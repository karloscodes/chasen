package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/net/websocket"
)

// A shell in a real container. It needs Docker, so it runs with
// CHASEN_TEST_DOCKER=1. The end-to-end test covers the same through the CLI.
func TestShellInAContainer(t *testing.T) {
	if os.Getenv("CHASEN_TEST_DOCKER") == "" {
		t.Skip("set CHASEN_TEST_DOCKER=1: this test starts a container")
	}
	const container = "chasen-shell-test"
	exec.Command("docker", "rm", "-f", container).Run()
	if out, err := exec.Command("docker", "run", "-d", "--name", container, "busybox", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %s", out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", container).Run() })
	api := httptest.NewServer(websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   func(ws *websocket.Conn) { shellSession(ws, container, 80, 24) },
	})
	defer api.Close()
	// session types the keys and returns what the terminal showed, and the exit code.
	session := func(t *testing.T, keys string) (string, int) {
		t.Helper()
		shell, err := protocol.Client{URL: api.URL}.Shell("shop", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer shell.Close()
		shell.Write([]byte(keys))
		var shown strings.Builder
		for {
			output, code, done, err := shell.Next()
			if err != nil {
				t.Fatalf("the shell broke: %v, after %q", err, shown.String())
			}
			if done {
				return shown.String(), code
			}
			shown.Write(output)
		}
	}

	t.Run("it runs what the user types, in the container, and gives the exit code back", func(t *testing.T) {
		shown, code := session(t, "echo sum-$((40+2)) in $(hostname)\nexit 3\n")

		if !strings.Contains(shown, "sum-42 in ") || code != 3 {
			t.Fatalf("code %d, the terminal showed %q", code, shown)
		}
	})

	t.Run("it has a terminal of the size of the user", func(t *testing.T) {
		shown, _ := session(t, "stty size\nexit\n")

		if !strings.Contains(shown, "24 80") {
			t.Fatalf("the terminal showed %q, want the size 24 80", shown)
		}
	})

	t.Run("a client that goes away leaves no shell behind", func(t *testing.T) {
		shell, err := protocol.Client{URL: api.URL}.Shell("shop", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		shell.Write([]byte("sleep 987\n"))
		shell.Next() // the echo of the keys: the command runs

		shell.Close()

		for range 50 {
			if out, _ := exec.Command("docker", "exec", container, "ps").Output(); !strings.Contains(string(out), "sleep 987") {
				return
			}
			exec.Command("sleep", "0.1").Run()
		}
		t.Fatal("the command of the shell still runs in the container")
	})
}
