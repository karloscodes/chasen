package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

	t.Run("restart applies new configuration and secrets, from the same image", func(t *testing.T) {
		// The change is in the working directory only: a restart needs no commit.
		os.WriteFile(filepath.Join(app, "chasen.yml"), []byte(yml+"port: 9000\nenv:\n  GREETING: adios\nsecrets: [TOKEN]\nsecrets_command: echo TOKEN=rotated\n"), 0644)

		out := must(app, bin, "restart")

		if strings.Contains(out, "exporting to image") || !strings.Contains(out, "Restarted example") {
			t.Errorf("restart = %q, want a restart and no build", out)
		}
		greeting, _ := docker("ps", "-q", "--filter", "name=^example(-next)?$")
		values, _ := docker("exec", greeting, "sh", "-c", "echo $GREETING $TOKEN $PORT")
		if values != "adios rotated 9000" {
			t.Errorf("env in the container = %q, want the new greeting, the rotated secret, and the port of chasen.yml", values)
		}
		must(app, "git", "checkout", "-q", "chasen.yml")
		must(app, bin, "restart")
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

	t.Run("a deploy that does not get healthy keeps the previous version live", func(t *testing.T) {
		commit("app.py", "raise SystemExit('broken')\n")

		_, err := run(app, bin, "deploy")

		if err == nil {
			t.Error("the deploy of a broken app reported success")
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
	})

	t.Run("a directory with an index.html and no Dockerfile deploys as a website", func(t *testing.T) {
		site := filepath.Join(t.TempDir(), "site")
		os.MkdirAll(site, 0755)
		os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>hello site</h1>"), 0644)
		os.WriteFile(filepath.Join(site, "chasen.yml"), []byte("name: site\n"), 0644)
		must(site, "git", "init", "-q")
		must(site, "git", "add", "-A")
		must(site, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "site")

		must(site, bin, "deploy")

		if got := get("site.localhost"); got != "<h1>hello site</h1>" {
			t.Errorf("GET site.localhost = %q, want the index.html", got)
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
		if out := must(app, bin, "-a", "lognorth", "status"); !strings.Contains(out, "https://lognorth.localhost") {
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
		if out := must(app, bin, "servers"); strings.Count(out, "\n") != 1 || !strings.Contains(out, "* http") {
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
