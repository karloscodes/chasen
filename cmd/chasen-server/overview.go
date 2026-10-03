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
	// The containers: an app has two during a deploy, and the one that runs counts.
	containers := map[string][2]string{} // name: status, state
	if out, err := docker("ps", "-a", "--format", "{{.Names}}\t{{.Status}}\t{{.State}}"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.SplitN(line, "\t", 3); len(f) == 3 {
				containers[f[0]] = [2]string{f[1], f[2]}
			}
		}
	}
	running := map[string][2]string{} // app: action, started
	if db, err := openServerDB(); err == nil {
		defer db.Close()
		if rows, err := db.Query("SELECT app, action, started_at FROM activity WHERE status = 'running' ORDER BY id"); err == nil {
			defer rows.Close()
			for rows.Next() {
				var app, action, started string
				if rows.Scan(&app, &action, &started) == nil {
					if at, err := time.Parse("2006-01-02 15:04:05", started); err == nil {
						started = at.UTC().Format(time.RFC3339)
					}
					running[app] = [2]string{action, started}
				}
			}
		}
	}
	var rows []overviewApp
	for _, name := range matcha.ListAppsSorted(apps) {
		c := containers[name]
		if next := containers[name+"-next"]; next[1] == "running" && c[1] != "running" {
			c = next
		}
		_, version, _ := strings.Cut(apps[name].Image, ":")
		r := overviewApp{App: name, State: c[0], Version: version, Up: c[1] == "running" && !strings.Contains(c[0], "unhealthy")}
		if r.State == "" {
			r.State = "no container"
		}
		if now, ok := running[name]; ok {
			r.Running, r.Since = now[0], now[1]
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b overviewApp) int { return strings.Compare(a.App, b.App) })
	return rows, nil
}
