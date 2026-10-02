package main

import (
	"archive/tar"
	"compress/gzip"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// mockServer starts the mock server and returns a login for it.
func mockServer(t *testing.T) credentials {
	t.Helper()
	server := mock.New(time.Now())
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	return credentials{URL: web.URL, Token: server.Token}
}

func TestDownloadBackup(t *testing.T) {
	t.Run("the newest backup becomes a file in this directory, named after the app and the backup", func(t *testing.T) {
		creds := mockServer(t)
		t.Chdir(t.TempDir())

		if err := downloadBackup(creds, "shop", ""); err != nil {
			t.Fatal(err)
		}

		files, _ := filepath.Glob("shop-*.tar.gz")
		if len(files) != 1 {
			t.Fatalf("got the files %v, want one shop-<backup>.tar.gz", files)
		}
		file, _ := os.Open(files[0])
		defer file.Close()
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		if entry, err := tar.NewReader(unzipped).Next(); err != nil || !strings.HasSuffix(entry.Name, ".sqlite3") {
			t.Fatalf("the archive has %v (%v), want a database", entry, err)
		}
	})

	t.Run("a file from before stays as it is", func(t *testing.T) {
		creds := mockServer(t)
		t.Chdir(t.TempDir())
		if err := downloadBackup(creds, "shop", ""); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob("shop-*.tar.gz")
		os.WriteFile(files[0], []byte("mine"), 0600)

		err := downloadBackup(creds, "shop", "")

		if err == nil || !strings.Contains(err.Error(), "exists already") {
			t.Fatalf("got %v, want a refusal", err)
		}
		if kept, _ := os.ReadFile(files[0]); string(kept) != "mine" {
			t.Fatal("the download replaced the file")
		}
	})

	t.Run("a backup that does not exist saves nothing and says where the list is", func(t *testing.T) {
		creds := mockServer(t)
		t.Chdir(t.TempDir())

		err := downloadBackup(creds, "shop", "19990101T000000Z")

		if err == nil || !strings.Contains(err.Error(), "chasen backups") {
			t.Fatalf("got %v", err)
		}
		if files, _ := filepath.Glob("*"); len(files) != 0 {
			t.Fatalf("the directory has %v", files)
		}
	})
}

func TestShellOverTheProtocol(t *testing.T) {
	// read collects what the terminal shows until it shows want, or the shell ends.
	read := func(t *testing.T, shell *protocol.Shell, want string) (shown string, code int, done bool) {
		t.Helper()
		for !strings.Contains(shown, want) {
			output, code, done, err := shell.Next()
			if err != nil {
				t.Fatalf("the shell broke: %v, after %q", err, shown)
			}
			if done {
				return shown, code, true
			}
			shown += string(output)
		}
		return shown, 0, false
	}

	t.Run("the keys go to the server, and its output and the exit code come back", func(t *testing.T) {
		creds := mockServer(t)
		shell, err := protocol.Client(creds).Shell("shop", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer shell.Close()

		shell.Write([]byte("hello\r"))
		shown, _, _ := read(t, shell, "you typed: hello")
		shell.Resize(120, 40)
		shell.Write([]byte("exit 3\r"))
		_, code, done := read(t, shell, "never shown")

		if !strings.Contains(shown, "you typed: hello") || !done || code != 3 {
			t.Fatalf("the terminal showed %q, done %v, code %d", shown, done, code)
		}
	})

	t.Run("an app that is not deployed gives its reason, not a broken connection", func(t *testing.T) {
		creds := mockServer(t)
		shell, err := protocol.Client(creds).Shell("nothing", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer shell.Close()

		_, _, _, err = shell.Next()

		if err == nil || !strings.Contains(err.Error(), "not deployed") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a wrong token gets no shell", func(t *testing.T) {
		creds := mockServer(t)
		creds.Token = "wrong"

		_, err := protocol.Client(creds).Shell("shop", 80, 24)

		if err == nil || !strings.Contains(err.Error(), "refused the shell") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSecretsFromACommand(t *testing.T) {
	t.Run("a value in double quotes loses the quotes and the escapes of the tool that printed it", func(t *testing.T) {
		// What `fnox export` prints for the value: a "quote", a \ and = sign
		env := parseDotenv(`KEY="a \"quote\", a \\ and = sign"` + "\nPLAIN=abc123\nexport OTHER='single \\n stays'\n# a comment\n")

		if env["KEY"] != `a "quote", a \ and = sign` {
			t.Errorf("KEY is %q", env["KEY"])
		}
		if env["PLAIN"] != "abc123" || env["OTHER"] != `single \n stays` {
			t.Errorf("got %q", env)
		}
	})
}
