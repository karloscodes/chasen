package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/karloscodes/chasen/protocol"
)

// chasen overview --json is the tree of every server you are logged in to,
// for a desktop: each server with its apps, their state, the change that runs
// now, and the alerts of the server.
//
// chasen overview --watch prints it again, one line each time, and keeps its
// connections to the servers open between two asks: no new SSH login each
// time. The plugin of Omarchy runs it for as long as the bar runs.

type overviewJSON struct {
	Servers  []serverView `json:"servers"`
	Running  int          `json:"running"`  // changes that run now, on all servers
	Down     int          `json:"down"`     // apps that do not run
	Errors   int          `json:"errors"`   // alerts that are errors
	Warnings int          `json:"warnings"` // alerts that are warnings, and servers that do not answer
}

type serverView struct {
	Name   string      `json:"name"`
	Error  string      `json:"error,omitempty"` // why the server did not answer
	Apps   []appView   `json:"apps"`
	Alerts []alertView `json:"alerts"`
}

type appView struct {
	App     string `json:"app"`
	State   string `json:"state"`
	Up      bool   `json:"up"`
	Version string `json:"version"`
	Running string `json:"running,omitempty"`
	Since   string `json:"since,omitempty"`
	ID      int64  `json:"id,omitempty"`
}

type alertView struct {
	Error bool   `json:"error"`
	What  string `json:"what"`
	Fix   string `json:"fix"`
}

func overviewAll(w io.Writer) error {
	return json.NewEncoder(w).Encode(overviewOf(nil))
}

// How often --watch asks: every minute, and every 5 seconds while a change
// runs. The alerts change slowly: at most once a minute.
const (
	watchIdle    = time.Minute
	watchRunning = 5 * time.Second
)

// watchOverview prints the overview now, then again after each wait, until
// it cannot write: the program that reads it ended. A line on stdin asks at
// once, for a panel that opens.
func watchOverview(stdin io.Reader, w io.Writer) error {
	asked := make(chan struct{}, 1)
	go func() {
		lines := bufio.NewScanner(stdin)
		for lines.Scan() {
			select {
			case asked <- struct{}{}:
			default:
			}
		}
	}()
	var last overviewJSON
	var alertsAt time.Time
	for {
		var before []serverView
		if time.Since(alertsAt) < watchIdle {
			before = last.Servers
		} else {
			alertsAt = time.Now()
		}
		last = overviewOf(before)
		if err := json.NewEncoder(w).Encode(last); err != nil {
			return err
		}
		wait := watchIdle
		if last.Running > 0 {
			wait = watchRunning
		}
		select {
		case <-time.After(wait):
		case <-asked:
		}
	}
}

// overviewOf asks every server at the same time. With the views of before,
// it keeps their alerts and does not ask for them.
func overviewOf(before []serverView) overviewJSON {
	saved := loadLogins()
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	views := make([]serverView, len(addresses))
	var wg sync.WaitGroup
	for i, address := range addresses {
		name := barName(address)
		var alerts []alertView
		for _, v := range before {
			if v.Name == name && v.Error == "" {
				alerts = v.Alerts
			}
		}
		wg.Go(func() {
			views[i] = askServer(name, credentials{URL: address, Token: saved.Tokens[address]}, alerts)
		})
	}
	wg.Wait()

	all := overviewJSON{Servers: views}
	for _, v := range views {
		if v.Error != "" {
			all.Warnings++
		}
		for _, a := range v.Apps {
			if a.Running != "" {
				all.Running++
			}
			if !a.Up {
				all.Down++
			}
		}
		for _, a := range v.Alerts {
			if a.Error {
				all.Errors++
			} else {
				all.Warnings++
			}
		}
	}
	return all
}

// askServer asks one server for its overview, then for its alerts, unless
// they are known. One after the other, the two asks share one connection.
func askServer(name string, creds credentials, known []alertView) serverView {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := protocol.Client(creds)
	view := serverView{Name: name, Apps: []appView{}, Alerts: []alertView{}}
	var apps strings.Builder
	code, err := client.Run(ctx, "overview", []string{"--json"}, nil, &apps)
	if err != nil && ctx.Err() == nil {
		// A connection that stayed open can be gone: after a sleep of the
		// computer, or a restart of the server. The next one is new.
		apps.Reset()
		code, err = client.Run(ctx, "overview", []string{"--json"}, nil, &apps)
	}
	if err == nil && code != 0 {
		err = fmt.Errorf("it has no overview yet: chasen-server update")
	}
	if err != nil {
		view.Error = err.Error()
		return view
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(apps.String())), &view.Apps); err != nil {
		view.Error = "its overview is not readable: " + err.Error()
	}
	if known != nil {
		view.Alerts = known
		return view
	}
	var alerts strings.Builder
	client.Run(ctx, "alerts", nil, nil, &alerts)
	list, _ := parseAlerts(alerts.String())
	for _, a := range list {
		view.Alerts = append(view.Alerts, alertView{a.error, a.what, a.fix})
	}
	return view
}
