package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/karloscodes/chasen/oauth"
	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
)

// The API (see the protocol package). A request needs the token of the server, or
// the token of a login. Both allow every command on every app of the server.

const (
	apiPort        = "8080"
	agentContainer = "chasen-server"
	agentImage     = "docker:29-cli" // the Docker client and CA certificates
	maxRequestBody = 2 << 30         // the source of an app, as tar.gz
)

// serverServe is the long-running process of the server. It answers the API,
// keeps the live replica running, and makes the hourly backups.
func serverServe() error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return errors.New("no API token. Run: chasen-server setup")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	db, err := openServerDB()
	if err != nil {
		return err
	}
	// An entry that still runs is from before a restart of the server.
	db.Exec("UPDATE activity SET status = 'failed', finished_at = CURRENT_TIMESTAMP, output = output || 'The server restarted.' WHERE status = 'running'")

	if cfg.Backup.S3 != nil {
		go func() {
			// A restore stops the replica. This starts it again, and it waits for the restore to end.
			for {
				run(self, "replicate")
				time.Sleep(time.Second)
			}
		}()
	}
	go func() {
		for range time.Tick(time.Hour) {
			if err := run(self, "backup"); err != nil {
				log.Print("hourly backup failed: ", err)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /up", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	// allowed reports a request with the token of the server, or of a login.
	allowed := func(w http.ResponseWriter, r *http.Request) bool {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var login int
		db.QueryRow("SELECT count(*) FROM logins WHERE token_sha256 = ?", hashToken(token)).Scan(&login)
		if !isOwner(cfg, token) && login == 0 {
			http.Error(w, "not authorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	// The two requests that are not a command with a text output.
	mux.HandleFunc("GET "+protocol.ShellPath, func(w http.ResponseWriter, r *http.Request) {
		if allowed(w, r) {
			serveShell(w, r, db)
		}
	})
	mux.HandleFunc("GET "+protocol.DownloadPath, func(w http.ResponseWriter, r *http.Request) {
		if allowed(w, r) {
			serveDownload(w, r)
		}
	})
	mux.HandleFunc("POST /v1/{command}", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r) {
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.PathValue("command") == protocol.Logout {
			db.Exec("DELETE FROM logins WHERE token_sha256 = ?", hashToken(token))
			fmt.Fprintf(w, "%s0\n", protocol.ExitMarker)
			return
		}
		// setup, check, serve, and replicate are not in the protocol: they run only on the server itself.
		if !slices.Contains(protocol.Commands, r.PathValue("command")) {
			http.Error(w, "unknown command", http.StatusNotFound)
			return
		}
		serveCommand(w, r, self, db)
	})
	// `chasen login`: the user proves they own the server with its token, in a
	// browser. The client then gets a token of its own.
	login := &oauth.Server{
		Name: "api." + cfg.Domain,
		Authenticate: func(key string) (string, bool) {
			return "owner", isOwner(cfg, key)
		},
		Issue: func(string) (string, error) {
			token, err := matcha.GeneratePrivateKey()
			if err != nil {
				return "", err
			}
			_, err = db.Exec("INSERT INTO logins (token_sha256) VALUES (?)", hashToken(token))
			return token, err
		},
	}
	login.Register(mux)
	log.Print("chasen-server listens on :" + apiPort)
	api := &http.Server{Addr: ":" + apiPort, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	return api.ListenAndServe()
}

func isOwner(cfg serverConfig, token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(cfg.Token)) == 1
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// serveCommand runs this binary again with the command of the request, the
// same way the command runs on the server by hand. Each argument is one
// element of argv: no shell reads it. The command checks every argument.
func serveCommand(w http.ResponseWriter, r *http.Request, self string, db *sql.DB) {
	// Read the full body first. The response starts after the request ends.
	body, err := os.CreateTemp("", "chasen-request-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(body.Name())
	defer body.Close()
	if _, err := io.Copy(body, http.MaxBytesReader(w, r.Body, maxRequestBody)); err != nil {
		http.Error(w, "cannot read the request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A client that goes away stops `logs`. Every other command runs to its end,
	// so a lost connection never leaves a deploy half done.
	ctx := context.Background()
	if r.PathValue("command") == "logs" {
		ctx = r.Context()
	}
	out := &flushWriter{w: w, newline: true}
	args := r.URL.Query()["arg"]
	// A command that changes an app goes into the activity feed, with its output.
	var activity int64
	if action := recordedAction(r.PathValue("command"), args[min(1, len(args)):]); action != "" && len(args) > 0 && checkAppName(args[0]) == nil {
		activity, _ = startActivity(db, args[0], action)
		out.keep = &bytes.Buffer{}
		// The feed has the output while the command runs, so `chasen history`
		// and the screen can show a deploy that is on its way.
		out.save = func(output string) { saveActivity(db, activity, output) }
	}
	cmd := exec.CommandContext(ctx, self, append([]string{r.PathValue("command")}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = body, out, out
	// Stop the command together with the processes it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	code := 0
	if err := cmd.Run(); err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			fmt.Fprintln(out, "Error:", err)
		}
	}
	if activity != 0 {
		finishActivity(db, activity, code == 0, out.keep.String())
	}
	if !out.newline {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "%s%d\n", protocol.ExitMarker, code)
}

// flushWriter sends each write to the client at once.
type flushWriter struct {
	w       http.ResponseWriter
	newline bool          // the last byte was a newline
	keep    *bytes.Buffer // when set, a copy of the output for the activity feed
	save    func(output string)
	saved   time.Time // when save ran last
}

// saveEvery is how often a running command saves its output to the feed.
const saveEvery = time.Second

func (f *flushWriter) Write(p []byte) (int, error) {
	if f.keep != nil && f.keep.Len() < activityOutputMax {
		f.keep.Write(p)
		if f.save != nil && time.Since(f.saved) >= saveEvery {
			f.save(f.keep.String())
			f.saved = time.Now()
		}
	}
	n, err := f.w.Write(p)
	if n > 0 {
		f.newline = p[n-1] == '\n'
	}
	http.NewResponseController(f.w).Flush()
	return n, err
}

// startAgent runs `chasen-server serve` in a container on the network of the
// proxy. The container uses the binary and the Docker of the host. The paths
// inside the container are the same as on the host.
func startAgent(self string) error {
	if _, err := docker("network", "inspect", "matcha-network"); err != nil {
		if out, err := docker("network", "create", "matcha-network"); err != nil {
			return errors.New(out)
		}
	}
	etc, data := root()+"/etc/chasen", root()+"/var/matcha"
	if err := os.MkdirAll(data, 0755); err != nil {
		return err
	}
	docker("rm", "-f", agentContainer)
	out, err := docker("run", "-d", "--name", agentContainer, "--restart", "unless-stopped",
		"--log-opt", "max-size=10m", "--log-opt", "max-file=3", // the engine caps the logs of the apps the same way
		"--network", "matcha-network",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", self+":/usr/local/bin/chasen-server:ro",
		"-v", etc+":"+etc, "-v", data+":"+data,
		"-e", "CHASEN_ROOT="+root(),
		agentImage, "chasen-server", "serve")
	if err != nil {
		return fmt.Errorf("cannot start the API: %s", out)
	}
	return nil
}

// routeAgent makes the proxy serve the API on api.<base domain>, with a
// certificate. It reports false when the proxy does not run yet.
func routeAgent(cfg serverConfig) (bool, error) {
	if running, _ := docker("ps", "-q", "--filter", "name=^matcha-proxy$"); running == "" {
		return false, nil
	}
	if out, err := docker(agentRoute(cfg)...); err != nil {
		return false, fmt.Errorf("cannot route the API: %s", out)
	}
	return true, nil
}

// agentRoute is the docker command that routes the API. The API gets a
// certificate, unless the domain is local or another proxy does HTTPS.
func agentRoute(cfg serverConfig) []string {
	args := []string{"exec", "matcha-proxy", "kamal-proxy", "deploy", agentContainer,
		"--target", agentContainer + ":" + apiPort, "--host", "api." + cfg.Domain,
		"--health-check-path", "/up", "--target-timeout", "1h"}
	if !isLocal(cfg.Domain) && !cfg.PlainHTTP {
		args = append(args, "--tls")
	}
	return args
}

// isLocal reports a domain that only resolves on this machine. It gets no certificate.
func isLocal(domain string) bool {
	return domain == "localhost" || strings.HasSuffix(domain, ".localhost")
}
