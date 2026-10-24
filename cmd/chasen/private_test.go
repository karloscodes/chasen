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
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
)

func TestPrivateDeploy(t *testing.T) {
	// A mock server of version, which keeps the deploy it gets.
	deployTo := func(t *testing.T, version string, args ...string) (sentDeploy, error) {
		server := mock.New(time.Now())
		server.Wait = func(context.Context, time.Duration) bool { return true }
		server.Version = version
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
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CHASEN_URL", api.URL)
		t.Setenv("CHASEN_TOKEN", server.Token)
		t.Cleanup(func() { appFlag, tagFlag, domainFlag, privateFlag, autoUpdateFlag = "", "", "", "", nil })

		err := runClient(args)

		return sent, err
	}

	t.Run("--private sends 127.0.0.1, and the word after it stays the image", func(t *testing.T) {
		sent, err := deployTo(t, "v0.13.0", "deploy", "--private", "ghcr.io/acme/logs")

		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(sent.args, " ") != "logs latest" || sent.settings.Private != "127.0.0.1" || sent.settings.Domain != "" {
			t.Errorf("args = %v, settings = %+v, want logs latest at 127.0.0.1 and no domain", sent.args, sent.settings)
		}
	})

	t.Run("--private= sends the address of the server in a VPN, with its port", func(t *testing.T) {
		sent, err := deployTo(t, "v0.13.1", "deploy", "ghcr.io/acme/logs", "--private=10.8.0.1:3001")

		if err != nil || sent.settings.Private != "10.8.0.1:3001" {
			t.Errorf("private = %q (%v), want 10.8.0.1:3001", sent.settings.Private, err)
		}
	})

	t.Run("refuses --private with --domain, and sends nothing", func(t *testing.T) {
		sent, err := deployTo(t, "v0.13.0", "deploy", "ghcr.io/acme/logs", "--private", "--domain", "logs.example.com")

		if err == nil || !strings.Contains(err.Error(), "not both") || sent.args != nil {
			t.Errorf("err = %v, sent = %v, want a refusal before the deploy", err, sent.args)
		}
	})

	t.Run("refuses an address that the internet reaches", func(t *testing.T) {
		sent, err := deployTo(t, "v0.13.0", "deploy", "ghcr.io/acme/logs", "--private=0.0.0.0")

		if err == nil || !strings.Contains(err.Error(), "not a private address") || sent.args != nil {
			t.Errorf("err = %v, sent = %v, want a refusal before the deploy", err, sent.args)
		}
	})

	t.Run("refuses a server from before private apps: it would put the app on the internet", func(t *testing.T) {
		sent, err := deployTo(t, "", "deploy", "--private", "ghcr.io/acme/logs")

		if err == nil || !strings.Contains(err.Error(), "from before private apps") || sent.args != nil {
			t.Errorf("err = %v, sent = %v, want a refusal before the deploy", err, sent.args)
		}
	})
}

func TestPrivateInChasenYml(t *testing.T) {
	load := func(t *testing.T, file string) (appFile, error) {
		t.Chdir(t.TempDir())
		os.WriteFile("chasen.yml", []byte(file), 0644)
		return loadAppFile()
	}

	t.Run("private: true is 127.0.0.1", func(t *testing.T) {
		app, err := load(t, "name: logs\nprivate: true\n")

		if err != nil || app.Private != "127.0.0.1" {
			t.Errorf("private = %q (%v), want 127.0.0.1", app.Private, err)
		}
	})

	t.Run("private: takes the address of the server in a VPN, with its port", func(t *testing.T) {
		app, err := load(t, "name: logs\nprivate: 100.101.102.103:3001\n")

		if err != nil || app.Private != "100.101.102.103:3001" {
			t.Errorf("private = %q (%v)", app.Private, err)
		}
	})

	t.Run("private: false is refused, with the way to the internet", func(t *testing.T) {
		_, err := load(t, "name: logs\nprivate: false\n")

		if err == nil || !strings.Contains(err.Error(), "chasen deploy --domain") {
			t.Errorf("err = %v, want the way to the internet", err)
		}
	})

	t.Run("the review finds an address that the internet reaches", func(t *testing.T) {
		app, _ := load(t, "name: logs\nprivate: 203.0.113.5\n")

		found := reviewAppFile(app, true, "")

		for _, f := range found {
			if f.Error && strings.Contains(f.What, "not a private address") {
				return
			}
		}
		t.Errorf("findings = %+v, want an error for the address", found)
	})
}

func TestPrivateAddress(t *testing.T) {
	t.Cleanup(func() { privateFlag, domainFlag = "", "" })
	cases := []struct {
		name, flag, domain, file, want, problem string
	}{
		{"the flag alone", "10.8.0.1", "", "", "10.8.0.1", ""},
		{"chasen.yml alone", "", "", "127.0.0.1", "127.0.0.1", ""},
		{"the two say one thing", "127.0.0.1", "", "127.0.0.1", "127.0.0.1", ""},
		{"the two say two things", "10.8.0.1", "", "127.0.0.1", "", "Keep one of them"},
		{"chasen.yml says private, and the command a domain", "", "logs.example.com", "127.0.0.1", "", "remove private: from chasen.yml"},
		{"neither", "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			privateFlag, domainFlag = c.flag, c.domain

			got, err := privateAddress(c.file)

			if c.problem == "" && (err != nil || got != c.want) {
				t.Errorf("privateAddress = %q, %v, want %q", got, err, c.want)
			}
			if c.problem != "" && (err == nil || !strings.Contains(err.Error(), c.problem)) {
				t.Errorf("privateAddress = %v, want an error with %q", err, c.problem)
			}
		})
	}
}

func TestAtLeast(t *testing.T) {
	for version, want := range map[string]bool{
		"v0.13.0": true, "v0.13.0-rc.1": true, "v0.14.2": true, "v1.0.0": true, "dev": true,
		"v0.12.8": false, "v0.9.30": false, "": false,
	} {
		if got := atLeast(version, privateSince); got != want {
			t.Errorf("atLeast(%q) = %v, want %v", version, got, want)
		}
	}
}
