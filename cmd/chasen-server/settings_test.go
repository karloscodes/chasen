package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
)

func TestServerSettings(t *testing.T) {
	t.Run("a server that is not set up says so", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		_, err := loadServerConfig()

		if err == nil || !strings.Contains(err.Error(), "chasen-server setup") {
			t.Fatalf("got %v, want the hint to run setup", err)
		}
	})

	t.Run("a server without a bucket is complete", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "secret"}); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadServerConfig()

		if err != nil {
			t.Fatal(err)
		}
		if cfg.Domain != "example.com" || cfg.Token != "secret" || cfg.Backup.S3 != nil || cfg.AutoUpdate != nil {
			t.Fatalf("got %+v", cfg)
		}
	})

	t.Run("the bucket, the heartbeat, and the update choice come back as saved", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		off := false
		saved := serverConfig{Domain: "example.com", Token: "secret", AutoUpdate: &off}
		saved.Backup.HeartbeatURL = "https://ping.example.com/1"
		saved.Backup.S3 = &s3Config{Endpoint: "https://s3.example.com", Region: "auto", Bucket: "backups", AccessKeyID: "id", SecretAccessKey: "key"}
		if err := saveServerConfig(saved); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadServerConfig()

		if err != nil {
			t.Fatal(err)
		}
		if *cfg.Backup.S3 != *saved.Backup.S3 || cfg.Backup.HeartbeatURL != saved.Backup.HeartbeatURL || cfg.AutoUpdate == nil || *cfg.AutoUpdate {
			t.Fatalf("got %+v with bucket %+v", cfg, cfg.Backup.S3)
		}
	})

	t.Run("a bucket that is taken away leaves no keys behind", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		cfg := serverConfig{Domain: "example.com", Token: "secret"}
		cfg.Backup.S3 = &s3Config{Endpoint: "https://s3.example.com", Bucket: "backups", AccessKeyID: "id", SecretAccessKey: "key"}
		if err := saveServerConfig(cfg); err != nil {
			t.Fatal(err)
		}

		cfg.Backup.S3 = nil
		if err := saveServerConfig(cfg); err != nil {
			t.Fatal(err)
		}

		left := query(t, root()+"/etc/chasen/server.sqlite3", "SELECT count(*) FROM settings WHERE name LIKE 's3_%'")
		if left != "0" {
			t.Fatalf("%s rows of the bucket are left", left)
		}
	})

	t.Run("the config.yml of an older version is copied once, and stays", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		os.MkdirAll(filepath.Dir(configPath()), 0755)
		old := "domain: example.com\ntoken: old-token\nauto_update: false\nbackup:\n  heartbeat_url: https://ping.example.com/1\n  s3:\n    endpoint: https://s3.example.com\n    region: auto\n    bucket: backups\n    access_key_id: id\n    secret_access_key: key\n"
		os.WriteFile(configPath(), []byte(old), 0600)

		cfg, err := loadServerConfig()

		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "old-token" || cfg.Backup.S3 == nil || cfg.Backup.S3.SecretAccessKey != "key" || cfg.AutoUpdate == nil || *cfg.AutoUpdate || cfg.Backup.HeartbeatURL == "" {
			t.Fatalf("got %+v with bucket %+v", cfg, cfg.Backup.S3)
		}
		if _, err := os.Stat(configPath()); err != nil {
			t.Fatal("the file is gone: the previous version cannot start after a rollback")
		}

		// From now on the database is the source: a change of the file does nothing.
		os.WriteFile(configPath(), []byte("domain: other.example\ntoken: new-token\n"), 0600)
		if cfg, _ = loadServerConfig(); cfg.Token != "old-token" {
			t.Fatalf("the file won over the database: token %q", cfg.Token)
		}
	})
}

func TestAppSettings(t *testing.T) {
	t.Run("the settings of a deploy come back, without the registry login", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		sent := protocol.Settings{Image: "ghcr.io/you/shop:abc", Env: map[string]string{"KEY": "value"}, Port: 3000}
		sent.Registry = &protocol.Registry{Username: "you", Password: "token"}
		if err := saveSettings("shop", sent); err != nil {
			t.Fatal(err)
		}

		got, err := loadSettings("shop")

		if err != nil {
			t.Fatal(err)
		}
		if got.Image != sent.Image || got.Env["KEY"] != "value" || got.Port != 3000 || got.Registry != nil {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("an app that was never deployed has none", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		got, err := loadSettings("shop")

		if err != nil || got.Image != "" || len(got.Env) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("the settings file of an older version still counts", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		os.MkdirAll(filepath.Dir(envPath("shop")), 0700)
		os.WriteFile(envPath("shop"), []byte(`{"image":"ghcr.io/you/shop:old","env":{"KEY":"old"}}`), 0600)

		got, err := loadSettings("shop")

		if err != nil || got.Env["KEY"] != "old" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("a removed app leaves no settings", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		os.MkdirAll(filepath.Dir(envPath("shop")), 0700)
		os.WriteFile(envPath("shop"), []byte(`{"env":{"KEY":"old"}}`), 0600)
		if err := saveSettings("shop", protocol.Settings{Env: map[string]string{"KEY": "new"}}); err != nil {
			t.Fatal(err)
		}

		forgetSettings("shop")

		if got, _ := loadSettings("shop"); len(got.Env) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestAddonKeepsItsEnvAtAnUpdate(t *testing.T) {
	had := map[string]string{"FORMLANDER_SESSION_SECRET": "the-secret", "PRIVATE_KEY": "old", "PORT": "9999"}
	standard := standardEnv([]string{"forms.example.com"}, "latest", "old", shape{Port: 8080, Volumes: []string{"/app/storage"}})

	env := addonEnv(had, standard)

	if env["FORMLANDER_SESSION_SECRET"] != "the-secret" {
		t.Error("the update took a setting of the app away")
	}
	if env["PORT"] != "8080" || env["PRIVATE_KEY"] != "old" || env["BASE_URL"] != "https://forms.example.com" {
		t.Errorf("the env of the standard is not there: %v", env)
	}
}

func TestCheckHasTheEnvOfADeploy(t *testing.T) {
	env := engineEnv("fusionaly", "fusionaly.check", 8080, "the-key")

	want := map[string]string{"FUSIONALY_PRIVATE_KEY": "the-key", "FUSIONALY_DOMAIN": "fusionaly.check", "FUSIONALY_APP_PORT": "8080", "FUSIONALY_ENV": "production"}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s is %q, want %q: the engine sets it at a deploy, so the check must too", name, env[name], value)
		}
	}
}

func TestVersionOfTheNewestImage(t *testing.T) {
	t.Run("the version that the build wrote in the image", func(t *testing.T) {
		if got := imageVersion("v3.2.2\n", "sha256:3c18953fc50018000e671b6f75f4f9d5fba52d30495370128497c"); got != "3.2.2" {
			t.Fatalf("got %q, want 3.2.2", got)
		}
	})

	t.Run("an image with no version gets the start of its id", func(t *testing.T) {
		for _, label := range []string{"", "<no value>", "not a version!"} {
			if got := imageVersion(label, "sha256:a7811c5ec9ab0123456789"); got != "a7811c5ec9ab" {
				t.Fatalf("label %q: got %q, want a7811c5ec9ab", label, got)
			}
		}
	})
}

// The CLI refuses these names in chasen.yml before a deploy, from the list in
// the protocol. The list must be the names that the server sets.
func TestStandardEnvNamesMatchTheProtocol(t *testing.T) {
	set := slices.Sorted(maps.Keys(standardEnv([]string{"shop.example.com"}, "1", "key", shape{Port: 8080, Volumes: []string{"/storage"}})))

	listed := slices.Sorted(slices.Values(protocol.StandardEnv))

	if !slices.Equal(set, listed) {
		t.Errorf("the server sets %v, and the protocol lists %v", set, listed)
	}
}
