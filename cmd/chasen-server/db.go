package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// The database of the server. It holds all the state of the server: its
// settings (the domain, the token, the bucket), the apps, the settings that
// the client sent for each app, the logins of `chasen login`, and the
// activity feed. The server has no config file.
const serverSchema = `
CREATE TABLE IF NOT EXISTS apps (
	name           TEXT PRIMARY KEY,
	image          TEXT NOT NULL,
	domain         TEXT NOT NULL,
	port           INTEGER NOT NULL,
	health_path    TEXT NOT NULL,
	health_timeout INTEGER NOT NULL,
	volumes        TEXT NOT NULL,
	env            TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS imported_files (
	file     TEXT PRIMARY KEY,
	modified TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_settings (
	app      TEXT PRIMARY KEY,
	settings TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS logins (
	token_sha256 TEXT PRIMARY KEY,
	created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS activity (
	id          INTEGER PRIMARY KEY,
	app         TEXT NOT NULL,
	action      TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
	started_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	finished_at TEXT,
	output      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS activity_app ON activity (app, id);`

func openServerDB() (*sql.DB, error) {
	path := root() + "/etc/chasen/server.sqlite3"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sqliteDB(path)
	if err != nil {
		return nil, err
	}
	// Only root reads the database: it holds the token, the keys of the bucket,
	// the secrets of the apps, the logins, and the output of deploys.
	os.Chmod(path, 0600)
	if _, err := db.Exec("PRAGMA journal_mode = WAL;" + serverSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := importApps(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// The feed keeps this many entries, and this much output of each one.
const (
	activityKept      = 500
	activityOutputMax = 256 << 10
)

// recordedAction returns the feed entry for an API command, or "" for a
// command that changes nothing.
func recordedAction(command string, args []string) string {
	switch {
	case command == "deploy" || command == "enable" || command == "restart" || command == "restore" || command == "remove":
		return strings.TrimSpace(command + " " + strings.Join(args, " "))
	case command == "domains" && len(args) > 0:
		return "domains " + strings.Join(args, " ")
	case command == "run" && len(args) > 0:
		// A command in the container can change the data: the feed has it.
		return "run " + strings.Join(args, " ")
	}
	return ""
}

func startActivity(db *sql.DB, app, action string) (int64, error) {
	result, err := db.Exec("INSERT INTO activity (app, action, status) VALUES (?, ?, 'running')", app, action)
	if err != nil {
		return 0, err
	}
	db.Exec("DELETE FROM activity WHERE id <= (SELECT max(id) FROM activity) - ?", activityKept)
	return result.LastInsertId()
}

// saveActivity keeps the output so far of an entry that still runs.
func saveActivity(db *sql.DB, id int64, output string) {
	db.Exec("UPDATE activity SET output = ? WHERE id = ? AND status = 'running'", output, id)
}

func finishActivity(db *sql.DB, id int64, succeeded bool, output string) {
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	db.Exec("UPDATE activity SET status = ?, finished_at = CURRENT_TIMESTAMP, output = ? WHERE id = ?", status, output, id)
}

// serverHistory shows the feed of an app, or the output of one entry.
func serverHistory(name string, args []string) error {
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if len(args) == 1 {
		var output string
		err := db.QueryRow("SELECT output FROM activity WHERE app = ? AND id = ?", name, args[0]).Scan(&output)
		if err != nil {
			return fmt.Errorf("no entry %s for %s", args[0], name)
		}
		fmt.Print(output)
		return nil
	}

	rows, err := db.Query("SELECT id, started_at, action, status FROM activity WHERE app = ? ORDER BY id DESC LIMIT 20", name)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN (UTC)\tACTION\tRESULT")
	for rows.Next() {
		var id int64
		var when, action, status string
		if err := rows.Scan(&id, &when, &action, &status); err != nil {
			return err
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", id, when, action, status)
	}
	return w.Flush()
}
