package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// sentDeploy is what a server gets for a deploy: the app, the version, and
// the settings.
type sentDeploy struct {
	args     []string
	settings protocol.Settings
}

func TestDeployAnImage(t *testing.T) {
	// A mock server that keeps the deploy it gets, in an app directory with
	// settings and a Dockerfile that the deploy of an image must not use.
	deployTo := func(t *testing.T, args ...string) sentDeploy {
		server := mock.New(time.Now())
		server.Wait = func(context.Context, time.Duration) bool { return true }
		var sent sentDeploy
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/deploy" {
				body, _ := io.ReadAll(r.Body)
				line, _ := bufio.NewReader(bytes.NewReader(body)).ReadBytes('\n')
				sent.args = r.URL.Query()["arg"]
				json.Unmarshal(line, &sent.settings)
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			server.ServeHTTP(w, r)
		}))
		t.Cleanup(api.Close)
		t.Chdir(t.TempDir())
		os.WriteFile("chasen.yml", []byte("name: shop\nenv:\n  FOO: bar\n"), 0644)
		os.WriteFile("Dockerfile", []byte("FROM scratch\n"), 0644)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CHASEN_URL", api.URL)
		t.Setenv("CHASEN_TOKEN", server.Token)
		t.Setenv("GHCR_TOKEN", "test")
		t.Cleanup(func() { appFlag, tagFlag, domainFlag = "", "", "" })

		if err := runClient(args); err != nil {
			t.Fatal(err)
		}
		return sent
	}

	t.Run("deploys the newest image at the domain after it, named after it, with nothing of the directory", func(t *testing.T) {
		sent := deployTo(t, "deploy", "ghcr.io/acme/chat", "chat.example.com")

		if strings.Join(sent.args, " ") != "chat latest" {
			t.Errorf("args = %v, want chat latest", sent.args)
		}
		s := sent.settings
		if s.Image != "ghcr.io/acme/chat:latest" || s.Domain != "chat.example.com" || !s.KeepSettings || len(s.Env) != 0 {
			t.Errorf("settings = %+v, want the image, the domain, keep_settings, and no env", s)
		}
	})

	t.Run("takes the tag of the image and the name of -a", func(t *testing.T) {
		sent := deployTo(t, "deploy", "acme/chat:1.2", "-a", "team-chat")

		if strings.Join(sent.args, " ") != "team-chat 1.2" || sent.settings.Image != "acme/chat:1.2" {
			t.Errorf("args = %v, image = %s, want team-chat 1.2", sent.args, sent.settings.Image)
		}
	})
}

func TestIsImageRef(t *testing.T) {
	for word, want := range map[string]bool{
		"ghcr.io/acme/chat":          true,
		"acme/chat:1.2":              true,
		"ghcr.io/you/app@sha256:abc": true,
		"root@203.0.113.5":           false,
		"ubuntu@matcha-prod":         false,
		"example.com":                false,
		"cloud":                      false,
		"ssh://root@203.0.113.5":     false,
		"https://api.example.com":    false,
	} {
		if got := isImageRef(word); got != want {
			t.Errorf("isImageRef(%q) = %v, want %v", word, got, want)
		}
	}
}

func TestRailsMasterKey(t *testing.T) {
	rails := func(t *testing.T, files map[string]string) {
		t.Chdir(t.TempDir())
		for path, content := range files {
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte(content), 0600)
		}
	}

	t.Run("the key of config/master.key travels with the deploy", func(t *testing.T) {
		rails(t, map[string]string{"config/credentials.yml.enc": "x", "config/master.key": "abc123\n"})
		env := map[string]string{}

		err := railsMasterKey(env)

		if err != nil || env["RAILS_MASTER_KEY"] != "abc123" {
			t.Errorf("RAILS_MASTER_KEY = %q, %v, want the content of config/master.key", env["RAILS_MASTER_KEY"], err)
		}
	})

	t.Run("the key of the production credentials wins over the master key", func(t *testing.T) {
		rails(t, map[string]string{"config/master.key": "abc123", "config/credentials/production.key": "prod456"})
		env := map[string]string{}

		railsMasterKey(env)

		if env["RAILS_MASTER_KEY"] != "prod456" {
			t.Errorf("RAILS_MASTER_KEY = %q, want the production key", env["RAILS_MASTER_KEY"])
		}
	})

	t.Run("a key in the secrets wins over the file", func(t *testing.T) {
		rails(t, map[string]string{"config/master.key": "abc123"})
		env := map[string]string{"RAILS_MASTER_KEY": "from-secrets"}

		railsMasterKey(env)

		if env["RAILS_MASTER_KEY"] != "from-secrets" {
			t.Errorf("RAILS_MASTER_KEY = %q, want the one of the secrets", env["RAILS_MASTER_KEY"])
		}
	})

	t.Run("an app that is not Rails gets nothing", func(t *testing.T) {
		rails(t, map[string]string{"Dockerfile": "FROM scratch"})
		env := map[string]string{}

		err := railsMasterKey(env)

		if _, ok := env["RAILS_MASTER_KEY"]; ok || err != nil {
			t.Errorf("env = %v, %v, want no key", env, err)
		}
	})
}
