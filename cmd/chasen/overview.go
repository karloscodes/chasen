package main

import (
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
// now, and the alerts of the server. The plugin of Omarchy draws it, and asks
// again every few seconds while something runs.

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
}

type alertView struct {
	Error bool   `json:"error"`
	What  string `json:"what"`
	Fix   string `json:"fix"`
}

func overviewAll(w io.Writer) error {
	saved := loadLogins()
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	views := make([]serverView, len(addresses))
	var wg sync.WaitGroup
	for i, address := range addresses {
		wg.Go(func() { views[i] = askServer(barName(address), credentials{URL: address, Token: saved.Tokens[address]}) })
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
	return json.NewEncoder(w).Encode(all)
}

// askServer asks one server for its overview and its alerts, at the same time.
func askServer(name string, creds credentials) serverView {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := protocol.Client(creds)
	view := serverView{Name: name, Apps: []appView{}, Alerts: []alertView{}}
	var apps, alerts strings.Builder
	var appsErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		code, err := client.Run(ctx, "overview", []string{"--json"}, nil, &apps)
		if err == nil && code != 0 {
			err = fmt.Errorf("it has no overview yet: chasen-server update")
		}
		appsErr = err
	})
	wg.Go(func() { client.Run(ctx, "alerts", nil, nil, &alerts) })
	wg.Wait()
	if appsErr != nil {
		view.Error = appsErr.Error()
		return view
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(apps.String())), &view.Apps); err != nil {
		view.Error = "its overview is not readable: " + err.Error()
	}
	list, _ := parseAlerts(alerts.String())
	for _, a := range list {
		view.Alerts = append(view.Alerts, alertView{a.error, a.what, a.fix})
	}
	return view
}
