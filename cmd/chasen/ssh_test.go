package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
)

// TestRelayHelper is not a test. The `ssh` of the tests below runs this test
// binary with CHASEN_TEST_RELAY set, and then it does what `ssh <server>
// chasen-server connect` does: it joins its input and its output to the API.
func TestRelayHelper(t *testing.T) {
	address := os.Getenv("CHASEN_TEST_RELAY")
	if address == "" {
		return
	}
	api, err := net.Dial("tcp", address)
	if err != nil {
		os.Exit(255)
	}
	go func() {
		io.Copy(api, os.Stdin)
		api.(*net.TCPConn).CloseWrite()
	}()
	io.Copy(os.Stdout, api)
	os.Exit(0)
}

// sshServer starts the mock server and puts an `ssh` first on the PATH that
// leads to it. It returns the login of the server through SSH, and the file
// where that `ssh` writes its arguments.
func sshServer(t *testing.T) (creds credentials, calls string) {
	t.Helper()
	web := mockServer(t)
	api, _ := url.Parse(web.URL)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"chasen-server token"*) echo %s ;;
  *"chasen-server connect"*) CHASEN_TEST_RELAY=%s exec %q -test.run='^TestRelayHelper$' ;;
  *) echo "ssh: no such command" >&2; exit 255 ;;
esac
`, calls, web.Token, api.Host, self)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Each server of a test has its own address, so no test uses the connection of another.
	return credentials{URL: "ssh://deploy@" + strings.ReplaceAll(api.Host, ":", "-") + ".example.com:2222", Token: web.Token}, calls
}

func TestServerThroughSSH(t *testing.T) {
	t.Run("a command goes through ssh to the API, and its output comes back", func(t *testing.T) {
		creds, calls := sshServer(t)
		var out strings.Builder

		err := remote(creds, nil, &out, "list")

		if err != nil || !strings.Contains(out.String(), "shop") {
			t.Fatalf("list = %q (%v), want the apps of the server", out.String(), err)
		}
		host := strings.TrimPrefix(strings.TrimSuffix(creds.URL, ":2222"), "ssh://")
		if ran, _ := os.ReadFile(calls); !strings.HasPrefix(string(ran), "-T -p 2222 "+host+" ") || !strings.Contains(string(ran), "chasen-server connect") {
			t.Errorf("ssh ran with %q, want the port, the user, the host, and the connect command", ran)
		}
	})

	t.Run("the commands of one process use one connection", func(t *testing.T) {
		creds, calls := sshServer(t)

		remote(creds, nil, io.Discard, "list")
		err := remote(creds, nil, io.Discard, "load")

		if ran, _ := os.ReadFile(calls); err != nil || strings.Count(string(ran), "\n") != 1 {
			t.Errorf("ssh ran %d times (%v), want one time for two commands", strings.Count(string(ran), "\n"), err)
		}
	})

	t.Run("a backup downloads through ssh", func(t *testing.T) {
		creds, _ := sshServer(t)
		t.Chdir(t.TempDir())

		err := downloadBackup(creds, "shop", "")

		if files, _ := filepath.Glob("shop-*.tar.gz"); err != nil || len(files) != 1 {
			t.Fatalf("got the files %v (%v), want one shop-<backup>.tar.gz", files, err)
		}
	})

	t.Run("the shell in the container works through ssh", func(t *testing.T) {
		creds, _ := sshServer(t)
		shell, err := protocol.Client(creds).Shell("shop", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer shell.Close()

		shell.Write([]byte("hello\rexit 3\r"))

		var shown string
		for {
			output, code, done, err := shell.Next()
			if err != nil {
				t.Fatalf("the shell broke: %v, after %q", err, shown)
			}
			if done {
				if !strings.Contains(shown, "you typed: hello") || code != 3 {
					t.Errorf("the terminal showed %q, code %d", shown, code)
				}
				return
			}
			shown += string(output)
		}
	})

	t.Run("a wrong token gets the same refusal as on the web", func(t *testing.T) {
		creds, _ := sshServer(t)
		creds.Token = "wrong"

		err := remote(creds, nil, io.Discard, "list")

		if err != errUnauthorized {
			t.Errorf("got %v, want the refusal of the token", err)
		}
	})

	t.Run("an ssh that cannot log in says that the way is SSH", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0755)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

		err := remote(credentials{URL: "ssh://root@refused.example.com", Token: "x"}, nil, io.Discard, "list")

		if err == nil || !strings.Contains(err.Error(), "cannot reach the server through SSH (ssh://root@refused.example.com)") {
			t.Errorf("got %v", err)
		}
	})
}

func TestAddServerThroughSSH(t *testing.T) {
	t.Run("the server gives its token to who can log in with SSH", func(t *testing.T) {
		creds, calls := sshServer(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("CHASEN_TOKEN", "")
		t.Setenv("CHASEN_URL", "")

		err := connect(creds.URL)

		if err != nil {
			t.Fatal(err)
		}
		saved := loadLogins()
		if saved.Current != creds.URL || saved.Tokens[creds.URL] != creds.Token {
			t.Errorf("saved %+v, want the login of %s with the token of the server", saved, creds.URL)
		}
		if ran, _ := os.ReadFile(calls); !strings.Contains(string(ran), "chasen-server token") {
			t.Errorf("ssh ran with %q, want the token command", ran)
		}
	})

	t.Run("what a person types becomes the address", func(t *testing.T) {
		for typed, want := range map[string]string{
			"root@203.0.113.5":            "ssh://root@203.0.113.5",
			"deploy@server.example.com":   "ssh://deploy@server.example.com",
			"ssh://root@203.0.113.5:2222": "ssh://root@203.0.113.5:2222",
			"example.com":                 "https://api.example.com",
		} {
			if got := apiAddress(typed); got != want {
				t.Errorf("apiAddress(%q) = %q, want %q", typed, got, want)
			}
		}
	})

	t.Run("an address that could be an option of ssh is refused", func(t *testing.T) {
		_, err := protocol.SSHCommand("ssh://-oProxyCommand=evil@host", "chasen-server token")

		if err == nil {
			t.Error("want an error")
		}
	})
}
