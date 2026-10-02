package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// releaseServer serves a release: one program file and its checksums.
func releaseServer(t *testing.T, program, sum string) *httptest.Server {
	asset := "chasen-" + runtime.GOOS + "-" + runtime.GOARCH
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n%s  chasen-other-os\n", sum, asset, checksum([]byte("other")))
		case "/" + asset:
			fmt.Fprint(w, program)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestUpdate(t *testing.T) {
	installed := func(t *testing.T, content string) string {
		self := filepath.Join(t.TempDir(), "chasen")
		if err := os.WriteFile(self, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
		return self
	}
	content := func(t *testing.T, file string) string {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Run("replaces the program with the release, and keeps it executable", func(t *testing.T) {
		self := installed(t, "old program")
		server := releaseServer(t, "new program", checksum([]byte("new program")))

		changed, err := updateFile(self, server.URL)

		if err != nil || !changed || content(t, self) != "new program" {
			t.Fatalf("updateFile = %v, %v, and the file is %q", changed, err, content(t, self))
		}
		if info, _ := os.Stat(self); info.Mode().Perm()&0100 == 0 {
			t.Errorf("the new program is not executable: %v", info.Mode())
		}
	})

	t.Run("changes nothing when the checksum of the download is wrong", func(t *testing.T) {
		self := installed(t, "old program")
		server := releaseServer(t, "a program that someone changed", checksum([]byte("new program")))

		changed, err := updateFile(self, server.URL)

		if err == nil || changed || content(t, self) != "old program" {
			t.Fatalf("updateFile = %v, %v, and the file is %q", changed, err, content(t, self))
		}
		if _, err := os.Stat(self + ".new"); err == nil {
			t.Error("the download was left next to the program")
		}
	})

	t.Run("does nothing when the program is the release already", func(t *testing.T) {
		self := installed(t, "new program")
		server := releaseServer(t, "new program", checksum([]byte("new program")))

		changed, err := updateFile(self, server.URL)

		if err != nil || changed {
			t.Fatalf("updateFile = %v, %v", changed, err)
		}
	})
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.4.0", "v0.3.2", true},
		{"v0.3.10", "v0.3.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.3.2", "v0.3.2", false},
		{"v0.3.1", "v0.3.2", false},
		{"v0.4.0", "dev", false},
		{"", "v0.3.2", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNewerRelease(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	before := version
	version = "v0.3.2"
	t.Cleanup(func() { version = before })
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		fmt.Fprint(w, `{"tag_name": "v0.4.0"}`)
	}))
	t.Cleanup(server.Close)

	first := newerReleaseFrom(server.URL)
	second := newerReleaseFrom(server.URL)

	if first != "v0.4.0" || second != "v0.4.0" {
		t.Errorf("newerReleaseFrom = %q, then %q, want v0.4.0 both times", first, second)
	}
	if asked != 1 {
		t.Errorf("asked the server %d times in one day, want 1", asked)
	}
}
