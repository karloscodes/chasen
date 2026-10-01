package main

import (
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
		for _, stamp := range []string{"20260101T100000Z", "20260101T103000Z"} {
			if err := s3.putFile("prune/snapshots/"+stamp+"/db.sqlite3.gz", file); err != nil {
				t.Fatal(err)
			}
		}

		err := prune("prune", cfg)

		if err != nil {
			t.Fatal(err)
		}
		left, _ := s3.list("prune/snapshots/")
		if len(left) != 1 || left[0] != "prune/snapshots/20260101T103000Z/db.sqlite3.gz" {
			t.Errorf("offsite backups after prune = %v, want only the 10:30 backup", left)
		}
	})
}
