package main

import (
	"cmp"
	"context"
	"encoding/json"
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

// The screen of `chasen` with no command. It is for watching and running a
// server: the apps on the left, and one app on the right, with its state, its
// history, its backups, its domains, and its logs. It does not deploy: a
// deploy needs the directory of the app, and the screen is about the server. Every part is the output of a protocol command, so
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

// paneRunning is the pane with the output of the entry that runs now. It is
// not a tab: the overview shows it.
const paneRunning = 5

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
	title string
	// command is the line of the CLI that does the same, or a note about it.
	command string
	lines   []string
	scroll  int
	follow  bool // stay at the end while lines arrive
	// headings gives the lines that start at the edge the accent: the help.
	headings bool
	running  bool
	failed   bool
	cancel   context.CancelFunc
	// live is the history entry that the overlay follows while it runs:
	// the app and the id.
	live [2]string
	// draw makes the lines of an overlay that paints its own, for the width of
	// the window, in place of lines.
	draw func(width int) []string
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
	// For a text: change runs after each key, and cancel when esc ends it.
	change func(value string)
	cancel func()
}

// hits is where the things that the mouse can choose are on the screen, in
// the lines and columns of the terminal, from 1. The drawing fills it in.
type hits struct {
	left     int       // the width of the side of the apps
	appTop   int       // the line of the first app that shows
	appFirst int       // which app that is
	tabs     [][2]int  // the first and the last column of each tab name
	rowTop   int       // the line of the first row of the tab
	rowFirst int       // which row that is
	tree     []treeRef // with several servers: what each line of the list is, from appTop
}

// treeRef is a line of the list of servers and apps: a server, or an app of it.
type treeRef struct{ server, app string }

type tui struct {
	run     runner
	server  string // the name of the server, for the first line
	cwdApp  string // the app of the current directory, or ""
	servers []string
	change  func(server string) (runner, string, error) // go to another server
	// tree has the apps of the other servers, for the list on the left: the
	// screen shows every server with its apps, and the keys go from one to
	// the next. The apps of the current server are apps.
	tree   map[string][]appRow
	listed bool   // the current server answered with its apps
	want   string // the app to choose when the apps of a new server arrive

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
	hit           hits                 // where the last drawing put the things that a click can choose
	filter        string               // what / narrows the rows or the logs of the tab to
	watching      map[string]string    // the id of the history entry that runs now, by app
	finished      map[string]string    // the id of an entry that ended, until its history says how
	changes       map[string][2]string // from the overview: the id and the action that run now, by app
	noOverview    bool                 // the server is older than the overview command: read the histories
	openURL       func(page string) error
	message       string // one line of news, until the next key
	update        string // a newer release of chasen, or ""
	// stats is the output of `chasen load`: how busy the server is.
	stats   []string
	noStats bool // the server is older than the load command: do not ask again
	// alerts is what `chasen alerts` said last: what is wrong with the
	// server, or puts it at risk. The screen asks again every minute.
	alerts        []alertRow
	alertsChecked string // the last line: when the server looked
	noAlerts      bool   // the server, or the cloud, has no alerts command: do not ask again
	frame         int    // for the spinner
	events        chan any
	now           func() time.Time
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
	overviewEvent struct {
		server string
		output string
		failed bool
	}
	statsEvent struct {
		output string
		failed bool
	}
	treeEvent struct {
		server string
		output string
		err    error
	}
	alertsEvent struct {
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
	// liveEvent is the output so far of the entry that an overlay follows.
	liveEvent struct {
		on     *overlay
		output string
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
		panes: map[string]*pane{}, logsFollow: true, watching: map[string]string{}, finished: map[string]string{}, openURL: openURL,
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

// parseApps reads the table of `chasen list`: NAME, VERSION, DOMAINS. The
// cloud puts the server of each app in a first column, SERVER. The screen
// leaves it out.
func parseApps(output string) []appRow {
	var apps []appRow
	lines := cleanLines(output)
	withServer := len(lines) > 0 && strings.HasPrefix(lines[0], "SERVER")
	for i, line := range lines {
		cells := columns.Split(strings.TrimSpace(line), 4)
		if withServer {
			cells = cells[min(1, len(cells)):]
		} else if len(cells) == 4 {
			cells = append(cells[:2], cells[2]+"  "+cells[3])
		}
		if i == 0 || len(cells) == 0 || cells[0] == "" {
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

// rows returns the lines of the current tab that the cursor can choose: all
// of them, or those that the filter keeps.
func (t *tui) rows() []string {
	lines := t.pane(t.app(), t.tab).lines
	switch t.tab {
	case tabHistory:
		if len(lines) > 0 && strings.HasPrefix(lines[0], "ID") {
			lines = lines[1:]
		}
	case tabBackups:
		if len(lines) == 1 && lines[0] == "No backups." {
			return nil
		}
	case tabOverview, tabLogs:
		return nil
	}
	return t.filtered(lines)
}

// filtered returns the lines that have the text of the filter, in any case.
func (t *tui) filtered(lines []string) []string {
	if t.filter == "" {
		return lines
	}
	want := strings.ToLower(t.filter)
	var kept []string
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), want) {
			kept = append(kept, line)
		}
	}
	return kept
}

// running returns the history entry of an app that runs now: its id and its
// action. A deploy from another terminal, or from CI, shows up here. The
// overview says it for all apps in one call; a server from before it says it
// in the history of each app.
func (t *tui) running(app string) (id, action string) {
	if t.changes != nil {
		now := t.changes[app]
		return now[0], now[1]
	}
	lines := t.pane(app, tabHistory).lines
	for _, line := range lines[min(1, len(lines)):] {
		if parts := columns.Split(strings.TrimSpace(line), 4); len(parts) == 4 && parts[3] == "running" {
			return parts[0], parts[2]
		}
	}
	return "", ""
}

// commandLine is the line of the CLI that does what the screen does with
// these arguments of the protocol. It works from any directory.
func commandLine(args ...string) string {
	if len(args) < 2 || args[0] == "enable" {
		return "chasen " + strings.Join(args, " ")
	}
	return "chasen -a " + args[1] + " " + args[0] + strings.TrimRight(" "+strings.Join(args[2:], " "), " ")
}

// loadApps asks for all that the screen shows of the server: the apps with
// their domains, what poll asks, and the alerts and the tree. These change
// slowly: the screen asks for them once a minute.
func (t *tui) loadApps() {
	t.listApps()
	if !t.noOverview {
		t.askOverview()
	}
	t.loadStats()
	t.loadAlerts()
	t.loadTree()
}

func (t *tui) listApps() {
	run := t.run
	go func() {
		var out strings.Builder
		_, err := run(context.Background(), &out, "list")
		t.events <- appsEvent{out.String(), err}
	}()
}

// poll asks the current server what runs on its apps, and how busy it is:
// two calls, however many apps it has. A server from before the overview
// has the list of apps in place of it.
func (t *tui) poll() {
	if t.noOverview {
		t.listApps()
	} else {
		t.askOverview()
	}
	t.loadStats()
}

func (t *tui) loadStats() {
	run := t.run
	if t.noStats {
		return
	}
	go func() {
		var out strings.Builder
		code, err := run(context.Background(), &out, "load")
		t.events <- statsEvent{out.String(), err != nil || code != 0}
	}()
}

// loadTree asks each other server for its apps, for the tree on the left.
func (t *tui) loadTree() {
	if len(t.servers) < 2 || t.change == nil {
		return
	}
	for _, name := range t.servers {
		if name == t.server {
			continue
		}
		run, _, err := t.change(name)
		if err != nil {
			continue
		}
		go func() {
			var out strings.Builder
			_, err := run(context.Background(), &out, "list")
			t.events <- treeEvent{name, out.String(), err}
		}()
	}
}

// switchServer makes another server the current one, with app chosen, or
// its first app. The apps it had are in the tree, so the list does not
// empty while the server answers.
func (t *tui) switchServer(name, app string) {
	run, name, err := t.change(name)
	if err != nil {
		t.message = err.Error()
		return
	}
	if t.tree == nil {
		t.tree = map[string][]appRow{}
	}
	t.tree[t.server] = t.apps
	t.stopLogs()
	t.run, t.server = run, name
	t.panes, t.stats, t.noStats = map[string]*pane{}, nil, false
	t.changes, t.noOverview, t.watching, t.finished = nil, false, map[string]string{}, map[string]string{}
	t.alerts, t.alertsChecked, t.noAlerts = nil, "", false
	t.apps, t.appsLoaded, t.listed, t.selected = t.tree[name], len(t.tree[name]) > 0, false, 0
	t.cursor, t.scroll, t.filter, t.want = 0, 0, "", app
	for i, a := range t.apps {
		if a.Name == app {
			t.selected = i
		}
	}
	if t.appsLoaded {
		t.show()
	}
	t.loadApps()
}

// crossServer goes from the first or the last app of a server to the next
// server that has apps, in the direction of by. It reports whether it went.
func (t *tui) crossServer(by int) bool {
	if len(t.servers) < 2 || t.change == nil {
		return false
	}
	step := 1
	if by < 0 {
		step = -1
	}
	for i := slices.Index(t.servers, t.server) + step; i >= 0 && i < len(t.servers); i += step {
		apps := t.tree[t.servers[i]]
		if len(apps) == 0 {
			continue
		}
		app := apps[0].Name
		if step < 0 {
			app = apps[len(apps)-1].Name
		}
		t.switchServer(t.servers[i], app)
		return true
	}
	return false
}

// sameApps reports whether the overview and the list have the same apps.
func sameApps(overview []appView, list []appRow) bool {
	var a, b []string
	for _, app := range overview {
		a = append(a, app.App)
	}
	for _, app := range list {
		b = append(b, app.Name)
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// askOverview asks the server for the state of all its apps and what runs on
// them, in one call.
func (t *tui) askOverview() {
	run, server := t.run, t.server
	go func() {
		var out strings.Builder
		code, err := run(context.Background(), &out, "overview", "--json")
		t.events <- overviewEvent{server, out.String(), err != nil || code != 0}
	}()
}

// alertsTitle is the title of the overlay of the alerts.
const alertsTitle = "alerts"

var checkedAt = regexp.MustCompile(`Checked at (\d\d:\d\d UTC)\.(?: Quiet: ([^.]+)\.)?`)

// alertView draws the alerts: when the server looked, then each alert with a
// dot of its color, what is wrong, and the fix under it, wrapped to the window.
func (t *tui) alertView(width int) []string {
	note := "Refreshes every minute. The same: chasen alerts"
	if m := checkedAt.FindStringSubmatch(t.alertsChecked); m != nil {
		note = "Checked at " + m[1] + ", refreshes every minute. The same: chasen alerts"
		if m[2] != "" {
			note += ". Turned off: " + m[2]
		}
	}
	lines := []string{" " + paint(note, colorDim), ""}
	if t.alertsChecked == "" {
		return append(lines, " "+paint(spinner[t.frame%len(spinner)], colorAccent)+paint(" Asking the server", colorDim))
	}
	if len(t.alerts) == 0 {
		return append(lines, " "+paint("✓", colorAccent)+" No alerts. Nothing is wrong, and nothing puts the server at risk.")
	}
	const indent = "             "
	for _, a := range t.alerts {
		level, color := "warning", colorAccent
		if a.error {
			level, color = "error  ", colorBad
		}
		what := wrap(a.what, width-len(indent)-2)
		lines = append(lines, " "+paint("●", color)+" "+paint(level, color)+"   "+paint(what[0], colorBold))
		for _, more := range what[1:] {
			lines = append(lines, indent+paint(more, colorBold))
		}
		for _, line := range wrap(a.fix, width-len(indent)-2) {
			lines = append(lines, indent+paint(line, colorDim))
		}
		lines = append(lines, "")
	}
	return lines
}

// loadAlerts asks the server what is wrong with it.
func (t *tui) loadAlerts() {
	if t.noAlerts {
		return
	}
	run := t.run
	go func() {
		var out strings.Builder
		code, err := run(context.Background(), &out, "alerts")
		t.events <- alertsEvent{out.String(), err != nil || code != 0}
	}()
}

// alertRow is one alert: its level, what is wrong, and the fix.
type alertRow struct {
	error     bool
	what, fix string
}

// parseAlerts reads the output of chasen alerts: a line with the level and
// what is wrong, then an indented line with the fix, and at the end the time
// of the check.
func parseAlerts(output string) (alerts []alertRow, checked string) {
	for _, line := range cleanLines(output) {
		switch {
		case strings.HasPrefix(line, "ERROR "), strings.HasPrefix(line, "WARNING "):
			level, what, _ := strings.Cut(line, " ")
			alerts = append(alerts, alertRow{level == "ERROR", strings.TrimSpace(what), ""})
		case strings.HasPrefix(line, "         ") && len(alerts) > 0:
			alerts[len(alerts)-1].fix = strings.TrimSpace(line)
		case strings.TrimSpace(line) != "":
			checked = strings.TrimSpace(line)
		}
	}
	return alerts, checked
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

// act runs a protocol command that changes something, and shows its output
// while it runs, under the line of the CLI that does the same. When it ends,
// the screen loads the app again.
func (t *tui) act(args ...string) {
	ctx, cancel := context.WithCancel(context.Background())
	o := &overlay{title: strings.Join(args, " "), command: commandLine(args...), running: true, follow: true, cancel: cancel}
	if args[0] == "restart" {
		// The restart of the CLI also sends the chasen.yml of the directory.
		o.command = "chasen restart, in the directory of " + args[1] + ", also applies its chasen.yml. This one keeps the settings of the last deploy."
	}
	t.overlay = o
	run := t.run
	go func() {
		w := &lineWriter{emit: func(line string) { t.events <- actionLine{o, line} }}
		code, err := run(ctx, w, args...)
		if err == nil && code != 0 {
			err = fmt.Errorf("it failed, with exit code %d", code)
		}
		w.flush()
		t.events <- actionDone{o, err}
	}()
}

// watch notes if an app has an entry that runs. When the entry ends, the
// state of the app is new: the screen asks for it again and says what ended.
func (t *tui) watch(app string) {
	id, _ := t.running(app)
	before := t.watching[app]
	t.watching[app] = id
	if before == "" || before == id {
		return
	}
	// The history says how it ended.
	t.finished[app] = before
	t.load(app, tabHistory)
	delete(t.panes, paneKey(app, paneRunning))
	t.load(app, tabOverview)
	t.loadApps()
}

// tellFinished says how the entry that ended went, when the history of its
// app has it.
func (t *tui) tellFinished(app string) {
	id := t.finished[app]
	if id == "" {
		return
	}
	for _, line := range t.pane(app, tabHistory).lines {
		if parts := columns.Split(strings.TrimSpace(line), 4); len(parts) == 4 && parts[0] == id && parts[3] != "running" {
			t.message = app + ": " + parts[2] + " " + parts[3]
			delete(t.finished, app)
		}
	}
}

// follow asks again for what runs: the history of each app with a running
// entry, the output of the one of the chosen app, and the entry that an
// overlay shows.
func (t *tui) follow() {
	asked := false
	for app, id := range t.watching {
		if id == "" {
			continue
		}
		// One overview tells when any of them ends. A server from before it
		// tells in the history of each app.
		if t.noOverview {
			t.load(app, tabHistory)
		} else if !asked {
			t.askOverview()
			asked = true
		}
		if app == t.app() {
			run := t.run
			go func() {
				var out strings.Builder
				_, err := run(context.Background(), &out, "history", app, id)
				t.events <- paneEvent{app, paneRunning, out.String(), err}
			}()
		}
	}
	if o := t.overlay; o != nil && o.live[1] != "" {
		app, id := o.live[0], o.live[1]
		if t.watching[app] != id {
			o.running, o.live = false, [2]string{}
		}
		run := t.run
		go func() {
			var out strings.Builder
			if _, err := run(context.Background(), &out, "history", app, id); err == nil {
				t.events <- liveEvent{o, out.String()}
			}
		}()
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
		// Every 10 seconds, ask again for what the screen shows, and for
		// what runs: a deploy from somewhere else shows up in the overview. A
		// server from before it shows it in the history of each app. Every
		// minute, ask for what changes slowly too.
		if t.frame%80 == 0 && t.overlay == nil && t.prompt == nil {
			if t.frame%480 == 0 {
				t.loadApps()
			} else {
				t.poll()
			}
			t.load(t.app(), t.tab)
			if t.noOverview {
				for _, app := range t.apps[:min(len(t.apps), 12)] {
					t.load(app.Name, tabHistory)
				}
			}
		}
		// Every second, follow what runs now.
		if t.frame%8 == 0 {
			t.follow()
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
		first := !t.listed
		t.listed = true
		t.apps, t.appsErr = parseApps(e.output), ""
		// Keep the same app chosen. At the start, choose the app of this
		// directory; after a switch, the app that the keys went to.
		if first {
			name = cmp.Or(t.want, t.cwdApp)
			t.want = ""
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
	case treeEvent:
		if e.err == nil && e.server != t.server {
			if t.tree == nil {
				t.tree = map[string][]appRow{}
			}
			t.tree[e.server] = parseApps(e.output)
		}
	case overviewEvent:
		if e.server != t.server {
			break
		}
		// A server from before the overview command, or the cloud, answers with an error.
		var apps []appView
		if e.failed || json.Unmarshal([]byte(strings.TrimSpace(e.output)), &apps) != nil {
			t.noOverview, t.changes = true, nil
			break
		}
		changes := map[string][2]string{}
		for _, a := range apps {
			if a.Running != "" {
				changes[a.App] = [2]string{fmt.Sprint(a.ID), a.Running}
			}
			// The overview of v0.8.6 and v0.8.7 has no id: read the histories.
			if a.Running != "" && a.ID == 0 {
				changes = nil
				break
			}
		}
		if changes == nil {
			t.noOverview, t.changes = true, nil
			break
		}
		t.changes = changes
		// An app that is new, or gone: ask for the list with its domains now.
		if !sameApps(apps, t.apps) {
			t.listApps()
		}
		for _, a := range apps {
			t.watch(a.App)
		}
		// An entry that ended before the overview: none of its app runs now.
		for app := range t.watching {
			if !slices.ContainsFunc(apps, func(a appView) bool { return a.App == app }) {
				t.watch(app)
			}
		}
	case alertsEvent:
		// A server from before the alerts command, or the cloud, answers with an error.
		t.noAlerts = e.failed
		t.alerts, t.alertsChecked = nil, ""
		if !e.failed {
			t.alerts, t.alertsChecked = parseAlerts(e.output)
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
		if e.tab == tabHistory && e.err == nil {
			t.watch(e.app)
			t.tellFinished(e.app)
		}
	case logEvent:
		if e.app == t.logsFor {
			t.logs = append(t.logs, e.line)
			if len(t.logs) > 5000 {
				t.logs = t.logs[len(t.logs)-4000:]
			}
		}
	case actionLine:
		e.on.lines = append(e.on.lines, e.line)
	case liveEvent:
		e.on.lines = cleanLines(e.output)
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
	if rest, ok := strings.CutPrefix(key, "mouse:"); ok {
		var button, x, y int
		if _, err := fmt.Sscanf(rest, "%d:%d:%d", &button, &x, &y); err == nil {
			t.mouse(button, x, y)
		}
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
	case "t":
		name := nextTheme()
		list := themes()
		t.message = "Theme: " + name + ". Next with t: " + list[(slices.Index(list, name)+1)%len(list)] + "."
	case "!":
		if t.noAlerts {
			t.message = "This server has no alerts. chasen update, and the next update of the server, bring them."
		} else {
			t.overlay = &overlay{title: alertsTitle, draw: t.alertView}
		}
	case "g":
		t.loadApps()
		t.show()
		t.message = "Loaded again."
	case "s":
		t.chooseServer()
	// The screen has two sides. Left and right go to a side, up and down move
	// in it. The tabs have their own keys.
	case "esc":
		// Esc takes one thing away at a time: the filter, then the side.
		if t.filter != "" {
			t.filter, t.cursor, t.scroll = "", 0, 0
		} else {
			t.inPane = false
		}
	case "left", "h":
		t.inPane = false
	case "/":
		t.startFilter()
	case ":":
		t.prompt = &prompt{label: ":", text: true, done: t.runLine}
	case "right", "l":
		t.inPane = len(t.apps) > 0
	case "tab":
		t.setTab(t.tab + 1)
	case "shift+tab":
		t.setTab(t.tab - 1)
	case "1", "2", "3", "4", "5":
		t.setTab(int(key[0] - '1'))
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
	}
	if t.app() == "" {
		return true
	}
	app := t.app()
	switch key {
	case "r":
		t.confirm("Restart "+app+"? It starts again from the same image, with the same settings.", func() {
			t.act("restart", app)
		})
	case "b":
		t.act("backup", app)
	case "o":
		t.openInBrowser()
	case "a":
		if t.tab == tabDomains {
			t.prompt = &prompt{label: "Add a domain to " + app + ":", text: true, done: func(domain string) {
				if domain = strings.TrimSpace(domain); domain != "" {
					t.act("domains", app, "add", domain)
				}
			}}
		}
	case "x":
		if rows := t.rows(); t.tab == tabDomains && t.inPane && t.cursor < len(rows) {
			domain := strings.Fields(rows[t.cursor] + " ")[0]
			t.confirm("Remove the domain "+domain+" from "+app+"?", func() {
				t.act("domains", app, "rm", domain)
			})
		}
	}
	return true
}

func (t *tui) setTab(tab int) {
	t.tab = (tab + len(tabNames)) % len(tabNames)
	t.cursor, t.scroll, t.filter = 0, 0, ""
	// Who picks a tab wants to be in it.
	t.inPane = len(t.apps) > 0
	t.show()
}

// pageSize is how many lines of a tab the window shows.
func (t *tui) pageSize() int { return max(1, t.height-8) }

// move goes up or down: in the list of apps, in the rows of a tab, or in the
// text of a tab.
func (t *tui) move(by int) {
	switch {
	case !t.inPane:
		if next := t.selected + by; (next < 0 || next >= len(t.apps)) && t.crossServer(by) {
			return
		}
		before := t.selected
		t.selected = max(0, min(t.selected+by, len(t.apps)-1))
		if t.selected != before {
			t.cursor, t.scroll, t.filter = 0, 0, ""
			t.show()
		}
	case t.tab == tabLogs:
		last := max(0, len(t.filtered(t.logs))-t.pageSize())
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
		o := &overlay{title: "history " + app + " " + first, command: commandLine("history", app, first), running: true}
		if t.watching[app] == first {
			// The entry still runs: the overlay follows it to its end.
			o.live, o.follow = [2]string{app, first}, true
		}
		t.overlay = o
		run := t.run
		go func() {
			var out strings.Builder
			_, err := run(context.Background(), &out, "history", app, first)
			t.events <- liveEvent{o, out.String()}
			if o.live[1] == "" {
				t.events <- actionDone{o, err}
			}
		}()
	case tabBackups:
		what := "the backup of " + t.stampText(first)
		if first == "live" {
			what = "the newest state of the live replica"
		}
		t.confirm("Restore "+app+" to "+what+"? The databases of now move aside.", func() {
			t.act("restore", app, first)
		})
	}
}

// The commands that the : line runs with no question: they change nothing.
var harmless = []string{"list", "load", "status", "history", "backups", "logs", "backup"}

// runLine runs what the user typed after the colon: a command of the CLI,
// for the chosen app. ":restore live" is `chasen -a shop restore live`.
func (t *tui) runLine(line string) {
	words := strings.Fields(line)
	if len(words) > 1 && words[0] == "chasen" {
		words = words[1:] // someone typed "chasen restore live"
	}
	if len(words) == 0 {
		return
	}
	command := words[0]
	switch {
	case command == "ssh" || command == "download":
		t.message = "Run it in a terminal of its own: chasen -a " + t.app() + " " + command
		return
	case !slices.Contains(protocol.Commands, command):
		t.message = "chasen has no command " + command + ". The commands: " + strings.Join(protocol.Commands, ", ")
		return
	case command == "deploy" || command == "check":
		t.message = "A " + command + " needs the directory of the app. Run there: chasen " + command
		return
	}
	args := []string{command}
	switch {
	case command == "list" || command == "load" || command == "enable":
		args = append(args, words[1:]...) // these have no app
	case t.app() == "":
		t.message = "No app is chosen."
		return
	default:
		args = append(append(args, t.app()), words[1:]...)
	}
	if slices.Contains(harmless, command) || (command == "domains" && len(words) == 1) {
		t.act(args...)
		return
	}
	t.confirm("Run "+commandLine(args...)+"?", func() { t.act(args...) })
}

// mouse acts on a click or on the wheel. A click chooses what is under it:
// an app, a tab, or a row. A click on the chosen row opens it, like enter.
// The wheel moves in the side that it is over.
func (t *tui) mouse(button, x, y int) {
	const left, wheelUp, wheelDown = 0, 64, 65
	if t.prompt != nil {
		return
	}
	if o := t.overlay; o != nil {
		switch {
		case button == wheelUp:
			t.overlayKey(o, "up")
		case button == wheelDown:
			t.overlayKey(o, "down")
		case button == left && o.pick != nil && y >= 5 && y-5 < len(o.choices):
			t.overlay = nil
			o.pick(y - 5)
		}
		return
	}
	onApps := x <= t.hit.left
	switch button {
	case wheelUp, wheelDown:
		by := map[bool]int{true: 1, false: 3}[onApps]
		if button == wheelUp {
			by = -by
		}
		t.inPane = !onApps && len(t.apps) > 0
		t.move(by)
	case left:
		for tab, columns := range t.hit.tabs {
			if y == 3 && x >= columns[0] && x <= columns[1] {
				t.setTab(tab)
				return
			}
		}
		if row := y - t.hit.appTop; onApps && len(t.hit.tree) > 0 && row >= 0 && row < len(t.hit.tree) {
			r := t.hit.tree[row]
			t.inPane = false
			switch {
			case r.server != t.server:
				t.switchServer(r.server, r.app)
			case r.app != "":
				t.move(slices.IndexFunc(t.apps, func(a appRow) bool { return a.Name == r.app }) - t.selected)
			}
			return
		}
		if app := t.hit.appFirst + y - t.hit.appTop; onApps && y >= t.hit.appTop && app < len(t.apps) {
			t.inPane = false
			t.move(app - t.selected)
			return
		}
		if row := t.hit.rowFirst + y - t.hit.rowTop; !onApps && t.hit.rowTop > 0 && y >= t.hit.rowTop && row < len(t.rows()) {
			again := t.inPane && row == t.cursor
			t.inPane, t.cursor = true, row
			if again {
				t.open()
			}
		}
	}
}

// startFilter opens the / line. The rows, or the logs, narrow with each key.
func (t *tui) startFilter() {
	if t.tab == tabOverview || t.app() == "" {
		t.message = "Nothing to filter here. / works on the history, the backups, the domains, and the logs."
		return
	}
	t.inPane = true
	before := t.filter
	set := func(value string) { t.filter, t.cursor, t.scroll, t.logsFollow = value, 0, 0, true }
	t.prompt = &prompt{label: "/", text: true, value: t.filter, change: set, done: set, cancel: func() { set(before) }}
}

// reportFailure opens a new issue with the command that failed and the end
// of its output. The page is in the browser: nothing is sent before the user
// reads it and sends it.
func (t *tui) reportFailure(o *overlay) {
	lines := o.lines[max(0, len(o.lines)-25):]
	what := "```\n$ " + o.title + "\n" + strings.Join(lines, "\n") + "\n```"
	page := reportURL(what)
	if err := t.openURL(page); err != nil {
		t.message = "Open this page to report it: " + page
		return
	}
	t.message = "A new issue is open in the browser, with the command and its output. Read it, then send it."
}

func (t *tui) openInBrowser() {
	urls := statusValues(t.pane(t.app(), tabOverview).lines, "URL")
	if len(urls) == 0 {
		t.message = "This app has no URL yet."
		return
	}
	if err := t.openURL(urls[0]); err != nil {
		t.message = urls[0]
		return
	}
	t.message = "Opened " + urls[0]
}

// openURL opens a page in the browser of the machine.
func openURL(page string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, page).Start()
}

func (t *tui) chooseServer() {
	if len(t.servers) < 2 || t.change == nil {
		t.message = "You are logged in to one server. Add another with: chasen add server <user>@<host>"
		return
	}
	o := &overlay{title: "servers", choices: t.servers}
	o.cursor = max(0, slices.Index(t.servers, t.server))
	o.pick = func(i int) { t.switchServer(t.servers[i], "") }
	t.overlay = o
}

func (t *tui) promptKey(p *prompt, key string) {
	before := p.value
	switch {
	case key == "esc" || (!p.text && (key == "n" || key == "q")):
		t.prompt = nil
		if p.cancel != nil {
			p.cancel()
		}
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
	if p.change != nil && p.value != before {
		p.change(p.value)
	}
}

func (t *tui) overlayKey(o *overlay, key string) {
	page := t.pageSize()
	last := max(0, len(o.lines)-page)
	if o.follow {
		o.scroll = last
	}
	switch key {
	case "!":
		if o.failed {
			t.reportFailure(o)
		}
		return
	case "esc", "q":
		if o.running && o.cancel != nil {
			t.message = "It still runs on the server, to its end."
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
  left, right (h, l)  go to the apps on the left, or to the tab on the right
  up, down (k, j)     move in the side you are on: the next app, the next row, or the next lines
  tab, shift+tab      the next tab, the tab before. 1 to 5 go to a tab
  enter               open the row: the output of a history entry, or the restore of a backup
  /                   narrow the rows, or the logs, to what you type. esc takes it away
  :                   run a command of the CLI for the chosen app, like :restore live
  the mouse           a click chooses an app, a tab, or a row. A click on the chosen row
                      opens it. The wheel scrolls. Hold shift to select text

Change the app
  r    restart the app, from the same image
  b    back up the app now
  a    add a domain (on the domains tab)
  x    remove the chosen domain (on the domains tab)
  o    open the app in the browser

The screen
  g    load everything again
  !    the alerts of the server: what is wrong, and what puts it at risk.
       They refresh every minute
  t    the next theme: the amber of Chasen, monochrome, or the colors of
       your terminal, which follow Omarchy. The screen remembers it
  s    go to another server
  q    close

Each action is a command of the CLI: chasen restart, chasen backup,
chasen restore, chasen domains. A deploy is not here: it belongs to the
directory of the app. Run chasen deploy there, and watch it arrive here.

A deploy that runs somewhere else shows up by itself: in the list of the
apps, in the overview, and in the history. Enter on it follows its output.

Something is wrong with Chasen? When an action fails, ! opens a report with
its output. Or close the screen and run: chasen report
`), "\n")

// --- the loop ---------------------------------------------------------------

// decodeKeys turns the bytes of a terminal into the names of keys.
func decodeKeys(input []byte) []string {
	named := map[string]string{
		"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
		"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
		"\x1b[5~": "pgup", "\x1b[6~": "pgdn", "\x1b[H": "home", "\x1b[F": "end",
		"\x1b[1~": "home", "\x1b[4~": "end", "\x1bOH": "home", "\x1bOF": "end",
		"\x1b[Z": "shift+tab",
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
			// A click or the wheel: ESC [ < button ; column ; line, then M for
			// a press and m for a release. Only the press counts.
			if rest, ok := strings.CutPrefix(string(input), "\x1b[<"); ok {
				if end := strings.IndexAny(rest, "Mm"); end >= 0 {
					var button, x, y int
					if _, err := fmt.Sscanf(rest[:end], "%d;%d;%d", &button, &x, &y); err == nil && rest[end] == 'M' {
						keys = append(keys, fmt.Sprintf("mouse:%d:%d:%d", button, x, y))
					}
					input = input[len("\x1b[<")+end+1:]
					continue
				}
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
	return t
}

// runScreen opens the screen in the terminal and stays until the user closes it.
func runScreen(t *tui) error {
	followTheme()
	fd := int(os.Stdin.Fd())
	before, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	// The other screen of the terminal, with no cursor. On the way out, the
	// terminal is as it was.
	// The terminal also reports the clicks and the wheel of the mouse.
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	defer func() {
		fmt.Print("\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l")
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
