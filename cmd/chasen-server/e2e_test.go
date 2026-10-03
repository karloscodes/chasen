package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The end-to-end test deploys the example app with real Docker and a real
// proxy on ports 80 and 443. It needs root, because the server gives the data
// directory to the user of the image. Run it with bin/e2e, which also starts
// the S3 store for the offsite copies.
func TestEndToEnd(t *testing.T) {
	if os.Getenv("CHASEN_BIN") == "" {
		t.Skip("set CHASEN_BIN to the directory of the built chasen and chasen-server binaries")
	}
	bin := filepath.Join(os.Getenv("CHASEN_BIN"), "chasen")
	server := filepath.Join(os.Getenv("CHASEN_BIN"), "chasen-server")
	if out, _ := docker("ps", "-aq", "--filter", "name=^(matcha-proxy|chasen-server)$"); out != "" {
		t.Skip("a matcha proxy or a chasen server exists on this machine. The test does not touch it")
	}

	root := t.TempDir()
	app := filepath.Join(t.TempDir(), "example")
	t.Setenv("CHASEN_ROOT", root)
	// The client saves its login under the home directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	run := func(dir string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		t.Logf("$ %s %s\n%s", name, strings.Join(args, " "), out)
		return string(out), err
	}
	must := func(dir string, name string, args ...string) string {
		t.Helper()
		out, err := run(dir, name, args...)
		if err != nil {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		return out
	}
	// A private registry plays the part of ghcr.io. chasen logs in to it with
	// the password of chasen.yml, in a directory of its own, so the Docker of
	// the machine keeps no trace.
	registry := os.Getenv("CHASEN_TEST_REGISTRY")
	if registry == "" {
		t.Skip("set CHASEN_TEST_REGISTRY to a registry with the user chasen and the password chasen-e2e")
	}
	t.Setenv("REGISTRY_PASSWORD", "chasen-e2e")
	// The first lines of chasen.yml in this test: the app and its private image.
	yml := "name: example\nimage: " + registry + "/example\nregistry:\n  username: chasen\n  password: REGISTRY_PASSWORD\n"

	commit := func(file, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(app, file), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		must(app, "git", "add", "-A")
		must(app, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "change "+file)
	}
	get := func(host string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://127.0.0.1/", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	// browserLogin runs a login command and does what the user does in the
	// browser: it sends the code from the terminal and the key to the login page.
	browserLogin := func(target, key string, command ...string) (string, error) {
		t.Helper()
		var out lockedBuffer
		cmd := exec.Command(bin, command...)
		cmd.Dir = app
		// No token, and no browser to open.
		cmd.Env = append(os.Environ(), "CHASEN_TOKEN=", "PATH=/nonexistent")
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var code string
		for range 100 {
			if _, rest, ok := strings.Cut(out.String(), "Code: "); ok && strings.Contains(rest, "\n") {
				code = strings.TrimSpace(rest)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		resp, err := http.PostForm(target+"/oauth/device", url.Values{"user_code": {code}, "key": {key}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			cmd.Process.Kill()
			cmd.Wait()
			return out.String(), fmt.Errorf("the login page answered %s", resp.Status)
		}
		err = cmd.Wait()
		t.Logf("$ chasen %s\n%s", strings.Join(command, " "), out.String())
		return out.String(), err
	}

	must(".", "cp", "-r", "example", app)
	must(app, "git", "init", "-q")
	commit("chasen.yml", yml+"env:\n  GREETING: hello\n")
	offsite := os.Getenv("CHASEN_TEST_S3") != ""

	// setup starts the API in a container and prints the token.
	setup := func() (api, token string) {
		t.Helper()
		out := must(".", server, "setup", "--domain", "localhost")
		_, token, _ = strings.Cut(strings.TrimSpace(out), "asks for it): ")
		ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", agentContainer)
		if err != nil {
			t.Fatal(ip)
		}
		api = "http://" + ip + ":" + apiPort
		for range 50 {
			if resp, err := http.Get(api + "/up"); err == nil {
				resp.Body.Close()
				return api, token
			}
			time.Sleep(200 * time.Millisecond)
		}
		logs, _ := docker("logs", agentContainer)
		t.Fatalf("the API did not start: %s", logs)
		return
	}
	t.Cleanup(func() {
		run(".", "docker", "rm", "-f", agentContainer)
	})
	api, token := setup()
	if offsite {
		// The owner of the server sets the bucket. A wrong key changes nothing.
		bucket := []string{"bucket", "--endpoint", os.Getenv("CHASEN_TEST_S3"), "--region", "us-east-1", "--access-key-id", "minioadmin",
			"--name", "chasen-e2e-" + strings.ToLower(time.Now().UTC().Format(stampLayout))}
		t.Setenv("S3_SECRET_ACCESS_KEY", "wrong")
		if out, err := run(".", server, bucket...); err == nil || !strings.Contains(out, "nothing changed") {
			t.Fatalf("bucket with a wrong secret = %q, %v, want a refusal", out, err)
		}
		t.Setenv("S3_SECRET_ACCESS_KEY", "minioadmin")
		must(".", server, bucket...)
		if out := must(".", server, "bucket"); !strings.Contains(out, "chasen-e2e-") {
			t.Fatalf("bucket = %q, want the new bucket", out)
		}
		api, token = setup() // the address of the API can change when it restarts
	}

	var proxyID string
	t.Cleanup(func() {
		run(".", server, "remove", "example")
		run(".", server, "remove", "site")
		run(".", server, "remove", "lognorth")
		// Remove only the proxy that this test started. Another job on the machine can own a later one.
		if proxyID != "" {
			run(".", "docker", "rm", "-f", proxyID)
		}
		if images, _ := docker("images", "-q", "chasen.invalid/*"); images != "" {
			run(".", "docker", append([]string{"rmi", "-f"}, strings.Fields(images)...)...)
		}
	})

	t.Run("login needs the token of the server", func(t *testing.T) {
		if out, err := run(app, bin, "deploy"); err == nil || !strings.Contains(out, "not logged in") {
			t.Errorf("deploy without a login = %q, %v, want a refusal", out, err)
		}
		if out, err := browserLogin(api, "wrong", "add", "server", api); err == nil {
			t.Errorf("login with a wrong key on the page = %q, want a refusal", out)
		}

		out, err := browserLogin(api, token, "add", "server", api)

		if err != nil || !strings.Contains(out, "Logged in to "+api) {
			t.Errorf("login = %q, %v, want a confirmation", out, err)
		}
	})

	t.Run("check tests a commit against the standard and changes nothing", func(t *testing.T) {
		out := must(app, bin, "check")

		if !strings.Contains(out, "All checks passed.") || !strings.Contains(out, "the port is 8080") || !strings.Contains(out, "the app stops on SIGTERM") {
			t.Errorf("check of the example app = %q, want every rule to pass", out)
		}
		if out := must(app, bin, "list"); strings.Contains(out, "example") {
			t.Errorf("list after a check = %q, want no app: a check deploys nothing", out)
		}
	})

	t.Run("load shows the load, the memory, and the disk of the machine, through the API", func(t *testing.T) {
		out := must(app, bin, "load")

		for _, want := range []string{"Load:", "cores, amd64)", "Memory:", "Disk:", "%)"} {
			if !strings.Contains(out, want) {
				t.Errorf("load = %q, want %q in it", out, want)
			}
		}
	})

	t.Run("alerts say what is wrong with the server, through the API", func(t *testing.T) {
		out := must(app, bin, "alerts")

		// What the machine of the test lacks is not known here: the check answers, with its time.
		if !strings.Contains(out, "Checked at") {
			t.Errorf("alerts = %q, want the time of the check", out)
		}
	})

	t.Run("a private image needs the login of its registry", func(t *testing.T) {
		t.Setenv("REGISTRY_PASSWORD", "wrong")

		out, err := run(app, bin, "deploy")

		if err == nil || !strings.Contains(out, "refused the login") {
			t.Errorf("deploy with a wrong registry password = %q, %v, want a refusal", out, err)
		}
		if out := must(app, bin, "list"); strings.Contains(out, "example") {
			t.Errorf("list = %q, want no app", out)
		}
	})

	t.Run("an app with a Dockerfile and no image gets the instructions", func(t *testing.T) {
		os.WriteFile(filepath.Join(app, "chasen.yml"), []byte("name: example\n"), 0644)
		defer must(app, "git", "checkout", "-q", "chasen.yml")

		out, err := run(app, bin, "deploy")

		if err == nil || !strings.Contains(out, "does not build on the server") || !strings.Contains(out, "image: ghcr.io/") {
			t.Errorf("deploy with no image = %q, %v, want the instructions", out, err)
		}
	})

	t.Run("the first deploy builds the image of the commit, pushes it, and serves it on its default domain", func(t *testing.T) {
		out := must(app, bin, "deploy")

		for _, step := range []string{"Building ", "Pushing ", "Pulling "} {
			if !strings.Contains(out, step+registry+"/example:") {
				t.Errorf("deploy = %q, want %q", out, step)
			}
		}

		if got := get("example.localhost"); !strings.HasPrefix(got, "hits=1 ") || !strings.HasSuffix(got, "greeting=hello") {
			t.Errorf("GET example.localhost = %q, want hits=1 and greeting=hello", got)
		}
	})

	proxyID, _ = docker("ps", "-q", "--filter", "name=^matcha-proxy$")

	t.Run("the API answers through the proxy, on its own domain", func(t *testing.T) {
		// The first deploy started the proxy. setup now gives the API a route.
		_, token := setup()

		if out, err := browserLogin("http://api.localhost", token, "add", "server", "http://api.localhost"); err != nil {
			t.Fatalf("login through the proxy = %q, %v", out, err)
		}

		if out := must(app, bin, "list"); !strings.Contains(out, "example.localhost") {
			t.Errorf("list through the proxy = %q, want the example app", out)
		}
	})

	t.Run("a wrong token gets a refusal", func(t *testing.T) {
		t.Setenv("CHASEN_URL", "http://api.localhost")
		t.Setenv("CHASEN_TOKEN", "wrong")

		out, err := run(app, bin, "list")

		if err == nil || !strings.Contains(out, "does not accept the token") {
			t.Errorf("list with a wrong token = %q, %v, want a refusal", out, err)
		}
	})

	t.Run("the next deploy keeps the data, applies new env, and makes a backup first", func(t *testing.T) {
		commit("chasen.yml", yml+"env:\n  GREETING: hola\nsecrets: [TOKEN]\nsecrets_command: echo TOKEN=s3cret\n")
		// A tag deploys an image that is in the registry. It builds nothing.
		if out, err := run(app, bin, "deploy", "--tag", "not-pushed"); err == nil || !strings.Contains(out, "Is the image pushed?") || strings.Contains(out, "Building") {
			t.Errorf("deploy of a tag that is not in the registry = %q, %v, want: cannot pull, and no build", out, err)
		}

		must(app, bin, "deploy")

		if got := get("example.localhost"); !strings.HasPrefix(got, "hits=2 ") || !strings.HasSuffix(got, "greeting=hola") {
			t.Errorf("GET example.localhost = %q, want hits=2 and greeting=hola", got)
		}
		if out := must(app, bin, "history"); strings.Count(out, "succeeded") != 2 || !strings.Contains(out, "deploy ") {
			t.Errorf("history = %q, want the two deploys, both succeeded", out)
		}
		if out := must(app, bin, "history", "1"); !strings.Contains(out, "Deployed example") {
			t.Errorf("history 1 = %q, want the output of the first deploy", out)
		}
		if out := must(app, bin, "backups"); !strings.Contains(out, "Z  server") {
			t.Errorf("backups = %q, want one backup on the server", out)
		}
		env, _ := docker("exec", "example", "sh", "-c", "echo $TOKEN")
		if next, _ := docker("exec", "example-next", "sh", "-c", "echo $TOKEN"); env != "s3cret" && next != "s3cret" {
			t.Errorf("TOKEN in the container = %q / %q, want s3cret", env, next)
		}
	})

	t.Run("rollback starts the version before, from the image the server kept, and again goes forward", func(t *testing.T) {
		version := func() string {
			_, rest, _ := strings.Cut(must(app, bin, "status"), "Version:")
			return strings.Fields(rest)[0]
		}
		newest := version()

		out := must(app, bin, "rollback")

		older := version()
		if !strings.Contains(out, "Rolled back example to "+older) || older == newest || strings.Contains(out, "Pulling") {
			t.Errorf("rollback = %q, now %s, want the version before %s, with no pull", out, older, newest)
		}
		must(app, bin, "rollback")
		if now := version(); now != newest {
			t.Errorf("the second rollback runs %s, want %s again", now, newest)
		}
		if out := must(app, bin, "history"); !strings.Contains(out, "rollback") {
			t.Errorf("history = %q, want the rollbacks", out)
		}
	})

	t.Run("restart applies new configuration and secrets, from the same image", func(t *testing.T) {
		// The change is in the working directory only: a restart needs no commit.
		// The secret key of the app comes as a secret too: then it is the owner's key, not one the server made.
		const key = "0123456789abcdef0123456789abcdef-kept-by-the-owner"
		os.WriteFile(filepath.Join(app, "chasen.yml"), []byte(yml+"port: 9000\nmemory: 300m\nenv:\n  GREETING: adios\nsecrets: [TOKEN, SECRET_KEY_BASE]\nsecrets_command: printf 'TOKEN=rotated\\nSECRET_KEY_BASE="+key+"\\n'\n"), 0644)

		out := must(app, bin, "restart")

		if strings.Contains(out, "exporting to image") || !strings.Contains(out, "Restarted example") {
			t.Errorf("restart = %q, want a restart and no build", out)
		}
		greeting, _ := docker("ps", "-q", "--filter", "name=^example(-next)?$")
		values, _ := docker("exec", greeting, "sh", "-c", "echo $GREETING $TOKEN $PORT")
		if values != "adios rotated 9000" {
			t.Errorf("env in the container = %q, want the new greeting, the rotated secret, and the port of chasen.yml", values)
		}
		if memory, _ := docker("inspect", "-f", "{{.HostConfig.Memory}}", greeting); memory != "314572800" {
			t.Errorf("the memory of the container = %q bytes, want the 300m of chasen.yml", memory)
		}
		if keys, _ := docker("exec", greeting, "sh", "-c", "echo $SECRET_KEY_BASE $PRIVATE_KEY"); keys != key+" "+key {
			t.Errorf("the secret key in the container = %q, want the key that the deploy brought, under both names", keys)
		}
		must(app, "git", "checkout", "-q", "chasen.yml")
		must(app, bin, "restart")
		// A restart that brings no key keeps the one the app has.
		if out := must(app, bin, "run", "sh", "-c", "echo $SECRET_KEY_BASE"); !strings.Contains(out, key) {
			t.Errorf("the secret key after a restart with no key = %q, want the same key", out)
		}
	})

	t.Run("run runs a command in the container of the app, and gives its exit code back", func(t *testing.T) {
		if out := must(app, bin, "run", "sh", "-c", `echo in-the-container && test -f "$DATABASE_PATH" && echo has-the-database`); !strings.Contains(out, "in-the-container") || !strings.Contains(out, "has-the-database") {
			t.Errorf("run = %q, want the output of the command, with the env and the storage of the app", out)
		}
		if out, err := run(app, bin, "run", "sh", "-c", "echo before-the-end; exit 3"); err == nil || !strings.Contains(out, "before-the-end") {
			t.Errorf("a command that fails: %q, %v, want its output and an error", out, err)
		}
		if out := must(app, bin, "history"); !strings.Contains(out, "run sh -c") {
			t.Errorf("history = %q, want the runs in it", out)
		}
	})

	t.Run("a custom domain serves the same app", func(t *testing.T) {
		must(app, bin, "domains", "add", "shop.localhost")

		if got := get("shop.localhost"); !strings.HasPrefix(got, "hits=3 ") {
			t.Errorf("GET shop.localhost = %q, want hits=3", got)
		}
		if out := must(app, bin, "domains"); out != "example.localhost\nshop.localhost\n" {
			t.Errorf("domains = %q", out)
		}
	})

	t.Run("restore brings the database back to the backup", func(t *testing.T) {
		must(app, bin, "backup")
		get("example.localhost") // hits=4, after the backup

		must(app, bin, "restore")

		if got := get("example.localhost"); !strings.HasPrefix(got, "hits=4 ") {
			t.Errorf("GET after restore = %q, want hits=4: the 3 in the backup and this request", got)
		}
	})

	t.Run("ssh opens a shell in the container of the app, through the proxy, and gives its exit code back", func(t *testing.T) {
		shell := exec.Command(bin, "ssh")
		shell.Dir = app
		shell.Stdin = strings.NewReader("echo in-the-shell-$((40+2)); test -f \"$DATABASE_PATH\" && echo has-the-database\nexit 3\n")

		out, err := shell.CombinedOutput()

		t.Logf("$ chasen ssh\n%s", out)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Errorf("ssh ended with %v, want the exit code 3 of the shell", err)
		}
		if !strings.Contains(string(out), "in-the-shell-42") || !strings.Contains(string(out), "has-the-database") {
			t.Errorf("ssh = %q, want the output of the commands, with the env and the storage of the app", out)
		}
		if out := must(app, bin, "history"); !strings.Contains(out, "ssh") {
			t.Errorf("history = %q, want the shell in it", out)
		}
	})

	t.Run("download saves the databases of the newest backup, ready to open", func(t *testing.T) {
		here := t.TempDir()

		must(here, bin, "-a", "example", "download")

		files, _ := filepath.Glob(filepath.Join(here, "example-*.tar.gz"))
		if len(files) != 1 {
			t.Fatalf("the directory has %v, want one example-<backup>.tar.gz", files)
		}
		file, err := os.Open(files[0])
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		archive := tar.NewReader(unzipped)
		entry, err := archive.Next()
		if err != nil {
			t.Fatal(err)
		}
		start := make([]byte, 15)
		io.ReadFull(archive, start)
		if string(start) != "SQLite format 3" {
			t.Errorf("%s starts with %q, want a SQLite database that is not compressed", entry.Name, start)
		}
	})

	if offsite {
		t.Run("the bucket is shown and set from the CLI, and the replica runs on", func(t *testing.T) {
			shown := must(app, bin, "bucket")
			name := strings.Fields(strings.TrimPrefix(shown, "Bucket "))[0]

			// The same bucket again, through the API: the server tests it, saves it, and starts the replica again.
			out := must(app, bin, "bucket", "--endpoint", os.Getenv("CHASEN_TEST_S3"), "--region", "us-east-1", "--access-key-id", "minioadmin", "--name", name)

			if !strings.HasPrefix(shown, "Bucket chasen-e2e-") || !strings.Contains(out, "Backups now go to the bucket "+name) {
				t.Errorf("bucket = %q, set = %q", shown, out)
			}
			replica := ""
			for range 20 {
				if replica = must(app, bin, "status"); strings.Contains(replica, "Replica:  live") {
					break
				}
				time.Sleep(time.Second)
			}
			if !strings.Contains(replica, "Replica:  live") {
				t.Errorf("status = %q, want the replica live again with no restart of the API", replica)
			}
			// The proof of a copy is a restore: verify restores the replica and the snapshot next to the real database.
			verified := ""
			for range 20 {
				if verified, _ = run(app, bin, "verify"); strings.Contains(verified, "The copies restore.") {
					break
				}
				time.Sleep(time.Second)
			}
			if !strings.Contains(verified, "ok    the live replica restores") || !strings.Contains(verified, "ok    the snapshot") {
				t.Errorf("verify = %q, want the live replica and the snapshot restored and checked", verified)
			}
			if out := must(app, bin, "bucket"); !strings.Contains(out, "example") || !strings.Contains(out, "LAST COPIED CHANGE") {
				t.Errorf("bucket = %q, want what the bucket holds for the app", out)
			}
		})
	}

	t.Run("the CLI reaches the server through SSH, with no address on the web", func(t *testing.T) {
		address := sshServer(t, root, os.Getenv("CHASEN_BIN"))

		out := must(app, bin, "add", "server", strings.TrimPrefix(address, "ssh://"))

		if !strings.Contains(out, "Logged in to "+address) {
			t.Errorf("add server = %q, want a login through SSH, with a token that the server made for it", out)
		}
		saved, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config/chasen/credentials.json"))
		if strings.Contains(string(saved), token) {
			t.Error("the CLI has the token of the server itself: an SSH login must get a token of its own")
		}
		if out := must(app, bin, "list"); !strings.Contains(out, "example") {
			t.Errorf("list through SSH = %q, want the app", out)
		}
		if out := must(app, bin, "run", "sh", "-c", "echo through-ssh"); !strings.Contains(out, "through-ssh") {
			t.Errorf("run through SSH = %q, want the output of the command", out)
		}
		if out := must(app, bin, "history"); !strings.Contains(out, "run sh -c echo through-ssh") {
			t.Errorf("history = %q, want the run: a command through SSH is a command of the API", out)
		}

		// An app with no image in chasen.yml goes from this computer to the
		// server through SSH, with no registry on the internet and no login.
		t.Cleanup(func() {
			docker("rm", "-f", "chasen-registry")
			docker("volume", "rm", "chasen-registry")
		})
		direct := filepath.Join(t.TempDir(), "direct")
		must(".", "cp", "-r", "example", direct)
		os.WriteFile(filepath.Join(direct, "chasen.yml"), []byte("name: direct\n"), 0644)
		must(direct, "git", "init", "-q")
		must(direct, "git", "add", "-A")
		must(direct, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "direct")

		out = must(direct, bin, "deploy")

		if !strings.Contains(out, "Deployed direct") || strings.Contains(out, "ghcr.io") {
			t.Errorf("deploy through SSH = %q, want the image of this computer, and no registry", out)
		}
		if got := get("direct.localhost"); !strings.HasPrefix(got, "hits=1 ") {
			t.Errorf("GET direct.localhost = %q, want the app", got)
		}
		must(direct, bin, "remove")
		// The tests after this one use the address on the web again, and they count the logins.
		must(app, bin, "logout")
		must(app, bin, "use", api)
	})

	t.Run("a folder with an image and no Dockerfile deploys the newest image, and the server names its version", func(t *testing.T) {
		// The release of another repository: the image of the app, pushed as latest.
		commit := strings.TrimSpace(must(app, "git", "rev-parse", "HEAD"))
		config := t.TempDir()
		login := exec.Command("docker", "--config", config, "login", "-u", "chasen", "--password-stdin", registry)
		login.Stdin = strings.NewReader("chasen-e2e")
		if out, err := login.CombinedOutput(); err != nil {
			t.Fatalf("docker login: %s", out)
		}
		must(".", "docker", "tag", registry+"/example:"+commit, registry+"/example:latest")
		must(".", "docker", "--config", config, "push", "-q", registry+"/example:latest")
		// The folder of the one who runs it: the settings, and nothing to build.
		released := t.TempDir()
		settings, _ := os.ReadFile(filepath.Join(app, "chasen.yml"))
		os.WriteFile(filepath.Join(released, "chasen.yml"), settings, 0644)

		out := must(released, bin, "deploy")

		if !strings.Contains(out, "No Dockerfile here") || strings.Contains(out, "Building") {
			t.Errorf("deploy = %q, want the newest image and no build", out)
		}
		if !strings.Contains(out, "The newest image is") {
			t.Errorf("deploy = %q, want the server to name the version of the image", out)
		}
		if status := must(released, bin, "status"); strings.Contains(status, "Version:  latest") {
			t.Errorf("status = %q, want a version that says what runs, not latest", status)
		}
	})

	t.Run("a deploy that does not get healthy keeps the previous version live", func(t *testing.T) {
		commit("app.py", "raise SystemExit('broken')\n")

		out, err := run(app, bin, "deploy")

		if err == nil {
			t.Error("the deploy of a broken app reported success")
		}
		if !strings.Contains(out, "It stopped with exit code 1") || !strings.Contains(out, "  broken") {
			t.Errorf("deploy = %q, want how the app ended and its last lines", out)
		}
		if got := get("shop.localhost"); !strings.HasPrefix(got, "hits=5 ") {
			t.Errorf("GET after the failed deploy = %q, want hits=5 from the previous version", got)
		}
		if out := must(app, bin, "history"); !strings.Contains(out, "failed") {
			t.Errorf("history = %q, want the failed deploy", out)
		}
		if out, err := run(app, bin, "check"); err == nil || !strings.Contains(out, "FAIL") || !strings.Contains(out, "broken") {
			t.Errorf("check of the broken commit = %q, %v, want a failed health rule and the last lines of the app", out, err)
		}
		if out := must(app, bin, "domains", "add", "blog.localhost"); !strings.Contains(out, "Added") {
			t.Errorf("domains add after the failed deploy = %q, want it to redeploy the good version", out)
		}

		// An app that listens on its own port, and not on the one of the standard.
		good, _ := os.ReadFile("example/app.py")
		commit("app.py", strings.Replace(string(good), `int(os.environ["PORT"])`, "9000", 1))

		out, err = run(app, bin, "deploy")

		if err == nil || !strings.Contains(out, "It listens on port 9000, not on 8080. Add EXPOSE 9000") {
			t.Errorf("deploy of an app on port 9000 = %q, %v, want the port it listens on", out, err)
		}
	})

	t.Run("a directory with an index.html and no Dockerfile deploys as a website", func(t *testing.T) {
		site := filepath.Join(t.TempDir(), "site")
		os.MkdirAll(site, 0755)
		os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>hello site</h1>"), 0644)
		os.WriteFile(filepath.Join(site, "chasen.yml"), []byte("name: site\n"), 0644)
		must(site, "git", "init", "-q")
		must(site, "git", "add", "-A")
		must(site, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "site")

		// The first deploy brings the domain of the app: the app gets that one, and no name under the base domain.
		must(site, bin, "deploy", "--domain", "www.site.localhost")

		if got := get("www.site.localhost"); got != "<h1>hello site</h1>" {
			t.Errorf("GET www.site.localhost = %q, want the index.html", got)
		}
		if out := must(site, bin, "domains"); out != "www.site.localhost\n" {
			t.Errorf("domains = %q, want only the domain of the deploy", out)
		}
		// A later deploy with another domain does not move the app.
		if out := must(site, bin, "deploy", "--domain", "other.localhost"); !strings.Contains(out, "chasen domains add other.localhost") {
			t.Errorf("deploy with another domain = %q, want the app to keep its domain and a line that says how to add one", out)
		}
	})

	t.Run("enable runs a product from its image, with the same backups", func(t *testing.T) {
		if out, _ := docker("ps", "-aq", "--filter", "name=^lognorth(-next)?$"); out != "" {
			t.Skip("a lognorth container exists on this machine. The test does not touch it")
		}
		must(app, bin, "enable", "lognorth")

		req, _ := http.NewRequest("GET", "http://127.0.0.1/_health", nil)
		req.Host = "lognorth.localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET lognorth.localhost/_health = %s, want 200", resp.Status)
		}
		if out := must(app, bin, "-a", "lognorth", "status"); !strings.Contains(out, "http://lognorth.localhost") {
			t.Errorf("status of the addon = %q, want its URL", out)
		}
		if out := must(app, bin, "-a", "lognorth", "backup"); !strings.Contains(out, "lognorth: backup 2") {
			t.Errorf("backup of the addon = %q, want a backup of its database", out)
		}
		if out, err := run(app, bin, "-a", "lognorth", "domains", "add", "logs.localhost"); err == nil || !strings.Contains(out, "one domain") {
			t.Errorf("domains add on an addon = %q, %v, want a refusal", out, err)
		}
		if out, err := run(app, bin, "enable", "postgres"); err == nil || !strings.Contains(out, "fusionaly") {
			t.Errorf("enable of an unknown addon = %q, %v, want the list of addons", out, err)
		}
	})

	t.Run("a server that lost its data gets it back from the offsite copy on deploy", func(t *testing.T) {
		if !offsite {
			t.Skip("set CHASEN_TEST_S3")
		}
		good, _ := os.ReadFile("example/app.py")
		commit("app.py", string(good))
		if out := must(app, bin, "backups"); !strings.Contains(out, "server + offsite") || !strings.Contains(out, "live") {
			t.Errorf("backups = %q, want backups on the server and offsite, and the live replica", out)
		}
		if out := must(app, bin, "status"); !strings.Contains(out, "Replica:  live") {
			t.Errorf("status = %q, want the live replica", out)
		}
		time.Sleep(15 * time.Second) // the daemon finds a replaced database in 10 seconds, then is one second behind
		must(app, bin, "remove")
		must(".", "mv", filepath.Join(root, "var/matcha/example"), filepath.Join(root, "lost"))

		out := must(app, bin, "deploy")

		if !strings.Contains(out, "Restored the data of example from the live replica") {
			t.Errorf("deploy output does not report the restore")
		}
		if got := get("example.localhost"); !strings.HasPrefix(got, "hits=6 ") {
			t.Errorf("GET on the new server = %q, want hits=6: the 5 before the loss and this request", got)
		}
	})

	t.Run("logout forgets one login and keeps the other", func(t *testing.T) {
		must(app, bin, "logout")

		// The test logged in two times: the server by its address, and by its name.
		if out := must(app, bin, "servers"); strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "* 1  http") {
			t.Errorf("servers after one logout = %q, want one login, and it is the current one", out)
		}
		must(app, bin, "logout")
		if out, err := run(app, bin, "list"); err == nil || !strings.Contains(out, "not logged in") {
			t.Errorf("list after the last logout = %q, %v, want: not logged in", out, err)
		}
	})

	t.Run("update installs the newest release, and goes back when the new API does not start", func(t *testing.T) {
		// A release is a directory on a web server: the binary and checksums.txt.
		release := t.TempDir()
		asset := "chasen-server-linux-" + runtime.GOARCH
		publish := func(binary []byte) {
			t.Helper()
			os.WriteFile(filepath.Join(release, asset), binary, 0644)
			os.WriteFile(filepath.Join(release, "checksums.txt"), []byte(checksum(binary)+"  "+asset+"\n"), 0644)
		}
		web := httptest.NewServer(http.FileServer(http.Dir(release)))
		defer web.Close()
		t.Setenv("CHASEN_DOWNLOADS", web.URL)
		apiUp := func() bool {
			req, _ := http.NewRequest("GET", "http://127.0.0.1/up", nil)
			req.Host = "api.localhost"
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
		current, err := os.ReadFile(server)
		if err != nil {
			t.Fatal(err)
		}

		publish(current)
		if out := must(".", server, "update"); !strings.Contains(out, "up to date") {
			t.Errorf("update to the same release = %q, want: up to date", out)
		}

		// The same program with other bytes at its end: a new release that works.
		newer := append(slices.Clone(current), []byte("\n# a newer release\n")...)
		publish(newer)
		appBefore, _ := docker("ps", "-q", "--filter", "name=^example")
		out := must(".", server, "update")
		installed, _ := os.ReadFile(server)
		if !strings.Contains(out, "Updated to") || checksum(installed) != checksum(newer) || !apiUp() {
			t.Errorf("update = %q, want the new binary in place and the API up", out)
		}
		if appAfter, _ := docker("ps", "-q", "--filter", "name=^example"); appBefore == "" || appAfter != appBefore {
			t.Errorf("the container of the app was %q and is %q, want the same one: an update does not restart the apps", appBefore, appAfter)
		}

		// A release that cannot start: the server must keep the version it has.
		publish([]byte("#!/bin/sh\nexit 1\n"))
		out, err = run(".", server, "update")
		installed, _ = os.ReadFile(server)
		if err == nil || !strings.Contains(out, "The previous version runs again") || checksum(installed) != checksum(newer) || !apiUp() {
			t.Errorf("update to a broken release = %q, %v, want the previous binary back and the API up", out, err)
		}
	})
}

// lockedBuffer collects the output of a command that still runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sshServer starts a real SSH server on this machine, for the user root, and
// puts an `ssh` first on the PATH that logs in to it with a key of the test.
// A command through this SSH finds the chasen-server of the test, and the
// root of the test. It returns the SSH address of the server.
func sshServer(t *testing.T, chasenRoot, binDir string) string {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
	}
	client, clientErr := exec.LookPath("ssh")
	if _, err := os.Stat(sshd); err != nil || clientErr != nil {
		t.Fatal("the test of the SSH way needs ssh and sshd on this machine: install openssh-server")
	}
	dir := t.TempDir()
	hostKey, clientKey := filepath.Join(dir, "host_key"), filepath.Join(dir, "client_key")
	for _, key := range []string{hostKey, clientKey} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %s", out)
		}
	}
	public, _ := os.ReadFile(clientKey + ".pub")
	os.WriteFile(filepath.Join(dir, "authorized_keys"), public, 0600)
	os.MkdirAll("/run/sshd", 0755) // sshd wants this directory

	// A free port: ask for one, and give it back.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()

	args := []string{"-D", "-e", "-p", port, "-f", "/dev/null", "-h", hostKey,
		"-o", "ListenAddress=127.0.0.1", "-o", "AuthorizedKeysFile=" + filepath.Join(dir, "authorized_keys"),
		"-o", "StrictModes=no", "-o", "PasswordAuthentication=no", "-o", "PermitRootLogin=yes", "-o", "PidFile=" + filepath.Join(dir, "sshd.pid"),
		// What a real server has by itself: chasen-server on the PATH, and its files in /etc/chasen.
		"-o", "SetEnv=CHASEN_ROOT=" + chasenRoot + " PATH=" + binDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	if _, err := os.Stat("/etc/pam.d/sshd"); err == nil {
		args = append(args, "-o", "UsePAM=yes") // a root account with no password is locked without it
	}
	var log lockedBuffer
	daemon := exec.Command(sshd, args...)
	daemon.Stdout, daemon.Stderr = &log, &log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Kill()
		daemon.Wait()
		t.Logf("sshd:\n%s", log.String())
	})
	for range 50 {
		if conn, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	script := "#!/bin/sh\nexec " + client + " -i " + clientKey + " -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \"$@\"\n"
	wrapper := t.TempDir()
	os.WriteFile(filepath.Join(wrapper, "ssh"), []byte(script), 0755)
	os.Chmod(wrapper, 0755)
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	return "ssh://root@127.0.0.1:" + port
}
