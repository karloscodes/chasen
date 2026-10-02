package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownload(t *testing.T) {
	// backup makes one backup of the app shop on a server in a temporary root,
	// with one database in its volume.
	backup := func(t *testing.T, stamp, content string) {
		t.Helper()
		dir := filepath.Join(backupsDir("shop"), stamp, "storage")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		plain := filepath.Join(t.TempDir(), "db.sqlite3")
		os.WriteFile(plain, []byte(content), 0600)
		if err := gzipFile(plain, filepath.Join(dir, "db.sqlite3.gz")); err != nil {
			t.Fatal(err)
		}
	}
	download := func(query string) *httptest.ResponseRecorder {
		db, err := openServerDB()
		if err != nil {
			panic(err)
		}
		defer db.Close()
		answer := httptest.NewRecorder()
		serveDownload(answer, httptest.NewRequest("GET", "/v1/download?"+query, nil), db)
		return answer
	}
	server := func(t *testing.T) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "secret"}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the newest backup comes as a tar.gz file with the databases ready to open", func(t *testing.T) {
		server(t)
		backup(t, "20261001T120000Z", "the older database")
		backup(t, "20261002T120000Z", "the newer database")

		answer := download("arg=shop")

		if answer.Code != http.StatusOK || !strings.Contains(answer.Header().Get("Content-Disposition"), `filename="shop-20261002T120000Z.tar.gz"`) {
			t.Fatalf("got %d with the name %q", answer.Code, answer.Header().Get("Content-Disposition"))
		}
		unzipped, err := gzip.NewReader(answer.Body)
		if err != nil {
			t.Fatal(err)
		}
		archive := tar.NewReader(unzipped)
		file, err := archive.Next()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(archive)
		if file.Name != "storage/db.sqlite3" || string(content) != "the newer database" {
			t.Fatalf("got the file %q with %q, want storage/db.sqlite3 with the newer database, not compressed", file.Name, content)
		}
	})

	t.Run("the history says that the data left the server", func(t *testing.T) {
		server(t)
		backup(t, "20261002T120000Z", "the database")

		download("arg=shop")

		got := query(t, root()+"/etc/chasen/server.sqlite3", "SELECT action || ' ' || status FROM activity WHERE app = 'shop'")
		if got != "download 20261002T120000Z succeeded" {
			t.Fatalf("the history has %q", got)
		}
	})

	t.Run("a backup by its name", func(t *testing.T) {
		server(t)
		backup(t, "20261001T120000Z", "the older database")
		backup(t, "20261002T120000Z", "the newer database")

		answer := download("arg=shop&arg=20261001T120000Z")

		if answer.Code != http.StatusOK || !strings.Contains(answer.Header().Get("Content-Disposition"), "shop-20261001T120000Z") {
			t.Fatalf("got %d with the name %q", answer.Code, answer.Header().Get("Content-Disposition"))
		}
	})

	t.Run("an app with no backup says how to make one", func(t *testing.T) {
		server(t)

		answer := download("arg=shop")

		if answer.Code != http.StatusNotFound || !strings.Contains(answer.Body.String(), "chasen backup") {
			t.Fatalf("got %d %q", answer.Code, answer.Body.String())
		}
	})

	t.Run("a name that is not an app is refused", func(t *testing.T) {
		server(t)

		answer := download("arg=../../etc")

		if answer.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400", answer.Code)
		}
	})
}
