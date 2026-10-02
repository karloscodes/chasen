package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/matcha"
)

func TestReplicaBehind(t *testing.T) {
	now := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	shop := func() string { return filepath.Join(dataDir("shop"), "db.sqlite3") }

	t.Run("a replica that has every transaction is not behind", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 40, Replica: 40}}, now.Add(-time.Hour))

		behind, err := replicasBehind("", now)

		if err != nil || len(behind) != 0 {
			t.Errorf("behind = %v (%v), want nothing", behind, err)
		}
	})

	t.Run("a replica that misses transactions for a moment is not a failure", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 41, Replica: 40}}, now.Add(-time.Minute))

		behind, err := replicasBehind("", now)

		if err != nil || len(behind) != 0 {
			t.Errorf("behind = %v (%v), want nothing after one minute", behind, err)
		}
	})

	t.Run("a replica that stays behind is a failure, with the moment it started and how much it misses", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		// The daemon looks every few seconds. The replica stopped an hour ago, and the app writes on.
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 41, Replica: 40}}, now.Add(-time.Hour))
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 90, Replica: 40}}, now.Add(-time.Minute))

		behind, err := replicasBehind("", now)

		want := "the live replica of shop (storage/db.sqlite3) is behind since 18:00 UTC: 50 transactions are not in the bucket"
		if err != nil || len(behind) != 1 || behind[0] != want {
			t.Errorf("behind = %q (%v), want %q", behind, err, want)
		}
		if other, _ := replicasBehind("blog", now); len(other) != 0 {
			t.Errorf("the blog is behind too: %v. It has no replica that is behind", other)
		}
	})

	t.Run("a replica that caught up is in sync again", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 41, Replica: 40}}, now.Add(-time.Hour))
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 90, Replica: 90}}, now.Add(-time.Minute))

		behind, err := replicasBehind("", now)

		if err != nil || len(behind) != 0 {
			t.Errorf("behind = %v (%v), want nothing", behind, err)
		}
	})

	t.Run("a database that is gone leaves the state", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveReplicaState(map[string]replicaPosition{shop(): {Local: 41, Replica: 40}}, now.Add(-time.Hour))
		saveReplicaState(map[string]replicaPosition{}, now.Add(-time.Minute))

		behind, err := replicasBehind("", now)

		if err != nil || len(behind) != 0 {
			t.Errorf("behind = %v (%v), want nothing: the app has no replica any more", behind, err)
		}
	})
}

func TestVerify(t *testing.T) {
	// shop makes a server with one app that has a database and one snapshot.
	shop := func(t *testing.T) (db string) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "token"}); err != nil {
			t.Fatal(err)
		}
		saveApp("shop", matcha.AppConfig{Image: "chasen.invalid/shop:1", Domain: "shop.example.com"})
		db = filepath.Join(dataDir("shop"), "db.sqlite3")
		os.MkdirAll(dataDir("shop"), 0755)
		query(t, db, "CREATE TABLE orders (name); INSERT INTO orders VALUES ('one')")
		if _, err := backupApp("shop", serverConfig{}); err != nil {
			t.Fatal(err)
		}
		return db
	}
	// files returns every file in the directory of the app.
	files := func() string {
		var all []string
		filepath.WalkDir(appDir("shop"), func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				all = append(all, strings.TrimPrefix(path, appDir("shop")))
			}
			return nil
		})
		return strings.Join(all, "\n")
	}

	t.Run("a snapshot that restores passes, and nothing changes", func(t *testing.T) {
		db := shop(t)
		before := files()

		err := serverVerify("shop")

		if err != nil {
			t.Fatal(err)
		}
		if after := files(); after != before {
			t.Errorf("the files of the app changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if got := query(t, db, "SELECT name FROM orders"); got != "one" {
			t.Errorf("the live database has %q, want it as it was", got)
		}
	})

	t.Run("a snapshot that is damaged fails the check", func(t *testing.T) {
		shop(t)
		backup := filepath.Join(backupsDir("shop"), localBackups("shop")[0], "storage", "db.sqlite3.gz")
		os.WriteFile(backup, []byte("garbage"), 0600)

		err := serverVerify("shop")

		if err == nil || !strings.Contains(err.Error(), "1 check(s) failed") {
			t.Errorf("got %v, want a failed check", err)
		}
	})

	t.Run("an app that is not deployed has nothing to check", func(t *testing.T) {
		shop(t)

		err := serverVerify("nothing")

		if err == nil || !strings.Contains(err.Error(), "not deployed") {
			t.Errorf("got %v", err)
		}
	})
}
