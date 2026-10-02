package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// heldLocks returns the file locks that this process holds on the file, as
// Linux lists them.
func heldLocks(t *testing.T, path string) []string {
	t.Helper()
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile("/proc/locks")
	if err != nil {
		t.Skip("this system does not list its file locks")
	}
	var held []string
	for _, line := range strings.Split(string(list), "\n") {
		// 1: POSIX  ADVISORY  READ 201062 08:01:1604677 1073741826 1073742335
		f := strings.Fields(line)
		if len(f) == 8 && f[4] == fmt.Sprint(os.Getpid()) && strings.HasSuffix(f[5], fmt.Sprintf(":%d", stat.Ino)) {
			held = append(held, f[3]+" "+f[6]+"-"+f[7])
		}
	}
	slices.Sort(held)
	return held
}

// The live replica holds each database with file locks, and a process that
// closes a file loses every lock it holds on that file. The replica looks for
// new databases every few seconds, in the same process.
func TestFindDatabasesOfTheReplica(t *testing.T) {
	t.Run("keeps the locks of a database that the process has open", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "storage", "db.sqlite3")
		os.MkdirAll(filepath.Dir(path), 0755)
		db, err := sqliteDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE orders (name); INSERT INTO orders VALUES ('one')"); err != nil {
			t.Fatal(err)
		}
		// A read that stays open, as the replica keeps one.
		read, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer read.Rollback()
		var orders int
		if err := read.QueryRow("SELECT COUNT(1) FROM orders").Scan(&orders); err != nil {
			t.Fatal(err)
		}
		onDatabase, onIndex := heldLocks(t, path), heldLocks(t, path+"-shm")
		if len(onDatabase) == 0 || len(onIndex) == 0 {
			t.Fatalf("the open read holds no lock: database %v, index %v", onDatabase, onIndex)
		}

		dbs, err := findDatabasesBut(dir, map[string]bool{path: true})

		if err != nil || !slices.Equal(dbs, []string{"storage/db.sqlite3"}) {
			t.Fatalf("found %v, %v", dbs, err)
		}
		if now := heldLocks(t, path); !slices.Equal(now, onDatabase) {
			t.Errorf("locks on the database: %v before the search, %v after", onDatabase, now)
		}
		if now := heldLocks(t, path+"-shm"); !slices.Equal(now, onIndex) {
			t.Errorf("locks on the -shm file: %v before the search, %v after", onIndex, now)
		}
	})

	t.Run("finds a database that the process does not know yet", func(t *testing.T) {
		dir := t.TempDir()
		query(t, filepath.Join(dir, "db.sqlite3"), "PRAGMA journal_mode=WAL; CREATE TABLE orders (name); SELECT 1")
		query(t, filepath.Join(dir, "queue.db"), "CREATE TABLE jobs (name); SELECT 1")
		os.WriteFile(filepath.Join(dir, "upload.txt"), []byte("not a database"), 0644)

		dbs, err := findDatabasesBut(dir, map[string]bool{filepath.Join(dir, "db.sqlite3"): true})

		if err != nil || !slices.Equal(dbs, []string{"db.sqlite3", "queue.db"}) {
			t.Fatalf("found %v, %v", dbs, err)
		}
	})
}
