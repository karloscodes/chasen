package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRootDoesNotFollowTheLinksOfAnApp(t *testing.T) {
	// server makes the database of the server and the storage of an app.
	server := func(t *testing.T) (serverDB, storage string) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		db, err := openServerDB()
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
		storage = filepath.Join(dataDir("shop"))
		if err := os.MkdirAll(storage, 0755); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(filepath.Dir(configPath()), "server.sqlite3"), storage
	}

	t.Run("a path through a link that the app made is not linkFree", func(t *testing.T) {
		serverDB, storage := server(t)
		os.Symlink(filepath.Dir(serverDB), filepath.Join(storage, "sub"))
		query(t, filepath.Join(storage, "db.sqlite3"), "CREATE TABLE orders (name); SELECT 1")

		through := linkFree(appDir("shop"), filepath.Join(storage, "sub", "server.sqlite3"))
		own := linkFree(appDir("shop"), filepath.Join(storage, "db.sqlite3"))

		if through || !own {
			t.Errorf("linkFree through the link = %v, of the app's own file = %v; want false, true", through, own)
		}
		if parentLinkFree(appDir("shop"), filepath.Join(storage, "sub", "new", "db.sqlite3")) {
			t.Error("parentLinkFree accepts a directory under a link")
		}
	})

	t.Run("the search for databases skips a link to the server database", func(t *testing.T) {
		serverDB, storage := server(t)
		os.Symlink(serverDB, filepath.Join(storage, "db.sqlite3"))
		os.Symlink(filepath.Dir(serverDB), filepath.Join(storage, "sub"))

		dbs, err := findDatabases(appDir("shop"), nil, "")

		if err != nil || len(dbs) != 0 {
			t.Errorf("found %v, %v; want nothing", dbs, err)
		}
	})

	t.Run("a pipe in the storage does not stop the search", func(t *testing.T) {
		_, storage := server(t)
		if err := syscall.Mkfifo(filepath.Join(storage, "pipe.sqlite3"), 0644); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)

		go func() { _, err := findDatabases(appDir("shop"), nil, ""); done <- err }()

		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the search waits on the pipe")
		}
	})

	t.Run("a restore refuses a backup that holds the server database", func(t *testing.T) {
		serverDB, storage := server(t)
		// As if the app made root copy it into its backup.
		data, err := os.ReadFile(serverDB)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(storage, "db.sqlite3"), data, 0644)
		stamp, err := backupApp("shop", serverConfig{})
		if err != nil {
			t.Fatal(err)
		}

		_, err = stageBackup("shop", stamp)

		if err == nil || !strings.Contains(err.Error(), "holds the database of the server") {
			t.Errorf("stageBackup = %v, want a refusal", err)
		}
	})
}
