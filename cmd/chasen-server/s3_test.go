package main

import (
	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
	"os"
	"path/filepath"
	"testing"
)

// Run an S3-compatible store first, for example:
//
//	docker run -d -p 127.0.0.1:19000:9000 -e RUSTFS_ACCESS_KEY=minioadmin -e RUSTFS_SECRET_KEY=minioadmin rustfs/rustfs
//	CHASEN_TEST_S3=http://127.0.0.1:19000 go test -run TestOffsite ./cmd/chasen-server
func TestOffsiteBackup(t *testing.T) {
	endpoint := os.Getenv("CHASEN_TEST_S3")
	if endpoint == "" {
		t.Skip("set CHASEN_TEST_S3 to the endpoint of an S3 store with the minioadmin credentials")
	}
	s3 := &s3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: "chasen-test", AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin"}
	if err := s3.createBucket(); err != nil {
		t.Fatal(err)
	}
	var cfg serverConfig
	cfg.Backup.S3 = s3

	t.Run("a new server restores the newest offsite backup", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		db := filepath.Join(dataDir("shop"), "my shop's db.sqlite3")
		os.MkdirAll(dataDir("shop"), 0755)
		query(t, db, "CREATE TABLE orders (name); INSERT INTO orders VALUES ('offsite')")
		if _, err := backupApp("shop", cfg); err != nil {
			t.Fatal(err)
		}
		// The server is lost: no data and no local backups.
		t.Setenv("CHASEN_ROOT", t.TempDir())
		os.MkdirAll(dataDir("shop"), 0755)

		stamp, err := fetchBackup("shop", "", cfg)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := stageBackup("shop", stamp)
		if err != nil {
			t.Fatal(err)
		}
		_, err = swap("shop", staged)

		if err != nil {
			t.Fatal(err)
		}
		db = filepath.Join(dataDir("shop"), "my shop's db.sqlite3")
		if got := query(t, db, "SELECT name FROM orders"); got != "offsite" {
			t.Errorf("orders after restore = %q, want %q", got, "offsite")
		}
	})

	t.Run("retention deletes expired offsite backups and keeps the others", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		file := filepath.Join(t.TempDir(), "db.gz")
		os.WriteFile(file, []byte("x"), 0600)
		// Two backups in one hour, more than a day before the newest: the
		// newest of that hour stays.
		for _, stamp := range []string{"20260101T100000Z", "20260101T103000Z", "20260102T120000Z"} {
			if err := s3.putFile("prune/snapshots/"+stamp+"/db.sqlite3.gz", file); err != nil {
				t.Fatal(err)
			}
		}

		err := prune("prune", cfg)

		if err != nil {
			t.Fatal(err)
		}
		left, _ := s3.list("prune/snapshots/")
		if len(left) != 2 || left[0] != "prune/snapshots/20260101T103000Z/db.sqlite3.gz" || left[1] != "prune/snapshots/20260102T120000Z/db.sqlite3.gz" {
			t.Errorf("offsite backups after prune = %v, want the 10:30 backup and the newest", left)
		}
	})

	t.Run("an app that turned its backups off loses its live replica in the bucket, and keeps its snapshots", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(cfg); err != nil {
			t.Fatal(err)
		}
		saveApp("demo", matcha.AppConfig{Image: "chasen.invalid/demo:1", Domain: "demo.example.com"})
		saveSettings("demo", protocol.Settings{NoBackup: true})
		file := filepath.Join(t.TempDir(), "file")
		os.WriteFile(file, []byte("x"), 0600)
		for _, key := range []string{"demo/live/storage/db.sqlite3/0001/a.ltx", "demo/live/storage/db.sqlite3/0009/b.ltx", "demo/snapshots/20260101T100000Z/db.sqlite3.gz"} {
			if err := s3.putFile(key, file); err != nil {
				t.Fatal(err)
			}
		}

		backupAll() // it reports that no replica runs in this test: the cleanup is what counts here

		live, _ := s3.list("demo/live/")
		snapshots, _ := s3.list("demo/snapshots/")
		if len(live) != 0 || len(snapshots) != 1 {
			t.Errorf("after the hourly run: replica files %v, snapshots %v, want no replica and the snapshot", live, snapshots)
		}
	})
}
