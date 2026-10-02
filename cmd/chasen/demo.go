package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// `chasen demo` is the screen with a server that lives in this program. It
// has three made-up apps, and it answers the protocol commands as a real
// server does: a backup adds a backup, a restart takes a moment, a deploy
// makes a new version. Nothing leaves the machine.

type demoApp struct {
	name, version, state string
	started              time.Time
	domains              []string
	backups              []string // the stamps, newest first
	history              []demoEntry
}

type demoEntry struct {
	at             time.Time
	action, result string
	output         string
}

type demoServer struct {
	mu   sync.Mutex
	apps []*demoApp
	// wait is the time a step takes. A test makes it short.
	wait func(ctx context.Context, d time.Duration) bool
}

func newDemoServer(now time.Time) *demoServer {
	hour := now.UTC().Truncate(time.Hour)
	stamps := func(count int) (list []string) {
		for i := range count {
			list = append(list, hour.Add(-time.Duration(i)*time.Hour).Format(stampLayout))
		}
		return list
	}
	return &demoServer{wait: wait, apps: []*demoApp{
		{
			name: "blog", version: "a1b2c3d4e5f6", state: "Up 6 days", started: now.Add(-6 * 24 * time.Hour),
			domains: []string{"blog.example.com"},
			history: []demoEntry{{now.Add(-6 * 24 * time.Hour), "deploy a1b2c3d", "succeeded", "Pulling ghcr.io/you/blog:a1b2c3d\nDeployed blog a1b2c3d\n  https://blog.example.com\n"}},
		},
		{
			name: "lognorth", version: "latest", state: "Up 2 days (healthy)", started: now.Add(-48 * time.Hour),
			domains: []string{"lognorth.example.com"}, backups: stamps(3),
			history: []demoEntry{{now.Add(-48 * time.Hour), "enable lognorth", "succeeded", "Pulling karloscodes/lognorth:latest\nStarting lognorth\nEnabled lognorth\n  https://lognorth.example.com\n"}},
		},
		{
			name: "shop", version: "3f9a2c1d5e8b", state: "Up 3 hours (healthy)", started: now.Add(-3 * time.Hour),
			domains: []string{"shop.example.com", "shop.com"}, backups: stamps(6),
			history: []demoEntry{
				{now.Add(-26 * time.Hour), "deploy 727846a", "succeeded", "Pulling ghcr.io/you/shop:727846a\nDeployed shop 727846a\n"},
				{now.Add(-25 * time.Hour), "domains add shop.com", "succeeded", "Added shop.com\n"},
				{now.Add(-5 * time.Hour), "deploy 6cff7df", "failed", "Pulling ghcr.io/you/shop:6cff7df\nshop: backup " + hour.Add(-5*time.Hour).Format(stampLayout) + " (on the server and offsite)\nStarting shop 6cff7df\nThe new version did not answer /up in 30 seconds. The previous version keeps the traffic.\nThe app must listen on port 3000 and answer 200 on /up\n"},
				{now.Add(-3 * time.Hour), "deploy 3f9a2c1", "succeeded", "Pulling ghcr.io/you/shop:3f9a2c1\nshop: backup " + hour.Add(-3*time.Hour).Format(stampLayout) + " (on the server and offsite)\nStarting shop 3f9a2c1\nDeployed shop 3f9a2c1\n  https://shop.example.com\n  https://shop.com\n"},
			},
		},
	}}
}

func (s *demoServer) app(name string) *demoApp {
	for _, app := range s.apps {
		if app.name == name {
			return app
		}
	}
	return nil
}

// record adds an entry to the history of an app.
func (app *demoApp) record(action, result, output string) {
	app.history = append(app.history, demoEntry{time.Now(), action, result, output})
}

// wait is the time a step takes on a real server. It ends early when the
// screen closes.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// run answers one protocol command.
func (s *demoServer) run(ctx context.Context, out io.Writer, args ...string) (int, error) {
	command, name := args[0], ""
	if len(args) > 1 {
		name = args[1]
	}
	if command == "logs" {
		return s.logs(ctx, out, name)
	}
	// The steps of a change take time, like on a server. The lock is not held
	// while they wait, so the screen stays alive.
	say := func(pause time.Duration, format string, a ...any) bool {
		fmt.Fprintf(out, format+"\n", a...)
		return s.wait(ctx, pause)
	}

	s.mu.Lock()
	app := s.app(name)
	if command == "list" {
		defer s.mu.Unlock()
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVERSION\tDOMAINS")
		for _, app := range s.apps {
			fmt.Fprintf(w, "%s\t%s\t%s\n", app.name, app.version, strings.Join(app.domains, " "))
		}
		return 0, w.Flush()
	}
	if app == nil {
		s.mu.Unlock()
		fmt.Fprintf(out, "Error: app %q is not deployed\n", name)
		return 1, nil
	}

	switch command {
	case "status":
		defer s.mu.Unlock()
		fmt.Fprintf(out, "App:      %s\nVersion:  %s\nState:    %s\n", app.name, app.version, app.state)
		for _, domain := range app.domains {
			fmt.Fprintf(out, "URL:      https://%s\n", domain)
		}
		backup, replica := "none", "off"
		if len(app.backups) > 0 {
			backup, replica = app.backups[0], "live"
		}
		fmt.Fprintf(out, "Backup:   %s\nReplica:  %s\n", backup, replica)
	case "history":
		defer s.mu.Unlock()
		if len(args) == 3 {
			for i, entry := range app.history {
				if fmt.Sprint(i+1) == args[2] {
					io.WriteString(out, entry.output)
					return 0, nil
				}
			}
			fmt.Fprintf(out, "Error: no entry %s for %s\n", args[2], name)
			return 1, nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tWHEN (UTC)\tACTION\tRESULT")
		for i := len(app.history) - 1; i >= 0; i-- {
			entry := app.history[i]
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, entry.at.UTC().Format("2006-01-02 15:04:05"), entry.action, entry.result)
		}
		return 0, w.Flush()
	case "backups":
		defer s.mu.Unlock()
		if len(app.backups) == 0 {
			fmt.Fprintln(out, "No backups.")
			return 0, nil
		}
		for _, stamp := range app.backups {
			fmt.Fprintf(out, "%s  server + offsite\n", stamp)
		}
		fmt.Fprintln(out, "live              offsite, continuous")
	case "domains":
		switch {
		case len(args) == 4 && args[2] == "add":
			app.domains = append(app.domains, args[3])
			app.record("domains add "+args[3], "succeeded", "Added "+args[3]+"\n")
			s.mu.Unlock()
			say(900*time.Millisecond, "Getting a certificate for %s", args[3])
			say(0, "Added %s", args[3])
		case len(args) == 4 && args[2] == "rm":
			app.domains = slices.DeleteFunc(app.domains, func(d string) bool { return d == args[3] })
			app.record("domains rm "+args[3], "succeeded", "Removed "+args[3]+"\n")
			s.mu.Unlock()
			say(0, "Removed %s", args[3])
		default:
			defer s.mu.Unlock()
			for _, domain := range app.domains {
				fmt.Fprintln(out, domain)
			}
		}
	case "backup":
		stamp := time.Now().UTC().Format(stampLayout)
		s.mu.Unlock()
		if !s.wait(ctx, 700*time.Millisecond) {
			return 0, ctx.Err()
		}
		s.mu.Lock()
		app.backups = append([]string{stamp}, app.backups...)
		s.mu.Unlock()
		say(0, "%s: backup %s (on the server and offsite)", name, stamp)
	case "restart":
		version := app.version
		s.mu.Unlock()
		if !say(1200*time.Millisecond, "Starting %s %.7s", name, version) || !say(600*time.Millisecond, "Waiting for /up") {
			return 0, ctx.Err()
		}
		s.mu.Lock()
		app.state, app.started = "Up 1 second (healthy)", time.Now()
		app.record("restart", "succeeded", "Starting "+name+"\nRestarted "+name+"\n")
		s.mu.Unlock()
		say(0, "Restarted %s", name)
	case "restore":
		s.mu.Unlock()
		if !say(900*time.Millisecond, "Stopping %s", name) || !say(900*time.Millisecond, "Restoring the databases from %s", strings.Join(args[2:], " ")) {
			return 0, ctx.Err()
		}
		s.mu.Lock()
		app.record("restore", "succeeded", "Restored "+name+"\n")
		s.mu.Unlock()
		say(0, "Restored %s from %s. The previous databases are in\n/var/matcha/%s/pre-restore-%s", name, strings.Join(args[2:], " "), name, time.Now().UTC().Format(stampLayout))
	default:
		s.mu.Unlock()
		fmt.Fprintf(out, "The demo does not have the command %s.\n", command)
		return 1, nil
	}
	return 0, nil
}

// logs writes a request line now and then, until the screen stops listening.
func (s *demoServer) logs(ctx context.Context, out io.Writer, name string) (int, error) {
	paths := []string{"GET /up 200 1ms", "GET / 200 12ms", "GET /products 200 18ms", "POST /cart 302 9ms", "GET /assets/app.css 304 0ms", "GET /checkout 200 21ms"}
	for i := 0; ; i++ {
		fmt.Fprintf(out, "%s %s %s\n", time.Now().UTC().Format("15:04:05"), name, paths[i%len(paths)])
		if !s.wait(ctx, time.Duration(300+(i*377)%900)*time.Millisecond) {
			return 0, nil
		}
	}
}

// deploy plays a deploy of the shop: a build, a push, a backup, and a new
// version. Every third deploy fails its health check, to show that the old
// version stays.
func (s *demoServer) deploy(ctx context.Context, out io.Writer) error {
	s.mu.Lock()
	app := s.app("shop")
	count := len(app.history)
	s.mu.Unlock()
	version := fmt.Sprintf("%07x", time.Now().UnixNano()&0xfffffff)
	var output strings.Builder
	out = io.MultiWriter(out, &output)
	steps := []string{
		"Building ghcr.io/you/shop:" + version,
		"#5 [2/4] COPY . .",
		"#6 [3/4] RUN bundle install",
		"#7 [4/4] RUN bin/rails assets:precompile",
		"Pushing ghcr.io/you/shop:" + version,
		"Pulling ghcr.io/you/shop:" + version,
		"Port 3000 (EXPOSE in the image). Health path /up. Storage /storage.",
		"shop: backup " + time.Now().UTC().Format(stampLayout) + " (on the server and offsite)",
		"Starting shop " + version,
	}
	for _, step := range steps {
		fmt.Fprintln(out, step)
		if !s.wait(ctx, 650*time.Millisecond) {
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app.backups = append([]string{time.Now().UTC().Format(stampLayout)}, app.backups...)
	if count%3 == 2 {
		fmt.Fprintln(out, "The new version did not answer /up in 30 seconds. The previous version keeps the traffic.")
		app.record("deploy "+version, "failed", output.String())
		return fmt.Errorf("the deploy failed. %s is still live", app.version[:7])
	}
	app.version, app.state, app.started = version, "Up 1 second (healthy)", time.Now()
	fmt.Fprintf(out, "\nDeployed shop %s\n  https://shop.example.com\n", version)
	app.record("deploy "+version, "succeeded", output.String())
	return nil
}

// demoScreen is the screen on top of the demo server. The app of the
// directory is the shop, so d deploys it.
func demoScreen() *tui {
	server := newDemoServer(time.Now())
	t := newTUI(server.run, "demo (nothing here is real)", "shop")
	t.deployCommand = server.deploy
	return t
}
