// Package mock is a Chasen server for development: it answers the protocol
// over HTTP as chasen-server does, with made-up apps that live in memory. It
// runs no Docker and touches nothing. Use it to work on the CLI and its
// screen: bin/dev starts it.
//
// A backup adds a backup, a restart takes a moment, a deploy makes a new
// version. To see a deploy fail, give the app the env FAIL in chasen.yml: it
// does not get healthy, and the old version stays.
package mock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/karloscodes/chasen/protocol"
)

const stampLayout = "20060102T150405Z"

type app struct {
	name, version, state string
	domains              []string
	backups              []string // the stamps, newest first
	history              []entry
}

type entry struct {
	at             time.Time
	action, result string
	output         string
}

func (a *app) record(action, result, output string) {
	a.history = append(a.history, entry{time.Now(), action, result, output})
}

// begin adds an entry that runs. Its output grows while the command works,
// as on a real server, so the history shows a deploy that is on its way. The
// writer it returns is for that output. Call it with the lock held.
func (s *Server) begin(a *app, action string) (index int, output io.Writer) {
	a.history = append(a.history, entry{at: time.Now(), action: action, result: "running"})
	index = len(a.history) - 1
	return index, running{s, a, index}
}

// end gives a running entry its result. Call it with the lock held.
func (a *app) end(index int, result string) { a.history[index].result = result }

type running struct {
	s     *Server
	a     *app
	index int
}

func (r running) Write(p []byte) (int, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.a.history[r.index].output += string(p)
	return len(p), nil
}

// Server is the mock server. It is an http.Handler.
type Server struct {
	// Token is the token that a request must send.
	Token string
	// Wait is the time a step takes. A test makes it short.
	Wait func(ctx context.Context, d time.Duration) bool
	// Log gets one line for each request, or nil.
	Log *log.Logger

	mu     sync.Mutex
	apps   []*app
	bucket string // the name of the bucket of the server, or ""
}

// New returns a server with three apps: a blog with no backups, the addon
// lognorth, and a shop with a failed deploy in its history.
func New(now time.Time) *Server {
	hour := now.UTC().Truncate(time.Hour)
	stamps := func(count int) (list []string) {
		for i := range count {
			list = append(list, hour.Add(-time.Duration(i)*time.Hour).Format(stampLayout))
		}
		return list
	}
	return &Server{Token: "dev", Wait: wait, apps: []*app{
		{
			name: "blog", version: "a1b2c3d", state: "Up 6 days", domains: []string{"blog.example.com"},
			history: []entry{{now.Add(-6 * 24 * time.Hour), "deploy a1b2c3d", "succeeded", "Pulling ghcr.io/you/blog:a1b2c3d\nDeployed blog a1b2c3d\n  https://blog.example.com\n"}},
		},
		{
			name: "lognorth", version: "latest", state: "Up 2 days (healthy)", domains: []string{"lognorth.example.com"}, backups: stamps(3),
			history: []entry{{now.Add(-48 * time.Hour), "enable lognorth", "succeeded", "Pulling karloscodes/lognorth:latest\nStarting lognorth\nEnabled lognorth\n  https://lognorth.example.com\n"}},
		},
		{
			name: "shop", version: "3f9a2c1", state: "Up 3 hours (healthy)", domains: []string{"shop.example.com", "shop.com"}, backups: stamps(6),
			history: []entry{
				{now.Add(-26 * time.Hour), "deploy 727846a", "succeeded", "Pulling ghcr.io/you/shop:727846a\nDeployed shop 727846a\n"},
				{now.Add(-25 * time.Hour), "domains add shop.com", "succeeded", "Added shop.com\n"},
				{now.Add(-5 * time.Hour), "deploy 6cff7df", "failed", "Pulling ghcr.io/you/shop:6cff7df\nStarting shop 6cff7df\nThe new version did not answer /up in 30 seconds. The previous version keeps the traffic.\nThe app must listen on port 3000 and answer 200 on /up\n"},
				{now.Add(-3 * time.Hour), "deploy 3f9a2c1", "succeeded", "Pulling ghcr.io/you/shop:3f9a2c1\nStarting shop 3f9a2c1\nDeployed shop 3f9a2c1\n  https://shop.example.com\n  https://shop.com\n"},
			},
		},
	}}
}

func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// stream sends each write to the client at once, and knows if the output
// ends in the middle of a line.
type stream struct {
	w     http.ResponseWriter
	alone bool // the last byte was a newline, or nothing was written
}

func (s *stream) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	if n > 0 {
		s.alone = p[n-1] == '\n'
	}
	return n, err
}

// ServeHTTP answers POST /v1/<command>?arg=...&arg=... like chasen-server:
// the output as it comes, then the exit marker with the exit code.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/up" {
		fmt.Fprintln(w, "ok")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		http.Error(w, "the token is not valid", http.StatusUnauthorized)
		return
	}
	// The two requests that are not a command with a text output.
	if r.Method == http.MethodGet && r.URL.Path == protocol.ShellPath {
		s.shell(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == protocol.DownloadPath {
		s.download(w, r)
		return
	}
	command, ok := strings.CutPrefix(r.URL.Path, "/v1/")
	if !ok || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	args := r.URL.Query()["arg"]
	out := &stream{w: w, alone: true}
	code := s.run(r.Context(), out, r.Body, command, args)
	if !out.alone {
		io.WriteString(out, "\n")
	}
	fmt.Fprintf(out, "%s%d\n", protocol.ExitMarker, code)
	if s.Log != nil {
		s.Log.Printf("%s %s -> %d", command, strings.Join(args, " "), code)
	}
}

func (s *Server) app(name string) *app {
	for _, a := range s.apps {
		if a.name == name {
			return a
		}
	}
	return nil
}

// settings reads the settings that deploy, check, and restart send: one line
// of JSON. The files of a website follow it, and the mock does not need them.
func settings(body io.Reader) protocol.Settings {
	var sent protocol.Settings
	if body == nil {
		return sent
	}
	line, _ := bufio.NewReader(body).ReadBytes('\n')
	json.Unmarshal(line, &sent)
	io.Copy(io.Discard, body)
	return sent
}

// run answers one command and returns its exit code.
func (s *Server) run(ctx context.Context, out io.Writer, body io.Reader, command string, args []string) int {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	// The steps of a change take time, like on a server. The lock is not held
	// while they wait.
	say := func(pause time.Duration, format string, a ...any) bool {
		fmt.Fprintf(out, format+"\n", a...)
		return s.Wait(ctx, pause)
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(out, "Error: "+format+"\n", a...)
		return 1
	}

	switch command {
	case "bucket":
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(args) == 0 {
			if s.bucket == "" {
				fmt.Fprintln(out, "No bucket. The backups stay on this server.")
			} else {
				fmt.Fprintf(out, "Bucket %s at https://s3.example.com (region auto)\n\n", s.bucket)
				fmt.Fprintln(out, "APP   SNAPSHOTS             LIVE REPLICA  TOTAL")
				fmt.Fprintln(out, "shop  40.0 MB in 2 backups  90.0 MB       130.0 MB")
				fmt.Fprintln(out, "Total: 130.0 MB in 6 objects")
			}
			return 0
		}
		// The secret access key is the first line of the request, like on a server.
		secret, _ := bufio.NewReader(body).ReadString('\n')
		name := ""
		for i, arg := range args {
			if arg == "--name" && i+1 < len(args) {
				name = args[i+1]
			}
		}
		if strings.TrimSpace(secret) == "" || name == "" {
			return fail("usage: chasen-server bucket --endpoint <url> --name <bucket> --access-key-id <id> [--region <region>]")
		}
		s.bucket = name
		fmt.Fprintf(out, "Backups now go to the bucket %s, and the live replica starts.\n", name)
		return 0
	case "list":
		s.mu.Lock()
		defer s.mu.Unlock()
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVERSION\tDOMAINS")
		for _, a := range s.apps {
			fmt.Fprintf(w, "%s\t%s\t%s\n", a.name, a.version, strings.Join(a.domains, " "))
		}
		w.Flush()
		return 0
	case "load":
		// Numbers that move, so the screen has something to show.
		beat := float64(time.Now().Unix()%20) / 20
		fmt.Fprintf(out, "Load:     %.2f %.2f %.2f (4 cores)\nMemory:   %.1f GB of 7.6 GB (%d%%)\nDisk:     31 GB of 75 GB (41%%)\n",
			0.2+beat, 0.4+beat/2, 0.5, 2.1+beat, int((2.1+beat)*100/7.6))
		return 0
	case "logout":
		return 0
	case "deploy", "check":
		if len(args) < 2 {
			return fail("usage: %s <app> <version>", command)
		}
		return s.deploy(ctx, out, command, name, args[1], settings(body))
	case "enable":
		return s.deploy(ctx, out, "enable", name, "latest", protocol.Settings{})
	case "logs":
		if s.exists(name) {
			return s.logs(ctx, out, name)
		}
	}

	s.mu.Lock()
	a := s.app(name)
	if a == nil {
		s.mu.Unlock()
		return fail("app %q is not deployed", name)
	}

	switch command {
	case "status":
		defer s.mu.Unlock()
		fmt.Fprintf(out, "App:      %s\nVersion:  %s\nState:    %s\n", a.name, a.version, a.state)
		for _, domain := range a.domains {
			fmt.Fprintf(out, "URL:      https://%s\n", domain)
		}
		backup, replica := "none", "off"
		if len(a.backups) > 0 {
			backup, replica = a.backups[0], "live"
		}
		fmt.Fprintf(out, "Backup:   %s\nReplica:  %s\n", backup, replica)
	case "history":
		defer s.mu.Unlock()
		if len(args) == 2 {
			for i, e := range a.history {
				if fmt.Sprint(i+1) == args[1] {
					io.WriteString(out, e.output)
					return 0
				}
			}
			return fail("no entry %s for %s", args[1], name)
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tWHEN (UTC)\tACTION\tRESULT")
		for i := len(a.history) - 1; i >= 0; i-- {
			e := a.history[i]
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, e.at.UTC().Format("2006-01-02 15:04:05"), e.action, e.result)
		}
		w.Flush()
	case "verify":
		defer s.mu.Unlock()
		fmt.Fprintf(out, "Checking the copies of %s. Nothing changes.\n", a.name)
		if len(a.backups) == 0 {
			fmt.Fprintln(out, "  skip  the live replica (this server has no bucket)")
			fmt.Fprintln(out, "  skip  the newest snapshot (the app has none yet)")
		} else {
			fmt.Fprintln(out, "  ok    the live replica restores storage/db.sqlite3 (1.2 MB), and it passes the integrity check")
			fmt.Fprintf(out, "  ok    the snapshot %s restores storage/db.sqlite3 (1.2 MB), and it passes the integrity check\n", a.backups[0])
		}
		fmt.Fprintln(out, "The copies restore.")
	case "backups":
		defer s.mu.Unlock()
		if len(a.backups) == 0 {
			fmt.Fprintln(out, "No backups.")
			return 0
		}
		for _, stamp := range a.backups {
			fmt.Fprintf(out, "%s  server + offsite\n", stamp)
		}
		fmt.Fprintln(out, "live              offsite, continuous")
	case "domains":
		switch {
		case len(args) == 3 && args[1] == "add":
			a.domains = append(a.domains, args[2])
			a.record("domains add "+args[2], "succeeded", "Added "+args[2]+"\n")
			s.mu.Unlock()
			say(900*time.Millisecond, "Getting a certificate for %s", args[2])
			say(0, "Added %s", args[2])
		case len(args) == 3 && args[1] == "rm":
			a.domains = slices.DeleteFunc(a.domains, func(d string) bool { return d == args[2] })
			a.record("domains rm "+args[2], "succeeded", "Removed "+args[2]+"\n")
			s.mu.Unlock()
			say(0, "Removed %s", args[2])
		default:
			defer s.mu.Unlock()
			for _, domain := range a.domains {
				fmt.Fprintln(out, domain)
			}
		}
	case "backup":
		stamp := time.Now().UTC().Format(stampLayout)
		s.mu.Unlock()
		if !s.Wait(ctx, 700*time.Millisecond) {
			return 1
		}
		s.mu.Lock()
		a.backups = append([]string{stamp}, a.backups...)
		s.mu.Unlock()
		say(0, "%s: backup %s (on the server and offsite)", name, stamp)
	case "restart":
		version := a.version
		index, kept := s.begin(a, "restart")
		s.mu.Unlock()
		out = io.MultiWriter(out, kept)
		sent := settings(body)
		ok := say(1200*time.Millisecond, "Starting %s %s", name, version) && say(600*time.Millisecond, "Waiting for /up")
		_, broken := sent.Env["FAIL"]
		if !ok || broken {
			code := fail("the app did not answer /up in 30 seconds. The previous container keeps the traffic")
			s.mu.Lock()
			a.end(index, "failed")
			s.mu.Unlock()
			return code
		}
		say(0, "Restarted %s", name)
		s.mu.Lock()
		a.state = "Up 1 second (healthy)"
		a.end(index, "succeeded")
		s.mu.Unlock()
	case "restore":
		index, kept := s.begin(a, "restore")
		s.mu.Unlock()
		out = io.MultiWriter(out, kept)
		which := strings.Join(args[1:], " ")
		if which == "" {
			which = "the newest backup"
		}
		ok := say(900*time.Millisecond, "Stopping %s", name) && say(900*time.Millisecond, "Restoring the databases from %s", which)
		if ok {
			say(0, "Restored %s from %s. The previous databases are in\n/var/matcha/%s/pre-restore-%s", name, which, name, time.Now().UTC().Format(stampLayout))
		}
		s.mu.Lock()
		a.end(index, map[bool]string{true: "succeeded", false: "failed"}[ok])
		s.mu.Unlock()
		if !ok {
			return 1
		}
	case "run":
		words := strings.Join(args[1:], " ")
		index, kept := s.begin(a, "run "+words)
		s.mu.Unlock()
		out = io.MultiWriter(out, kept)
		ok := len(args) > 1 && say(700*time.Millisecond, "== running in the container of %s: %s", name, words) && say(700*time.Millisecond, "(the mock server runs nothing: this is where the output of the command goes)")
		if strings.Contains(words, "false") || strings.Contains(words, "exit 1") {
			ok = false // a command that fails, to see how a failure looks
		}
		s.mu.Lock()
		a.end(index, map[bool]string{true: "succeeded", false: "failed"}[ok])
		s.mu.Unlock()
		if !ok {
			return 1
		}
	case "remove":
		s.apps = slices.DeleteFunc(s.apps, func(other *app) bool { return other == a })
		s.mu.Unlock()
		say(0, "Removed %s. The data and the backups stay in /var/matcha/%s", name, name)
	default:
		s.mu.Unlock()
		return fail("the mock server does not have the command %s", command)
	}
	return 0
}

func (s *Server) exists(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.app(name) != nil
}

// logs writes a request line now and then, until the client stops listening.
func (s *Server) logs(ctx context.Context, out io.Writer, name string) int {
	paths := []string{"GET /up 200 1ms", "GET / 200 12ms", "GET /products 200 18ms", "POST /cart 302 9ms", "GET /assets/app.css 304 0ms", "GET /checkout 200 21ms"}
	for i := 0; ; i++ {
		fmt.Fprintf(out, "%s %s %s\n", time.Now().UTC().Format("15:04:05"), name, paths[i%len(paths)])
		if !s.Wait(ctx, time.Duration(300+(i*377)%900)*time.Millisecond) {
			return 0
		}
	}
}

// deploy plays a deploy, a check, or the start of an addon: a pull, a backup,
// and a new version. An app with FAIL in its env does not get healthy.
func (s *Server) deploy(ctx context.Context, out io.Writer, command, name, version string, sent protocol.Settings) int {
	first := "Pulling " + sent.Image
	if sent.Image == "" {
		first = "Building the image of the website"
	}
	action := strings.TrimSpace(command + " " + version)

	s.mu.Lock()
	a := s.app(name)
	// A check changes nothing, and has no entry in the history.
	index := -1
	if command != "check" {
		if a == nil {
			a = &app{name: name, state: "not running", domains: []string{name + ".example.com"}}
			s.apps = append(s.apps, a)
			slices.SortFunc(s.apps, func(x, y *app) int { return strings.Compare(x.name, y.name) })
		}
		var kept io.Writer
		index, kept = s.begin(a, action)
		out = io.MultiWriter(out, kept)
	}
	hasData := a != nil && len(a.backups) > 0
	s.mu.Unlock()
	finish := func(result string) {
		if index >= 0 {
			s.mu.Lock()
			a.end(index, result)
			s.mu.Unlock()
		}
	}

	steps := []string{first, "Port 3000 (EXPOSE in the image). Health path /up. Storage /storage."}
	if hasData && command != "check" {
		steps = append(steps, name+": backup "+time.Now().UTC().Format(stampLayout)+" (on the server and offsite)")
	}
	steps = append(steps, "Starting "+name+" "+version, "Waiting for /up")
	for _, step := range steps {
		fmt.Fprintln(out, step)
		if !s.Wait(ctx, 1500*time.Millisecond) {
			finish("failed")
			return 1
		}
	}

	if _, broken := sent.Env["FAIL"]; broken {
		fmt.Fprintln(out, "Error: the new version did not answer /up in 30 seconds. The previous version keeps the traffic.\nThe app must listen on port 3000 and answer 200 on /up")
		finish("failed")
		return 1
	}
	if command == "check" {
		fmt.Fprintf(out, "%s %s follows the standard. Nothing live changed.\n", name, version)
		return 0
	}
	verb := "Deployed"
	if command == "enable" {
		verb = "Enabled"
	}
	s.mu.Lock()
	if hasData {
		a.backups = append([]string{time.Now().UTC().Format(stampLayout)}, a.backups...)
	}
	a.version, a.state = version, "Up 1 second (healthy)"
	domains := slices.Clone(a.domains)
	s.mu.Unlock()
	fmt.Fprintf(out, "\n%s %s %s\n", verb, name, version)
	for _, domain := range domains {
		fmt.Fprintf(out, "  https://%s\n", domain)
	}
	finish("succeeded")
	return 0
}
