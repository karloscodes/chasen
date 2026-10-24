package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker puts a `docker` script first on PATH. It records every call and
// answers just enough for a deploy: the proxy is running and every command succeeds,
// except `pull` when failPull is set. For a private app: the old container aja runs
// with OLD_RUNNING=1, the new one does not start with FAIL_RUN=1, and every
// container is at 127.0.0.1 for the health check.
func fakeDocker(t *testing.T, failPull bool) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$*" >> "` + logPath + `"
case "$*" in
  pull*) [ "$FAIL_PULL" = 1 ] && exit 1 ;;
  "ps -q --filter name=^matcha-proxy$") echo proxy-id ;;
  "ps -q --filter name=^aja$") [ "$OLD_RUNNING" = 1 ] && echo old-id ;;
  "run -d --name aja-next"*) [ "$FAIL_RUN" = 1 ] && exit 1 ;;
  "inspect -f {{.State.Running}}"*) echo "true 127.0.0.1" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if failPull {
		t.Setenv("FAIL_PULL", "1")
	}
	pullRetryDelay = 0
	return logPath
}

func newDeployable(t *testing.T) *Engine {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	app := AppConfig{Image: "ghcr.io/example/aja:latest", Domain: "aja.example.com", Port: 8080, HealthPath: "/up"}
	if err := SaveAppTo(cfgPath, "aja", app); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Name: "aja", AppImage: app.Image, AppPort: app.Port, HealthPath: app.HealthPath, ConfigPath: cfgPath})
	m.domain = app.Domain
	return m
}

func calls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func indexOf(lines []string, prefix string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func TestDeploy(t *testing.T) {
	t.Run("pulls the configured image before starting the new container", func(t *testing.T) {
		log := fakeDocker(t, false)
		m := newDeployable(t)

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		pull, run := indexOf(got, "pull ghcr.io/example/aja:latest"), indexOf(got, "run -d --name aja")
		if pull < 0 || run < 0 || pull > run {
			t.Errorf("want pull before run, got calls:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("does not pull when the caller already has the image", func(t *testing.T) {
		log := fakeDocker(t, false)
		m := newDeployable(t)
		m.config.SkipPull = true

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		if indexOf(got, "pull") >= 0 || indexOf(got, "run -d --name aja") < 0 {
			t.Errorf("want a run and no pull, got calls:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("caps the log of the container, so it cannot fill the disk", func(t *testing.T) {
		log := fakeDocker(t, false)
		m := newDeployable(t)

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		run := indexOf(got, "run -d --name aja")
		if run < 0 || !strings.Contains(got[run], "--log-opt max-size=10m --log-opt max-file=3") {
			t.Errorf("want a log limit on the run, got calls:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("deploys the local image when the pull fails", func(t *testing.T) {
		log := fakeDocker(t, true)
		m := newDeployable(t)

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if indexOf(calls(t, log), "run -d --name aja") < 0 {
			t.Error("want the container started from the local image")
		}
	})

	t.Run("deploys the record of the caller and reads no config file", func(t *testing.T) {
		log := fakeDocker(t, false)
		config := filepath.Join(t.TempDir(), "config.yml")
		m := New(Config{Name: "aja", ConfigPath: config, DataDirBase: t.TempDir(), SkipPull: true})

		err := m.DeployApp(AppConfig{
			Image: "ghcr.io/example/aja:1.2.0", Domain: "aja.example.com", Port: 3000, HealthPath: "/health",
			Volumes: []string{"/data"}, Env: map[string]string{"PRIVATE_KEY": "the-key"},
		})

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		run := indexOf(got, "run -d --name aja")
		if run < 0 {
			t.Fatalf("want a run, got calls:\n%s", strings.Join(got, "\n"))
		}
		for _, want := range []string{"-e PRIVATE_KEY=the-key", "-e AJA_PRIVATE_KEY=the-key", "-e AJA_DOMAIN=aja.example.com", "-e AJA_APP_PORT=3000", "/data", " ghcr.io/example/aja:1.2.0"} {
			if !strings.Contains(got[run], want) {
				t.Errorf("the run has no %q:\n%s", want, got[run])
			}
		}
		if proxy := indexOf(got, "exec matcha-proxy kamal-proxy deploy aja"); proxy < 0 || !strings.Contains(got[proxy], "aja.example.com") || !strings.Contains(got[proxy], "/health") {
			t.Errorf("want the proxy to get the domain and the health path of the record, got calls:\n%s", strings.Join(got, "\n"))
		}
		if _, err := os.Stat(config); !os.IsNotExist(err) {
			t.Errorf("the deploy made a config file (%v)", err)
		}
	})
}
