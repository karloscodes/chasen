package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/matcha"
)

func TestReplicaGap(t *testing.T) {
	now := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	replica := func(ages ...time.Duration) []s3Object {
		var objects []s3Object
		for _, age := range ages {
			objects = append(objects, s3Object{Key: "shop/live/db/0000/a.ltx", LastModified: now.Add(-age)})
		}
		return objects
	}
	cases := []struct {
		name    string
		changed time.Duration // how long ago the database changed
		replica []s3Object
		want    time.Duration
	}{
		{"a replica that got the last change has no gap", 30 * time.Second, replica(2*time.Hour, 29*time.Second), 0},
		{"a database that did not change for hours has no gap", 5 * time.Hour, replica(5 * time.Hour), 0},
		{"a replica that stopped an hour ago is behind by the changes since", time.Minute, replica(2*time.Hour, time.Hour), 59 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := replicaGap(now.Add(-c.changed), c.replica)

			if got != c.want {
				t.Errorf("gap = %s, want %s", got, c.want)
			}
		})
	}

	t.Run("a database with no replica at all is behind since its first day", func(t *testing.T) {
		got := replicaGap(now, nil)

		if got < replicaMayBeBehind {
			t.Errorf("gap = %s, want more than the limit of %s", got, replicaMayBeBehind)
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
