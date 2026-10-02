package main

import (
	"database/sql"
	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// query runs the statements and returns the value of the last one.
func query(t *testing.T, path, statements string) string {
	t.Helper()
	db, err := sqliteDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list := strings.Split(statements, ";")
	for _, statement := range list[:len(list)-1] {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var value sql.NullString
	if err := db.QueryRow(list[len(list)-1]).Scan(&value); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return value.String
}

func timeStamp(year, day, hour int) string {
	return time.Date(year, 1, day, hour, 0, 0, 0, time.UTC).Format(stampLayout)
}

func TestBackup(t *testing.T) {
	t.Run("restore brings back every database as it was at the backup", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		main := filepath.Join(dataDir("shop"), "db.sqlite3")
		queue := filepath.Join(dataDir("shop"), "storage", "queue.db")
		os.MkdirAll(filepath.Dir(queue), 0755)
		query(t, main, "PRAGMA journal_mode=WAL; CREATE TABLE orders (name); INSERT INTO orders VALUES ('before')")
		query(t, queue, "CREATE TABLE jobs (name); INSERT INTO jobs VALUES ('before')")
		os.WriteFile(filepath.Join(dataDir("shop"), "upload.txt"), []byte("not a database"), 0644)

		stamp, err := backupApp("shop", serverConfig{})
		if err != nil {
			t.Fatal(err)
		}
		query(t, main, "INSERT INTO orders VALUES ('after')")
		query(t, queue, "DELETE FROM jobs")
		staged, err := stageBackup("shop", stamp)
		if err != nil {
			t.Fatal(err)
		}
		aside, err := swap("shop", staged)

		if err != nil {
			t.Fatal(err)
		}
		if got := query(t, main, "SELECT group_concat(name) FROM orders"); got != "before" {
			t.Errorf("orders after restore = %q, want %q", got, "before")
		}
		if got := query(t, queue, "SELECT group_concat(name) FROM jobs"); got != "before" {
			t.Errorf("jobs after restore = %q, want %q", got, "before")
		}
		if got := query(t, filepath.Join(aside, "storage", "db.sqlite3"), "SELECT group_concat(name) FROM orders"); got != "before,after" {
			t.Errorf("orders kept aside = %q, want %q", got, "before,after")
		}
	})

	t.Run("a corrupt backup leaves the data as it is", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		db := filepath.Join(dataDir("shop"), "db.sqlite3")
		os.MkdirAll(dataDir("shop"), 0755)
		query(t, db, "CREATE TABLE orders (name); INSERT INTO orders VALUES ('live')")
		stamp, err := backupApp("shop", serverConfig{})
		if err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(backupsDir("shop"), stamp, "storage", "db.sqlite3.gz")
		os.WriteFile(backup, []byte("garbage"), 0600)

		_, err = stageBackup("shop", stamp)

		if err == nil {
			t.Error("restore of a corrupt backup returned no error")
		}
		if got := query(t, db, "SELECT name FROM orders"); got != "live" {
			t.Errorf("orders = %q, want %q", got, "live")
		}
	})

	t.Run("an app without a database gets no backup", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		stamp, err := backupApp("shop", serverConfig{})

		if stamp != "" || err != nil {
			t.Errorf("backupApp = %q, %v, want no backup and no error", stamp, err)
		}
	})
}

func TestRetention(t *testing.T) {
	stamps := []string{
		"20261001T125900Z", // newest
		"20261001T120000Z", // same hour as the newest
		"20261001T100000Z", // another hour of the same day
		"20260930T230000Z", // yesterday
		"20260930T010000Z", // yesterday, another hour
		"20260801T000000Z", // two months ago
		"20250101T000000Z", // last year
		"manual-copy",      // not a chasen backup
	}

	drop := expired(stamps)

	want := []string{"20261001T120000Z"}
	if !slices.Equal(drop, want) {
		t.Errorf("expired = %v, want %v", drop, want)
	}
}

func TestRetentionOfHourlyBackups(t *testing.T) {
	// One backup each hour for 60 days, as cron makes them.
	var stamps []string
	for day := 1; day <= 60; day++ {
		for hour := 0; hour < 24; hour++ {
			stamps = append(stamps, timeStamp(2026, day, hour))
		}
	}

	kept := len(stamps) - len(expired(stamps))

	// 24 hourly + 6 more daily + 4 weekly and 2 monthly not already kept: the count stays small and fixed.
	if kept < 30 || kept > 41 {
		t.Errorf("kept %d of %d backups, want 30 to 41", kept, len(stamps))
	}
	if slices.Contains(expired(stamps), timeStamp(2026, 60, 23)) {
		t.Error("the newest backup expired")
	}
}

func TestAppWithNoBackup(t *testing.T) {
	// server makes a server with two apps that have a database each: a shop,
	// and a demo that says backup: false.
	server := func(t *testing.T) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "token"}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"shop", "demo"} {
			saveApp(name, matcha.AppConfig{Image: "chasen.invalid/" + name + ":1", Domain: name + ".example.com"})
			os.MkdirAll(dataDir(name), 0755)
			query(t, filepath.Join(dataDir(name), "db.sqlite3"), "CREATE TABLE rows (name); INSERT INTO rows VALUES ('one')")
		}
		if err := saveSettings("demo", protocol.Settings{NoBackup: true}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the hourly backup leaves it out, and backs up the others", func(t *testing.T) {
		server(t)

		err := backupAll()

		if err != nil {
			t.Fatal(err)
		}
		if shop, demo := localBackups("shop"), localBackups("demo"); len(shop) != 1 || len(demo) != 0 {
			t.Errorf("backups: shop %v, demo %v, want one of the shop and none of the demo", shop, demo)
		}
	})

	t.Run("a backup that the owner asks for is still made", func(t *testing.T) {
		server(t)

		_, err := backupApp("demo", serverConfig{})

		if err != nil || len(localBackups("demo")) != 1 {
			t.Errorf("err = %v, backups %v, want the backup that was asked for", err, localBackups("demo"))
		}
	})
}
