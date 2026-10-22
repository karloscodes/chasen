package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.yml")
}

func TestSaveAndLoadMatchaConfig(t *testing.T) {
	path := testConfigPath(t)

	cfg := &MatchaConfig{
		Apps: map[string]AppConfig{
			"myapp": {
				Image:      "myorg/myapp:latest",
				Domain:     "myapp.example.com",
				Port:       3000,
				HealthPath: "/up",
				Volumes:    []string{"/app/storage"},
				Env:        map[string]string{"SECRET": "abc123"},
			},
		},
	}

	err := SaveMatchaConfigTo(cfg, path)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := LoadMatchaConfigFrom(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	app, ok := loaded.Apps["myapp"]
	if !ok {
		t.Fatal("app 'myapp' not found in loaded config")
	}
	if app.Image != "myorg/myapp:latest" {
		t.Errorf("Image = %q, want %q", app.Image, "myorg/myapp:latest")
	}
	if app.Domain != "myapp.example.com" {
		t.Errorf("Domain = %q, want %q", app.Domain, "myapp.example.com")
	}
	if app.Port != 3000 {
		t.Errorf("Port = %d, want %d", app.Port, 3000)
	}
	if app.HealthPath != "/up" {
		t.Errorf("HealthPath = %q, want %q", app.HealthPath, "/up")
	}
	if len(app.Volumes) != 1 || app.Volumes[0] != "/app/storage" {
		t.Errorf("Volumes = %v, want [/app/storage]", app.Volumes)
	}
	if app.Env["SECRET"] != "abc123" {
		t.Errorf("Env[SECRET] = %q, want abc123", app.Env["SECRET"])
	}
}

func TestSaveAndLoadApp(t *testing.T) {
	path := testConfigPath(t)

	app := AppConfig{
		Image:  "img:latest",
		Domain: "test.example.com",
		Port:   8080,
		Env:    map[string]string{"KEY": "val"},
	}

	err := SaveAppTo(path, "testapp", app)
	if err != nil {
		t.Fatalf("SaveApp failed: %v", err)
	}

	loaded, err := LoadAppFrom(path, "testapp")
	if err != nil {
		t.Fatalf("LoadApp failed: %v", err)
	}

	if loaded.Image != "img:latest" {
		t.Errorf("Image = %q, want img:latest", loaded.Image)
	}
	if loaded.Domain != "test.example.com" {
		t.Errorf("Domain = %q, want test.example.com", loaded.Domain)
	}
}

func TestLoadAppNotFound(t *testing.T) {
	path := testConfigPath(t)

	_, err := LoadAppFrom(path, "nonexistent")
	if err == nil {
		t.Fatal("expected error loading nonexistent app, got nil")
	}
}

func TestListApps(t *testing.T) {
	path := testConfigPath(t)

	SaveAppTo(path, "beta", AppConfig{Image: "img:1", Domain: "beta.example.com", Port: 3000})
	SaveAppTo(path, "alpha", AppConfig{Image: "img:2", Domain: "alpha.example.com", Port: 4000})

	apps, err := ListAppsFrom(path)
	if err != nil {
		t.Fatalf("ListApps failed: %v", err)
	}

	if len(apps) != 2 {
		t.Fatalf("got %d apps, want 2", len(apps))
	}

	sorted := ListAppsSorted(apps)
	if sorted[0] != "alpha" {
		t.Errorf("first app = %q, want alpha", sorted[0])
	}
	if sorted[1] != "beta" {
		t.Errorf("second app = %q, want beta", sorted[1])
	}
}

func TestRemoveApp(t *testing.T) {
	path := testConfigPath(t)

	SaveAppTo(path, "myapp", AppConfig{Image: "img:1", Domain: "test.com", Port: 8080})

	err := RemoveAppFrom(path, "myapp")
	if err != nil {
		t.Fatalf("RemoveApp failed: %v", err)
	}

	_, err = LoadAppFrom(path, "myapp")
	if err == nil {
		t.Fatal("expected error loading removed app, got nil")
	}
}

func TestRemoveAppNotFound(t *testing.T) {
	path := testConfigPath(t)

	err := RemoveAppFrom(path, "nonexistent")
	if err == nil {
		t.Fatal("expected error removing nonexistent app, got nil")
	}
}

func TestConfigPath(t *testing.T) {
	got := ConfigPath()

	if got != "/etc/matcha/config.yml" {
		t.Errorf("ConfigPath() = %q, want /etc/matcha/config.yml", got)
	}
}

func TestDataDir(t *testing.T) {
	got := DataDir("fusionaly")

	if got != "/var/matcha/fusionaly" {
		t.Errorf("DataDir() = %q, want /var/matcha/fusionaly", got)
	}
}

func TestSaveAppPreservesOtherApps(t *testing.T) {
	path := testConfigPath(t)

	SaveAppTo(path, "app1", AppConfig{Image: "img:1", Domain: "a.com", Port: 8080})
	SaveAppTo(path, "app2", AppConfig{Image: "img:2", Domain: "b.com", Port: 9090})

	// Verify both exist
	app1, err := LoadAppFrom(path, "app1")
	if err != nil {
		t.Fatalf("app1 lost: %v", err)
	}
	if app1.Image != "img:1" {
		t.Errorf("app1 Image = %q, want img:1", app1.Image)
	}

	app2, err := LoadAppFrom(path, "app2")
	if err != nil {
		t.Fatalf("app2 lost: %v", err)
	}
	if app2.Image != "img:2" {
		t.Errorf("app2 Image = %q, want img:2", app2.Image)
	}
}

func TestLoadMatchaConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.yml")

	cfg, err := LoadMatchaConfigFrom(path)
	if err != nil {
		t.Fatalf("should not error on missing file: %v", err)
	}
	if len(cfg.Apps) != 0 {
		t.Errorf("expected empty apps, got %d", len(cfg.Apps))
	}
}

func TestConfigFilePermissions(t *testing.T) {
	path := testConfigPath(t)

	SaveAppTo(path, "test", AppConfig{Image: "img:1", Env: map[string]string{"SECRET": "s3cret"}})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}
