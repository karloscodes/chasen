package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/karloscodes/matcha"
)

// The overview is what a glance needs, for each app of the server, in one
// call: its state, its version, and the change that runs now. The bar of a
// desktop asks for it every few seconds while a deploy runs, so it reads
// Docker once and the database once.

type overviewApp struct {
	App     string `json:"app"`
	State   string `json:"state"` // as Docker says it: "Up 3 hours (healthy)", "Exited (1) 2 minutes ago"
	Up      bool   `json:"up"`    // the container runs, and it is not unhealthy
	Version string `json:"version"`
	Running string `json:"running,omitempty"` // the change that runs now: "deploy 3f9a2c1"
	Since   string `json:"since,omitempty"`   // when that change started, in RFC 3339
	ID      int64  `json:"id,omitempty"`      // its id in the history, to follow its output
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
	}
	var rows []overviewApp
	for _, name := range matcha.ListAppsSorted(apps) {
		c := all.of(name)
		_, version, _ := strings.Cut(apps[name].Image, ":")
		r := overviewApp{App: name, State: c.Status, Version: version, Up: c.State == "running" && !strings.Contains(c.Status, "unhealthy")}
		if r.State == "" {
			r.State = "no container"
		}
		if now, ok := running[name]; ok {
			r.Running, r.Since, r.ID = now.action, now.started, now.id
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b overviewApp) int { return strings.Compare(a.App, b.App) })
	return rows, nil
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
