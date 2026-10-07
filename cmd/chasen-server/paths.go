package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// An app writes its own volumes, as the user of its image. Root reads the
// databases there for the backups and the live replica, and writes there for a
// restore. Between two looks, the app can turn any part of a path into a link:
// to /etc/chasen/server.sqlite3, which holds the token of the server, or to the
// data of another app. So root checks a path right before each use, and a copy
// checks it again after.

// linkFree reports whether path is inside dir and resolves to itself: no part
// of it below dir is a link.
func linkFree(dir, path string) bool {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	return err == nil && real == filepath.Join(realDir, rel)
}

// parentLinkFree reports whether the directory that would hold path is safe
// to create: its nearest part that exists is inside dir and is no link.
func parentLinkFree(dir, path string) bool {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		if parent == dir {
			return true
		}
		if _, err := os.Lstat(parent); err == nil {
			return linkFree(dir, parent)
		} else if !errors.Is(err, fs.ErrNotExist) || !strings.HasPrefix(parent, dir+"/") {
			return false
		}
	}
}

// isServerDatabase reports a SQLite file that has the tables of the database
// of the server. Such a file never belongs in the storage of an app: when a
// backup of an app holds one, the app made root copy it.
func isServerDatabase(path string) bool {
	var n int
	err := sqliteQueryRow(path, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'logins' AND sql LIKE '%token_sha256%'", &n)
	return err == nil && n > 0
}

func sqliteQueryRow(path, query string, dest ...any) error {
	db, err := sqliteDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.QueryRow(query).Scan(dest...)
}
