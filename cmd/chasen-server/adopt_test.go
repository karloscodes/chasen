package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/karloscodes/matcha"
)

// An app that matcha runs, in a real container. It needs Docker, so it runs
// with CHASEN_TEST_DOCKER=1.
func TestAdopt(t *testing.T) {
	if os.Getenv("CHASEN_TEST_DOCKER") == "" {
		t.Skip("set CHASEN_TEST_DOCKER=1: this test starts a container")
	}
	const name = "chasen-adopt-test"
	exec.Command("docker", "rm", "-f", name).Run()
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "busybox", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %s", out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	started := func() string {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.Id}} {{.State.StartedAt}}", name).Output()
		return strings.TrimSpace(string(out))
	}
	// server makes a Chasen server and a matcha config with the app and one more.
	server := func(t *testing.T) (matchaConfig string) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "secret"}); err != nil {
			t.Fatal(err)
		}
		matchaConfig = filepath.Join(t.TempDir(), "config.yml")
		app := matcha.AppConfig{Image: "busybox:latest", Domain: "shop.example.org", Port: 8080, HealthPath: "/_health",
			Volumes: []string{"/app/storage"}, Env: map[string]string{"PRIVATE_KEY": "the-key-of-the-app"}}
		if err := matcha.SaveAppTo(matchaConfig, name, app); err != nil {
			t.Fatal(err)
		}
		if err := matcha.SaveAppTo(matchaConfig, "other", matcha.AppConfig{Image: "busybox:latest", Domain: "other.example.org"}); err != nil {
			t.Fatal(err)
		}
		return matchaConfig
	}

	t.Run("the record moves to Chasen as it is, and the container keeps running", func(t *testing.T) {
		config := server(t)
		before := started()

		err := serverAdopt([]string{"--from", config, name})

		if err != nil {
			t.Fatal(err)
		}
		app, err := loadApp(name)
		if err != nil || app.Domain != "shop.example.org" || app.Env["PRIVATE_KEY"] != "the-key-of-the-app" || app.HealthPath != "/_health" || app.Image != "busybox:latest" {
			t.Errorf("Chasen has %+v (%v), want the record of matcha as it was", app, err)
		}
		if _, err := matcha.LoadAppFrom(config, name); err == nil {
			t.Error("matcha still has the app: two tools would run it")
		}
		if _, err := matcha.LoadAppFrom(config, "other"); err != nil {
			t.Error("the other app of matcha is gone")
		}
		if started() != before {
			t.Error("the container restarted or changed")
		}
		if copies, _ := filepath.Glob(config + ".bak.pre-chasen-*"); len(copies) != 1 {
			t.Errorf("want one copy of the config of matcha from before, got %v", copies)
		}
	})

	t.Run("undo gives the app back to matcha", func(t *testing.T) {
		config := server(t)
		if err := serverAdopt([]string{"--from", config, name}); err != nil {
			t.Fatal(err)
		}

		err := serverAdopt([]string{"--undo", "--from", config, name})

		if err != nil {
			t.Fatal(err)
		}
		if app, err := matcha.LoadAppFrom(config, name); err != nil || app.Env["PRIVATE_KEY"] != "the-key-of-the-app" {
			t.Errorf("matcha has %+v (%v), want the record back", app, err)
		}
		if _, err := loadApp(name); err == nil {
			t.Error("Chasen still has the app")
		}
	})

	t.Run("an app that matcha does not have changes nothing", func(t *testing.T) {
		config := server(t)

		err := serverAdopt([]string{"--from", config, "nothing"})

		if err == nil || !strings.Contains(err.Error(), "matcha has no app") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an app that does not run is not adopted", func(t *testing.T) {
		config := server(t)

		err := serverAdopt([]string{"--from", config, "other"})

		if err == nil || !strings.Contains(err.Error(), "does not run") {
			t.Fatalf("got %v", err)
		}
		if _, err := matcha.LoadAppFrom(config, "other"); err != nil {
			t.Error("matcha lost the app")
		}
	})
}
