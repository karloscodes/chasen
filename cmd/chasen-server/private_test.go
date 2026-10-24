package main

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/internal/engine"
)

// printedBy runs f and returns what it printed.
func printedBy(t *testing.T, f func() error) (string, error) {
	t.Helper()
	read, write, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = write
	err := f()
	os.Stdout = stdout
	write.Close()
	out, _ := io.ReadAll(read)
	return string(out), err
}

func TestPrivateApps(t *testing.T) {
	logs := engine.AppConfig{Image: "chasen.invalid/logs:1", Listen: "127.0.0.1:8080", Port: 8080, Env: map[string]string{"PRIVATE_KEY": "k"}}
	shop := engine.AppConfig{Image: "chasen.invalid/shop:1", Domain: "shop.example.com", Port: 3000, Env: map[string]string{"PRIVATE_KEY": "k"}}

	t.Run("the server keeps where a private app listens", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		err := saveApp("logs", logs)

		got, _ := loadApp("logs")
		if err != nil || !reflect.DeepEqual(got, logs) {
			t.Errorf("got %+v (%v), want %+v", got, err, logs)
		}
	})

	t.Run("the ports of the other private apps are taken", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("logs", logs)
		saveApp("admin", engine.AppConfig{Image: "chasen.invalid/admin:1", Listen: "10.8.0.1:3001"})
		saveApp("shop", shop)

		got := privatePorts("admin")

		if !reflect.DeepEqual(got, []int{8080}) {
			t.Errorf("privatePorts = %v, want the port of logs only", got)
		}
	})

	t.Run("a private app has no domain, and BASE_URL is its address on the server", func(t *testing.T) {
		if got := baseURL(appDomains(logs), logs.Listen); got != "http://127.0.0.1:8080" {
			t.Errorf("baseURL = %q", got)
		}
		if got := baseURL(appDomains(shop), shop.Listen); got != "https://shop.example.com" {
			t.Errorf("baseURL = %q", got)
		}
		if got := appDomains(logs); got != nil {
			t.Errorf("appDomains = %q, want none", got)
		}
	})

	t.Run("the list shows a private app with its address", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("logs", logs)
		saveApp("shop", shop)

		out, err := printedBy(t, serverList)

		if err != nil || !strings.Contains(out, "private 127.0.0.1:8080") || !strings.Contains(out, "shop.example.com") {
			t.Errorf("serverList printed:\n%s(%v)", out, err)
		}
	})

	t.Run("a private app takes no domain", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("logs", logs)

		err := serverDomains("logs", []string{"add", "logs.example.com"})

		if err == nil || !strings.Contains(err.Error(), "logs is private") || !strings.Contains(err.Error(), "chasen deploy --domain") {
			t.Errorf("serverDomains = %v, want a refusal that says how to put it on the internet", err)
		}
		if got, _ := loadApp("logs"); got.Domain != "" {
			t.Errorf("the app got the domain %q", got.Domain)
		}
	})

	t.Run("the screen gets the address of a private app", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("logs", logs)

		rows, err := overview()

		if err != nil || len(rows) != 1 || rows[0].Private != "127.0.0.1:8080" || len(rows[0].Domains) != 0 {
			t.Errorf("overview = %+v, %v", rows, err)
		}
	})

	t.Run("a server with only private apps needs no proxy", func(t *testing.T) {
		if _, err := docker("inspect", proxyContainer); err == nil {
			t.Skip("this machine has a proxy")
		}
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("logs", logs)

		alerts := proxyAlerts()

		if len(alerts) != 0 {
			t.Errorf("proxyAlerts = %+v, want none", alerts)
		}
	})
}
