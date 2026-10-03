package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/karloscodes/matcha"
)

// The apps of the server are rows in its database. A row is the record that
// the engine deploys: the image, the domains, the port, the health check,
// the volumes, and the env.

func loadApp(name string) (matcha.AppConfig, error) {
	apps, err := loadApps("WHERE name = ?", name)
	if err != nil {
		return matcha.AppConfig{}, err
	}
	app, ok := apps[name]
	if !ok {
		return app, fmt.Errorf("app %q is not deployed", name)
	}
	return app, nil
}

// listApps returns every app of the server, by name.
func listApps() (map[string]matcha.AppConfig, error) { return loadApps("") }

func loadApps(where string, args ...any) (map[string]matcha.AppConfig, error) {
	db, err := openServerDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name, image, domain, port, health_path, health_timeout, volumes, env, memory FROM apps "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := map[string]matcha.AppConfig{}
	for rows.Next() {
		var name, volumes, env string
		var app matcha.AppConfig
		if err := rows.Scan(&name, &app.Image, &app.Domain, &app.Port, &app.HealthPath, &app.HealthTimeout, &volumes, &env, &app.Memory); err != nil {
			return nil, err
		}
		if err := errors.Join(json.Unmarshal([]byte(volumes), &app.Volumes), json.Unmarshal([]byte(env), &app.Env)); err != nil {
			return nil, fmt.Errorf("the record of %s: %w", name, err)
		}
		apps[name] = app
	}
	return apps, rows.Err()
}

func saveApp(name string, app matcha.AppConfig) error {
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	return insertApp(db, name, app)
}

// execer is a database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertApp(db execer, name string, app matcha.AppConfig) error {
	volumes, _ := json.Marshal(app.Volumes)
	env, _ := json.Marshal(app.Env)
	_, err := db.Exec(`INSERT OR REPLACE INTO apps (name, image, domain, port, health_path, health_timeout, volumes, env, memory)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, app.Image, app.Domain, app.Port, app.HealthPath, app.HealthTimeout, string(volumes), string(env), app.Memory)
	return err
}

func forgetApp(name string) error {
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("DELETE FROM apps WHERE name = ?", name)
	return err
}

// importApps copies the apps of a server from before, which kept them in
// apps.yml. The file stays as it is: an update that fails goes back to the
// previous version, and that version reads the file. When the file changes
// after the copy, an older version wrote it, so the file is right and the
// server copies it again.
func importApps(db *sql.DB) error {
	info, err := os.Stat(appsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	modified := info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z")
	var imported string
	db.QueryRow("SELECT modified FROM imported_files WHERE file = 'apps.yml'").Scan(&imported)
	if imported == modified {
		return nil
	}

	apps, err := matcha.ListAppsFrom(appsPath())
	if err != nil {
		return fmt.Errorf("%s: %w", appsPath(), err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM apps"); err != nil {
		return err
	}
	for name, app := range apps {
		if err := insertApp(tx, name, app); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO imported_files (file, modified) VALUES ('apps.yml', ?)", modified); err != nil {
		return err
	}
	return tx.Commit()
}
