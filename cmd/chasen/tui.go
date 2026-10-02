package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/term"
)

// The screen of `chasen` with no command: the apps of the server on the left,
// and one app on the right, with its state, its history, its backups, its
// domains, and its logs. Every part is the output of a protocol command, so
// the screen needs nothing from the server that the commands do not have.
//
// One loop owns all the state. Keys, the size of the window, and the answers
// of the server arrive as events. After each event the loop draws the whole
// screen again.

// The parts of an app, in the order of the tabs.
const (
	tabOverview = iota
	tabHistory
	tabBackups
	tabDomains
	tabLogs
)

var tabNames = []string{"overview", "history", "backups", "domains", "logs"}

// tabCommand is the protocol command that fills a tab.
var tabCommand = []string{"status", "history", "backups", "domains", "logs"}

// runner runs one protocol command and writes its output to out.
type runner func(ctx context.Context, out io.Writer, args ...string) (int, error)

type appRow struct {
	Name, Version, Domains string
}

// pane is the output of one command for one app.
type pane struct {
	lines  []string
	loaded bool
	err    string
}

// overlay covers the apps and the tabs: the output of an action while it
// runs, one history entry, the help, or the list of servers.
type overlay struct {
	title  string
	lines  []string
	scroll int
	follow bool // stay at the end while lines arrive
	// headings gives the lines that start at the edge the accent: the help.
	headings bool
	running  bool
	failed   bool
	cancel   context.CancelFunc
	// choices makes the overlay a list to pick from.
	choices []string
	cursor  int
	pick    func(int)
}

// prompt is a question in the last line: a yes or no, or a text to type.
type prompt struct {
	label string
	text  bool // false: y or n
	value string
	done  func(value string)
}

type tui struct {
	run     runner
	server  string // the name of the server, for the first line
	cwdApp  string // the app of the current directory, or ""
	servers []string
	change  func(server string) (runner, string, error) // go to another server

	width, height int
	apps          []appRow
	appsLoaded    bool
	appsErr       string
	selected      int
	tab           int
	inPane        bool // the keys go to the tab, not to the list of apps
	cursor        int  // the chosen row of the history, the backups, or the domains
	scroll        int
	panes         map[string]*pane
	logs          []string
	logsFor       string
	logsCancel    context.CancelFunc
	logsFollow    bool
	overlay       *overlay
	prompt        *prompt
	message       string // one line of news, until the next key
	update        string // a newer release of chasen, or ""
	// stats is the output of `chasen load`: how busy the server is.
	stats         []string
	noStats       bool // the server is older than the load command: do not ask again
	frame         int  // for the spinner
	events        chan any
	now           func() time.Time
	deployCommand func(ctx context.Context, out io.Writer) error
}

// The events of the loop.
type (
	keyEvent  string
	sizeEvent struct{ width, height int }
	tickEvent struct{}
	appsEvent struct {
		output string
		err    error
	}
	statsEvent struct {
		output string
		failed bool
	}
	paneEvent struct {
		app    string
		tab    int
		output string
		err    error
	}
	logEvent struct {
		app  string
		line string
	}
	actionLine struct {
		on   *overlay
		line string
	}
	actionDone struct {
		on  *overlay
		err error
	}
)

func newTUI(run runner, server, cwdApp string) *tui {
	return &tui{
		run: run, server: server, cwdApp: cwdApp,
		width: 80, height: 24,
		panes: map[string]*pane{}, logsFollow: true,
		events: make(chan any, 256), now: time.Now,
	}
}

// --- what the server says ---------------------------------------------------

var (
	columns = regexp.MustCompile(`\s{2,}`)
	// ansi matches the escape sequences of a terminal. The output of a server
	// must not move the cursor of this screen.
	ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b.`)
)

// clean makes one line of server output safe to show: no escape sequences, no
// control characters, and only the text after the last carriage return.
func clean(line string) string {
	line = strings.TrimRight(line, "\r\n")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	line = ansi.ReplaceAllString(line, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, line)
}

func cleanLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		lines = append(lines, clean(line))
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// parseApps reads the table of `chasen list`: NAME, VERSION, DOMAINS.
func parseApps(output string) []appRow {
	var apps []appRow
	for i, line := range cleanLines(output) {
		cells := columns.Split(strings.TrimSpace(line), 3)
		if i == 0 || cells[0] == "" {
			continue
		}
		app := appRow{Name: cells[0]}
		switch {
		case len(cells) == 3:
			app.Version, app.Domains = cells[1], cells[2]
		case len(cells) == 2 && strings.Contains(cells[1], "."):
			app.Domains = cells[1] // an app with no version yet
		case len(cells) == 2:
			app.Version = cells[1]
		}
		apps = append(apps, app)
	}
	return apps
}

// statusValue returns the values of one name in the output of `chasen status`:
// "URL:      https://shop.example.com".
func statusValues(lines []string, name string) []string {
	var values []string
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, name+":"); ok {
			values = append(values, strings.TrimSpace(rest))
		}
	}
	return values
}

const stampLayout = "20060102T150405Z"

// ago says how long ago a moment was, in words a person uses.
func ago(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// stampText shows the name of a backup as a time: "Oct 1 12:00 UTC · 3 h ago".
func (t *tui) stampText(stamp string) string {
	at, err := time.Parse(stampLayout, stamp)
	if err != nil {
		return stamp
	}
	return at.Format("Jan 2 15:04 UTC") + " · " + ago(t.now(), at)
}

// --- the events --------------------------------------------------------------

func (t *tui) app() string {
	if t.selected < len(t.apps) {
		return t.apps[t.selected].Name
	}
	return ""
}

func paneKey(app string, tab int) string { return fmt.Sprintf("%s\x00%d", app, tab) }

func (t *tui) pane(app string, tab int) *pane {
	p := t.panes[paneKey(app, tab)]
	if p == nil {
		p = &pane{}
		t.panes[paneKey(app, tab)] = p
	}
	return p
}

// rows returns the lines of the current tab that the cursor can choose.
func (t *tui) rows() []string {
	lines := t.pane(t.app(), t.tab).lines
	switch t.tab {
	case tabHistory:
		if len(lines) > 0 && strings.HasPrefix(lines[0], "ID") {
			return lines[1:]
		}
	case tabBackups:
		if len(lines) == 1 && lines[0] == "No backups." {
			return nil
		}
	case tabOverview, tabLogs:
		return nil
	}
	return lines
}

func (t *tui) loadApps() {
	run := t.run
	go func() {
		var out strings.Builder
		_, err := run(context.Background(), &out, "list")
		t.events <- appsEvent{out.String(), err}
	}()
	if t.noStats {
		return
	}
	go func() {
		var out strings.Builder
		code, err := run(context.Background(), &out, "load")
		t.events <- statsEvent{out.String(), err != nil || code != 0}
	}()
}

// load asks the server for one tab of one app. The logs are a stream, and
// have their own way.
func (t *tui) load(app string, tab int) {
	if app == "" || tab == tabLogs {
		return
	}
	run := t.run
	go func() {
		var out strings.Builder
		code, err := run(context.Background(), &out, tabCommand[tab], app)
		if err == nil && code != 0 {
			err = errors.New(strings.TrimSpace(out.String()))
		}
		t.events <- paneEvent{app, tab, out.String(), err}
	}()
}

// show loads what the screen shows now: the tab, and for the overview the
// history too.
func (t *tui) show() {
	app := t.app()
	t.load(app, t.tab)
	if t.tab == tabOverview {
		t.load(app, tabHistory)
	}
	if t.tab == tabLogs {
		t.followLogs(app)
	} else {
		t.stopLogs()
	}
}

func (t *tui) stopLogs() {
	if t.logsCancel != nil {
		t.logsCancel()
		t.logsCancel = nil
	}
	t.logsFor = ""
}

func (t *tui) followLogs(app string) {
	if app == "" || t.logsFor == app {
		return
	}
	t.stopLogs()
	ctx, cancel := context.WithCancel(context.Background())
	t.logsFor, t.logsCancel, t.logs, t.logsFollow, t.scroll = app, cancel, nil, true, 0
	run := t.run
	go func() {
		w := &lineWriter{emit: func(line string) { t.events <- logEvent{app, line} }}
		_, err := run(ctx, w, "logs", app)
		w.flush()
		if err != nil && ctx.Err() == nil {
			t.events <- logEvent{app, "The logs stopped: " + err.Error()}
		}
	}()
}

// lineWriter gives the output of a command to emit, one line at a time.
type lineWriter struct {
	rest []byte
	emit func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.rest = append(w.rest, p...)
	for {
		i := slices.Index(w.rest, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(clean(string(w.rest[:i])))
		w.rest = w.rest[i+1:]
	}
}

func (w *lineWriter) flush() {
	if len(w.rest) > 0 {
		w.emit(clean(string(w.rest)))
		w.rest = nil
	}
}

// act runs a command that changes something, and shows its output while it
// runs. When it ends, the screen loads the app again.
func (t *tui) act(title string, work func(ctx context.Context, out io.Writer) error) {
	ctx, cancel := context.WithCancel(context.Background())
	o := &overlay{title: title, running: true, follow: true, cancel: cancel}
	t.overlay = o
	go func() {
		w := &lineWriter{emit: func(line string) { t.events <- actionLine{o, line} }}
		err := work(ctx, w)
		w.flush()
		t.events <- actionDone{o, err}
	}()
}

// command is the work of act for one protocol command.
func (t *tui) command(args ...string) func(ctx context.Context, out io.Writer) error {
	run := t.run
	return func(ctx context.Context, out io.Writer) error {
		code, err := run(ctx, out, args...)
		if err == nil && code != 0 {
			err = fmt.Errorf("it failed, with exit code %d", code)
		}
		return err
	}
}

func (t *tui) confirm(question string, yes func()) {
	t.prompt = &prompt{label: question + " (y/n)", done: func(string) { yes() }}
}

// handle changes the state for one event. It returns false when the screen
// must close.
func (t *tui) handle(event any) bool {
	switch e := event.(type) {
	case sizeEvent:
		t.width, t.height = e.width, e.height
	case tickEvent:
		t.frame++
		// Every few seconds, ask again for what the screen shows.
		if t.frame%40 == 0 && t.overlay == nil && t.prompt == nil {
			t.loadApps()
			t.load(t.app(), t.tab)
		}
	case appsEvent:
		t.appsLoaded = true
		if e.err != nil {
			t.appsErr = e.err.Error()
			if errors.Is(e.err, protocol.ErrUnauthorized) {
				t.appsErr = "The server does not accept the token. Close this screen (q) and log in again: chasen add server <domain>, or chasen login for the cloud."
			}
			break
		}
		name := t.app()
		first := t.apps == nil
		t.apps, t.appsErr = parseApps(e.output), ""
		// Keep the same app chosen. At the start, choose the app of this directory.
		if first {
			name = t.cwdApp
		}
		t.selected = max(0, min(t.selected, len(t.apps)-1))
		for i, app := range t.apps {
			if app.Name == name {
				t.selected = i
			}
		}
		if first {
			t.show()
			for _, app := range t.apps {
				t.load(app.Name, tabOverview) // the state of each app, for its dot
			}
		}
	case statsEvent:
		// A server from before the load command answers with an error.
		t.noStats = e.failed
		t.stats = nil
		if !e.failed {
			t.stats = cleanLines(e.output)
		}
	case paneEvent:
		p := t.pane(e.app, e.tab)
		p.loaded, p.err = true, ""
		if e.err != nil {
			p.err = e.err.Error()
		} else {
			p.lines = cleanLines(e.output)
		}
		t.cursor = max(0, min(t.cursor, len(t.rows())-1))
	case logEvent:
		if e.app == t.logsFor {
			t.logs = append(t.logs, e.line)
			if len(t.logs) > 5000 {
				t.logs = t.logs[len(t.logs)-4000:]
			}
		}
	case actionLine:
		e.on.lines = append(e.on.lines, e.line)
	case actionDone:
		e.on.running = false
		if e.err != nil && !errors.Is(e.err, context.Canceled) {
			e.on.failed = true
			e.on.lines = append(e.on.lines, "", "Error: "+e.err.Error())
		}
		t.loadApps()
		t.panes = map[string]*pane{}
		for _, app := range t.apps {
			t.load(app.Name, tabOverview)
		}
		t.show()
	case keyEvent:
		return t.key(string(e))
	}
	return true
}

func (t *tui) key(key string) bool {
	t.message = ""
	if key == "ctrl+c" {
		return false
	}
	if p := t.prompt; p != nil {
		t.promptKey(p, key)
		return true
	}
	if o := t.overlay; o != nil {
		t.overlayKey(o, key)
		return true
	}

	switch key {
	case "q":
		return false
	case "?":
		t.overlay = &overlay{title: "keys", lines: helpLines, headings: true}
	case "g":
		t.loadApps()
		t.show()
		t.message = "Loaded again."
	case "s":
		t.chooseServer()
	case "left", "h":
		t.setTab(t.tab - 1)
	case "right", "l":
		t.setTab(t.tab + 1)
	case "1", "2", "3", "4", "5":
		t.setTab(int(key[0] - '1'))
	case "tab":
		t.inPane = !t.inPane && len(t.apps) > 0
	case "esc":
		t.inPane = false
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup":
		t.move(-t.pageSize())
	case "pgdn":
		t.move(t.pageSize())
	case "home":
		t.move(-1 << 30)
	case "end":
		t.move(1 << 30)
	case "enter":
		t.open()
	case "d":
		t.deploy()
	}
	if t.app() == "" {
		return true
	}
	app := t.app()
	switch key {
	case "r":
		t.confirm("Restart "+app+"? It starts again from the same image, with the same settings.", func() {
			t.act("restart "+app, t.command("restart", app))
		})
	case "b":
		t.act("backup "+app, t.command("backup", app))
	case "o":
		t.openInBrowser()
	case "a":
		if t.tab == tabDomains {
			t.prompt = &prompt{label: "Add a domain to " + app + ":", text: true, done: func(domain string) {
				if domain = strings.TrimSpace(domain); domain != "" {
					t.act("domains add "+domain, t.command("domains", app, "add", domain))
				}
			}}
		}
	case "x":
		if rows := t.rows(); t.tab == tabDomains && t.inPane && t.cursor < len(rows) {
			domain := strings.Fields(rows[t.cursor] + " ")[0]
			t.confirm("Remove the domain "+domain+" from "+app+"?", func() {
				t.act("domains rm "+domain, t.command("domains", app, "rm", domain))
			})
		}
	}
	return true
}

func (t *tui) setTab(tab int) {
	t.tab = (tab + len(tabNames)) % len(tabNames)
	t.cursor, t.scroll = 0, 0
	t.show()
}

// pageSize is how many lines of a tab the window shows.
func (t *tui) pageSize() int { return max(1, t.height-8) }

// move goes up or down: in the list of apps, in the rows of a tab, or in the
// text of a tab.
func (t *tui) move(by int) {
	switch {
	case !t.inPane:
		before := t.selected
		t.selected = max(0, min(t.selected+by, len(t.apps)-1))
		if t.selected != before {
			t.cursor, t.scroll = 0, 0
			t.show()
		}
	case t.tab == tabLogs:
		last := max(0, len(t.logs)-t.pageSize())
		if t.logsFollow {
			t.scroll = last
		}
		t.scroll = max(0, min(t.scroll+by, last))
		t.logsFollow = t.scroll >= last
	case len(t.rows()) > 0:
		t.cursor = max(0, min(t.cursor+by, len(t.rows())-1))
	default:
		t.scroll = max(0, t.scroll+by)
	}
}

// open acts on the chosen row: it shows a history entry, or restores a backup.
func (t *tui) open() {
	if !t.inPane {
		t.inPane = len(t.apps) > 0
		return
	}
	app, rows := t.app(), t.rows()
	if t.cursor >= len(rows) {
		return
	}
	first := strings.Fields(rows[t.cursor] + " ")[0]
	switch t.tab {
	case tabHistory:
		o := &overlay{title: "history " + app + " " + first, running: true}
		t.overlay = o
		run := t.run
		go func() {
			var out strings.Builder
			_, err := run(context.Background(), &out, "history", app, first)
			for _, line := range cleanLines(out.String()) {
				t.events <- actionLine{o, line}
			}
			t.events <- actionDone{o, err}
		}()
	case tabBackups:
		what := "the backup of " + t.stampText(first)
		if first == "live" {
			what = "the newest state of the live replica"
		}
		t.confirm("Restore "+app+" to "+what+"? The databases of now move aside.", func() {
			t.act("restore "+app+" "+first, t.command("restore", app, first))
		})
	}
}

// deploy runs `chasen deploy` for the app of this directory.
func (t *tui) deploy() {
	switch {
	case t.cwdApp == "" || t.deployCommand == nil:
		t.message = "A deploy runs in the directory of an app. This directory has no Dockerfile and no index.html."
	case t.app() != "" && t.app() != t.cwdApp:
		t.message = "This directory is the app " + t.cwdApp + ". To deploy " + t.app() + ", open chasen in its directory."
	default:
		t.confirm("Deploy "+t.cwdApp+" from this directory?", func() {
			t.act("deploy "+t.cwdApp, t.deployCommand)
		})
	}
}

func (t *tui) openInBrowser() {
	urls := statusValues(t.pane(t.app(), tabOverview).lines, "URL")
	if len(urls) == 0 {
		t.message = "This app has no URL yet."
		return
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := exec.Command(opener, urls[0]).Start(); err != nil {
		t.message = urls[0]
		return
	}
	t.message = "Opened " + urls[0]
}

func (t *tui) chooseServer() {
	if len(t.servers) < 2 || t.change == nil {
		t.message = "You are logged in to one server. Add another with: chasen add server <domain>"
		return
	}
	o := &overlay{title: "servers", choices: t.servers}
	o.cursor = max(0, slices.Index(t.servers, t.server))
	o.pick = func(i int) {
		run, name, err := t.change(t.servers[i])
		if err != nil {
			t.message = err.Error()
			return
		}
		t.stopLogs()
		t.run, t.server = run, name
		t.apps, t.appsLoaded, t.selected, t.panes = nil, false, 0, map[string]*pane{}
		t.stats, t.noStats = nil, false
		t.loadApps()
	}
	t.overlay = o
}

func (t *tui) promptKey(p *prompt, key string) {
	switch {
	case key == "esc" || (!p.text && (key == "n" || key == "q")):
		t.prompt = nil
	case !p.text && key == "y", p.text && key == "enter":
		t.prompt = nil
		p.done(p.value)
	case p.text && key == "backspace":
		if _, size := utf8.DecodeLastRuneInString(p.value); size > 0 {
			p.value = p.value[:len(p.value)-size]
		}
	case p.text && utf8.RuneCountInString(key) == 1:
		p.value += key
	}
}

func (t *tui) overlayKey(o *overlay, key string) {
	page := t.pageSize()
	last := max(0, len(o.lines)-page)
	if o.follow {
		o.scroll = last
	}
	switch key {
	case "esc", "q":
		if o.running && o.cancel != nil {
			t.message = "It still runs on the server. A deploy runs to its end there."
			o.cancel()
		}
		t.overlay = nil
		return
	case "enter":
		if o.pick != nil {
			t.overlay = nil
			o.pick(o.cursor)
			return
		}
		if !o.running {
			t.overlay = nil
			return
		}
	case "up", "k":
		o.cursor, o.scroll = max(0, o.cursor-1), o.scroll-1
	case "down", "j":
		o.cursor, o.scroll = min(len(o.choices)-1, o.cursor+1), o.scroll+1
	case "pgup":
		o.scroll -= page
	case "pgdn":
		o.scroll += page
	case "home":
		o.scroll = 0
	case "end":
		o.scroll = last
	}
	o.scroll = max(0, min(o.scroll, last))
	o.follow = o.scroll >= last
}

var helpLines = strings.Split(strings.TrimSpace(`
Move
  up, down (k, j)     the next app, the next row, or the next lines
  left, right (h, l)  the next tab. 1 to 5 go to a tab
  tab                 go from the apps to the tab, and back
  enter               open the row: the output of a history entry, or the restore of a backup
  esc                 go back

Change the app
  d    deploy the app of this directory
  r    restart the app, from the same image
  b    back up the app now
  a    add a domain (on the domains tab)
  x    remove the chosen domain (on the domains tab)
  o    open the app in the browser

The screen
  g    load everything again
  s    go to another server
  q    close

Each action is a command of the CLI: chasen deploy, chasen restart,
chasen backup, chasen restore, chasen domains. Run chasen help for all of them.

Something is wrong with Chasen? Close the screen and run: chasen report
`), "\n")

// --- the loop ---------------------------------------------------------------

// decodeKeys turns the bytes of a terminal into the names of keys.
func decodeKeys(input []byte) []string {
	named := map[string]string{
		"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
		"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
		"\x1b[5~": "pgup", "\x1b[6~": "pgdn", "\x1b[H": "home", "\x1b[F": "end",
		"\x1b[1~": "home", "\x1b[4~": "end", "\x1bOH": "home", "\x1bOF": "end",
	}
	var keys []string
	for len(input) > 0 {
		if input[0] == 0x1b && len(input) > 1 {
			found := false
			for sequence, name := range named {
				if strings.HasPrefix(string(input), sequence) {
					keys, input, found = append(keys, name), input[len(sequence):], true
					break
				}
			}
			if found {
				continue
			}
			// An escape sequence this screen does not use: skip all of it.
			end := 2
			for end < len(input) && !(input[end] >= 0x40 && input[end] <= 0x7e) {
				end++
			}
			input = input[min(end+1, len(input)):]
			continue
		}
		r, size := utf8.DecodeRune(input)
		input = input[size:]
		switch r {
		case 0x1b:
			keys = append(keys, "esc")
		case '\r', '\n':
			keys = append(keys, "enter")
		case '\t':
			keys = append(keys, "tab")
		case 0x7f, 0x08:
			keys = append(keys, "backspace")
		case 0x03:
			keys = append(keys, "ctrl+c")
		default:
			if r >= 0x20 && r != utf8.RuneError {
				keys = append(keys, string(r))
			}
		}
	}
	return keys
}

// serverScreen makes the screen for a server and the app of this directory.
func serverScreen(creds credentials, cwdApp string) *tui {
	client := func(creds credentials) runner {
		return func(ctx context.Context, out io.Writer, args ...string) (int, error) {
			return protocol.Client(creds).Run(ctx, args[0], args[1:], nil, out)
		}
	}
	t := newTUI(client(creds), serverName(creds.URL), cwdApp)

	saved := loadLogins()
	for address := range saved.Tokens {
		t.servers = append(t.servers, serverName(address))
	}
	slices.Sort(t.servers)
	t.change = func(server string) (runner, string, error) {
		for address, token := range saved.Tokens {
			if serverName(address) == server {
				return client(credentials{URL: address, Token: token}), server, nil
			}
		}
		return nil, "", errors.New("no login for " + server)
	}
	// A deploy is the same program, so it builds and pushes as `chasen deploy` does.
	if self, err := os.Executable(); err == nil && cwdApp != "" {
		t.deployCommand = func(ctx context.Context, out io.Writer) error {
			cmd := exec.CommandContext(ctx, self, "deploy")
			cmd.Stdout, cmd.Stderr = out, out
			cmd.Env = append(os.Environ(), "CHASEN_URL="+creds.URL, "CHASEN_TOKEN="+creds.Token)
			return cmd.Run()
		}
	}
	return t
}

// runScreen opens the screen in the terminal and stays until the user closes it.
func runScreen(t *tui) error {
	fd := int(os.Stdin.Fd())
	before, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	// The other screen of the terminal, with no cursor. On the way out, the
	// terminal is as it was.
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer func() {
		fmt.Print("\x1b[?25h\x1b[?1049l")
		term.Restore(fd, before)
	}()

	if width, height, err := term.GetSize(fd); err == nil {
		t.width, t.height = width, height
	}
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for range resized {
			if width, height, err := term.GetSize(fd); err == nil {
				t.events <- sizeEvent{width, height}
			}
		}
	}()
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				t.events <- keyEvent("ctrl+c")
				return
			}
			for _, key := range decodeKeys(buf[:n]) {
				t.events <- keyEvent(key)
			}
		}
	}()
	go func() {
		for range time.Tick(125 * time.Millisecond) {
			t.events <- tickEvent{}
		}
	}()

	t.loadApps()
	drawn := ""
	for {
		// The clock ticks eight times a second for the spinner. Most ticks
		// change nothing, and then nothing is written.
		if frame := t.view(); frame != drawn {
			io.WriteString(os.Stdout, frame)
			drawn = frame
		}
		select {
		case <-stopped:
			return nil
		case event := <-t.events:
			if !t.handle(event) {
				t.stopLogs()
				return nil
			}
		}
		// Many lines can arrive at once. Take them all, then draw one time.
		for more := true; more; {
			select {
			case event := <-t.events:
				if !t.handle(event) {
					t.stopLogs()
					return nil
				}
			default:
				more = false
			}
		}
	}
}

// serverName is the short name of an API address: example.com for
// https://api.example.com, and cloud for the Chasen cloud.
func serverName(address string) string {
	if address == apiAddress("cloud") {
		return "cloud"
	}
	name := strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
	return strings.TrimPrefix(name, "api.")
}
