package main

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/karloscodes/chasen/internal/engine"
	"github.com/karloscodes/chasen/protocol"
)

// The overview is what a glance needs, for each app of the server, in one
// call: its state, its version, and the change that runs now. The bar of a
// desktop asks for it every few seconds while a deploy runs, so it reads
// Docker once and the database once. The map of the screen draws the rest:
// the domains that bring traffic, the jobs beside the app, and where its data
// goes. Those come from the same reads, and from the directory of backups.

type overviewApp struct {
	App     string `json:"app"`
	State   string `json:"state"` // as Docker says it: "Up 3 hours (healthy)", "Exited (1) 2 minutes ago"
	Up      bool   `json:"up"`    // the container runs, and it is not unhealthy
	Version string `json:"version"`
	Running string `json:"running,omitempty"` // the change that runs now: "deploy 3f9a2c1"
	Since   string `json:"since,omitempty"`   // when that change started, in RFC 3339
	ID      int64  `json:"id,omitempty"`      // its id in the history, to follow its output

	Domains   []string `json:"domains"`
	Private   string   `json:"private,omitempty"` // where a private app listens on the server, like 127.0.0.1:8080
	Jobs      string   `json:"jobs,omitempty"`    // the state of the jobs container, when the app has one
	Databases int      `json:"databases"`         // the SQLite databases in the newest backup
	Backup    string   `json:"backup"`            // the stamp of the newest backup, "" before the first, "off" with backup: false
	Replica   string   `json:"replica"`           // the live replica: "live", "behind", or "off"
}

// serverOverview prints the overview as a table, or as JSON with --json.
func serverOverview(args []string) error {
	rows, err := overview()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "--json" {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tSTATE\tVERSION\tNOW")
	for _, r := range rows {
		now := r.Running
		if t, err := time.Parse(time.RFC3339, r.Since); err == nil && now != "" {
			now += ", for " + age(time.Since(t))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.App, r.State, r.Version, now)
	}
	return w.Flush()
}

func overview() ([]overviewApp, error) {
	apps, err := listApps()
	if err != nil {
		return nil, err
	}
	all, _ := containers()
	type change struct {
		id              int64
		action, started string
	}
	running := map[string]change{}
	settings := map[string]protocol.Settings{}
	behind := map[string]bool{}
	if db, err := openServerDB(); err == nil {
		defer db.Close()
		if rows, err := db.Query("SELECT id, app, action, started_at FROM activity WHERE status = 'running' ORDER BY id"); err == nil {
			defer rows.Close()
			for rows.Next() {
				var c change
				var app string
				if rows.Scan(&c.id, &app, &c.action, &c.started) == nil {
					if at, err := time.Parse("2006-01-02 15:04:05", c.started); err == nil {
						c.started = at.UTC().Format(time.RFC3339)
					}
					running[app] = c
				}
			}
		}
		settings, behind = allSettings(db), behindReplicas(db, time.Now())
	}
	live := replicaPID() != 0
	var rows []overviewApp
	for _, name := range engine.ListAppsSorted(apps) {
		c := all.of(name)
		_, version, _ := strings.Cut(apps[name].Image, ":")
		r := overviewApp{App: name, State: c.Status, Version: version, Up: c.State == "running" && !strings.Contains(c.Status, "unhealthy")}
		if r.State == "" {
			r.State = "no container"
		}
		if now, ok := running[name]; ok {
			r.Running, r.Since, r.ID = now.action, now.started, now.id
		}
		r.Domains = strings.FieldsFunc(apps[name].Domain, func(c rune) bool { return c == ',' })
		r.Private = apps[name].Listen
		if settings[name].Jobs != "" {
			r.Jobs = cmp.Or(all.of(jobsName(name)).Status, "not running")
		}
		r.Backup, r.Databases = newestBackup(name)
		r.Replica = "off"
		switch {
		case settings[name].NoBackup:
			r.Backup = "off"
		case live && behind[name]:
			r.Replica = "behind"
		case live && r.Databases > 0:
			r.Replica = "live"
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b overviewApp) int { return strings.Compare(a.App, b.App) })
	return rows, nil
}

// allSettings reads the settings of every app in one query.
func allSettings(db *sql.DB) map[string]protocol.Settings {
	all := map[string]protocol.Settings{}
	rows, err := db.Query("SELECT app, settings FROM app_settings")
	if err != nil {
		return all
	}
	defer rows.Close()
	for rows.Next() {
		var app string
		var data []byte
		var s protocol.Settings
		if rows.Scan(&app, &data) == nil && json.Unmarshal(data, &s) == nil {
			all[app] = s
		}
	}
	return all
}

// behindReplicas gives the apps with a database that the live replica has
// not copied for longer than it may: the same rule as chasen verify.
func behindReplicas(db *sql.DB, now time.Time) map[string]bool {
	behind := map[string]bool{}
	rows, err := db.Query("SELECT app, behind_since FROM replica_state WHERE behind_since IS NOT NULL")
	if err != nil {
		return behind
	}
	defer rows.Close()
	for rows.Next() {
		var app, since string
		if rows.Scan(&app, &since) != nil {
			continue
		}
		if from, err := time.Parse(time.RFC3339, since); err == nil && now.Sub(from) > replicaMayBeBehind {
			behind[app] = true
		}
	}
	return behind
}

// newestBackup returns the stamp of the newest backup of an app, and how
// many databases it holds: one file for each.
func newestBackup(name string) (stamp string, databases int) {
	stamps := localBackups(name)
	if len(stamps) == 0 {
		return "", 0
	}
	files, _ := backupFiles(backupsDir(name) + "/" + stamps[0])
	return stamps[0], len(files)
}

// container is what Docker says of one container: "Up 3 hours (healthy)",
// and "running".
type container struct{ Status, State string }

type containerList map[string]container

// containers asks the socket of Docker for every container, by name: one
// request, with no docker command to start. The screen and the bar ask every
// few seconds.
func containers() (containerList, error) {
	var list []struct {
		Names         []string
		Status, State string
	}
	if err := dockerAPI("GET", "/containers/json?all=1", nil, &list); err != nil {
		return nil, err
	}
	all := containerList{}
	for _, c := range list {
		for _, name := range c.Names {
			all[strings.TrimPrefix(name, "/")] = container{c.Status, c.State}
		}
	}
	return all, nil
}

// of returns the container of an app. During a deploy an app has two, and
// the one that runs counts.
func (all containerList) of(app string) container {
	c := all[app]
	if next := all[app+"-next"]; next.State == "running" && c.State != "running" {
		c = next
	}
	return c
}
