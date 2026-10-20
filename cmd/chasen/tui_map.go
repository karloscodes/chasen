package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/karloscodes/chasen/protocol"
)

// The map is the first page of the screen. Each server is one block, drawn
// as what it is: the traffic that comes from the internet through the proxy
// to each app, the jobs beside an app, and where the data of the app goes:
// the hourly backups on the server, their copy in the bucket, and the live
// replica. Enter on an app opens the app, with its tabs; esc comes back.
//
// A line moves when its path is open: an app that answers behind the proxy,
// a live replica, a backup that runs. It does not count requests: the server
// does not know them. A broken line is red, and it does not move.
//
// The map is the output of three commands for each server: overview --json,
// load, and alerts. The same calls for a server, however many apps it has.

// serverMap is what one server said for the map.
type serverMap struct {
	apps    []appView
	stats   []string // the lines of chasen load
	alerts  []alertRow
	checked string // when the server looked for the alerts
	err     string
	loaded  bool
}

type mapEvent struct {
	server   string
	overview string
	load     string
	alerts   *string // nil when the alerts were not asked
	err      error
}

// mapServers are the servers of the map: every server with a login.
func (t *tui) mapServers() []string {
	if len(t.servers) > 1 && t.change != nil {
		return t.servers
	}
	return []string{t.server}
}

func (t *tui) runnerOf(server string) runner {
	if server == t.server {
		return t.run
	}
	if t.change == nil {
		return nil
	}
	run, _, err := t.change(server)
	if err != nil {
		return nil
	}
	return run
}

// loadMaps asks every server for its map. The asks to one server go one
// after the other, so they share its connection. The alerts change slowly:
// the map asks for them once a minute.
func (t *tui) loadMaps() {
	alerts := t.now().Sub(t.mapAlertsAt) >= time.Minute
	if alerts {
		t.mapAlertsAt = t.now()
	}
	for _, name := range t.mapServers() {
		run := t.runnerOf(name)
		if run == nil {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			e := mapEvent{server: name}
			var out strings.Builder
			code, err := run(ctx, &out, "overview", "--json")
			if err == nil && code != 0 {
				err = errors.New("it has no overview yet: chasen-server update")
			}
			if err != nil {
				e.err = err
				t.events <- e
				return
			}
			e.overview = out.String()
			out.Reset()
			if code, err := run(ctx, &out, "load"); err == nil && code == 0 {
				e.load = out.String()
			}
			if alerts {
				out.Reset()
				if code, err := run(ctx, &out, "alerts"); err == nil && code == 0 {
					text := out.String()
					e.alerts = &text
				}
			}
			t.events <- e
		}()
	}
}

func (t *tui) mapOf(server string) *serverMap {
	if t.maps == nil {
		t.maps = map[string]*serverMap{}
	}
	m := t.maps[server]
	if m == nil {
		m = &serverMap{}
		t.maps[server] = m
	}
	return m
}

// gotMap keeps what a server said. Every text from a server is cleaned of
// escape codes before the map draws it.
func (t *tui) gotMap(e mapEvent) {
	m := t.mapOf(e.server)
	m.loaded, m.err = true, ""
	if e.err != nil {
		m.err = cleanText(e.err.Error())
		if errors.Is(e.err, protocol.ErrUnauthorized) {
			m.err = "it does not accept the token. Log in again: chasen add server <user>@<host>"
		}
		return
	}
	var apps []appView
	if err := json.Unmarshal([]byte(strings.TrimSpace(e.overview)), &apps); err != nil {
		m.err = "its overview is not readable: " + err.Error()
		return
	}
	for i := range apps {
		a := &apps[i]
		a.App, a.State, a.Version, a.Running = clean(a.App), clean(a.State), clean(a.Version), clean(a.Running)
		a.Jobs, a.Backup, a.Replica = clean(a.Jobs), clean(a.Backup), clean(a.Replica)
		for j := range a.Domains {
			a.Domains[j] = clean(a.Domains[j])
		}
	}
	m.apps, m.stats = apps, cleanLines(e.load)
	if e.alerts != nil {
		m.alerts, m.checked = parseAlerts(*e.alerts)
	}
}

// mapRefs are the apps of the map, in the order of the drawing.
func (t *tui) mapRefs() []treeRef {
	var refs []treeRef
	for _, server := range t.mapServers() {
		if m := t.maps[server]; m != nil {
			for _, a := range m.apps {
				refs = append(refs, treeRef{server, a.App})
			}
		}
	}
	return refs
}

// mapChoice is the chosen app of the map. At the start, it is the app of the
// directory, or the first app.
func (t *tui) mapChoice() treeRef {
	refs := t.mapRefs()
	if slices.Contains(refs, t.mapChosen) {
		return t.mapChosen
	}
	if cwd := (treeRef{t.server, t.cwdApp}); slices.Contains(refs, cwd) {
		return cwd
	}
	if len(refs) > 0 {
		return refs[0]
	}
	return treeRef{}
}

func (t *tui) mapMove(by int) {
	refs := t.mapRefs()
	if len(refs) == 0 {
		return
	}
	at := max(0, slices.Index(refs, t.mapChoice()))
	t.mapChosen = refs[max(0, min(at+by, len(refs)-1))]
}

// mapRunning reports whether a change runs on a server of the map: then the
// map asks every 5 seconds, not every 10.
func (t *tui) mapRunning() bool {
	for _, m := range t.maps {
		for _, a := range m.apps {
			if a.Running != "" {
				return true
			}
		}
	}
	return false
}

// openFromMap opens the chosen app, with its tabs, on its server.
func (t *tui) openFromMap() {
	r := t.mapChoice()
	if r.app == "" {
		return
	}
	t.home, t.inPane = false, false
	if r.server != t.server {
		t.switchServer(r.server, r.app)
		return
	}
	t.want = r.app
	if i := slices.IndexFunc(t.apps, func(a appRow) bool { return a.Name == r.app }); i >= 0 {
		t.selected, t.cursor, t.scroll, t.filter = i, 0, 0, ""
		t.show()
	}
	t.loadApps()
}

// goHome leaves an app for the map, with that app chosen.
func (t *tui) goHome() {
	t.home = true
	t.stopLogs()
	if t.app() != "" {
		t.mapChosen = treeRef{t.server, t.app()}
	}
	t.loadMaps()
}

// mapKey acts on a key on the map.
func (t *tui) mapKey(key string) bool {
	chosen := t.mapChoice()
	switch key {
	case "q":
		return false
	case "?":
		t.overlay = &overlay{title: "keys", lines: helpLines, headings: true}
	case "t":
		name := nextTheme()
		list := themes()
		t.message = "Theme: " + name + ". Next with t: " + list[(slices.Index(list, name)+1)%len(list)] + "."
	case "g":
		t.loadMaps()
		t.message = "Loaded again."
	case "up", "k":
		t.mapMove(-1)
	case "down", "j":
		t.mapMove(1)
	case "pgup":
		t.mapMove(-5)
	case "pgdn":
		t.mapMove(5)
	case "home":
		t.mapMove(-1 << 30)
	case "end":
		t.mapMove(1 << 30)
	case "enter", "right", "l":
		t.openFromMap()
	case "o":
		domains := t.mapApp(chosen).Domains
		if len(domains) == 0 {
			t.message = "This app has no domain."
			break
		}
		page := "https://" + domains[0]
		if err := t.openURL(page); err != nil {
			t.message = page
			break
		}
		t.message = "Opened " + page
	case "!":
		if chosen.server == "" {
			break
		}
		server := chosen.server
		t.overlay = &overlay{title: alertsTitle + " of " + server, draw: func(width int) []string {
			m := t.mapOf(server)
			return t.alertLines(m.alerts, m.checked, width)
		}}
	}
	return true
}

func (t *tui) mapApp(r treeRef) appView {
	if m := t.maps[r.server]; m != nil {
		for _, a := range m.apps {
			if a.App == r.app {
				return a
			}
		}
	}
	return appView{}
}

// --- the drawing ---------------------------------------------------------------

// mapLines draws every server, with the chosen app in view, and a note on
// what the lines mean at the bottom when there is room.
func (t *tui) mapLines(width, height int) []string {
	loaded := false
	for _, m := range t.maps {
		loaded = loaded || m.loaded
	}
	if !loaded {
		t.hit = hits{}
		return t.splash(width, height)
	}
	chosen := t.mapChoice()
	var lines []string
	var refs []treeRef
	at := -1
	for i, server := range t.mapServers() {
		if i > 0 {
			lines, refs = append(lines, ""), append(refs, treeRef{})
		}
		block, blockRefs := t.serverBlock(server, width, chosen)
		for j, r := range blockRefs {
			if at < 0 && r == chosen && r.app != "" {
				at = len(lines) + j
			}
		}
		lines, refs = append(lines, block...), append(refs, blockRefs...)
	}
	note := spread(width, []cell{{" A moving line is an open path, or data on its way. It does not count requests.", colorDim}}, nil)
	if len(lines)+2 <= height {
		for len(lines) < height-1 {
			lines, refs = append(lines, ""), append(refs, treeRef{})
		}
		lines, refs = append(lines, note), append(refs, treeRef{})
	}
	start, end := window(len(lines), height, max(0, at))
	// The body starts at the third line of the terminal.
	t.hit = hits{mapTop: 3, mapRefs: refs[start:end]}
	return lines[start:end]
}

var (
	coresAndCPU = regexp.MustCompile(`\((\d+) cores, (\w+)\)`)
	memoryTotal = regexp.MustCompile(` of ([0-9.]+ GB)`)
)

// hardware is the machine in a few words: "amd64 · 4 cores · 7.6 GB".
func hardware(stats []string) string {
	var parts []string
	if m := coresAndCPU.FindStringSubmatch(strings.Join(statusValues(stats, "Load"), "")); m != nil {
		parts = append(parts, m[2], m[1]+" cores")
	}
	if m := memoryTotal.FindStringSubmatch(strings.Join(statusValues(stats, "Memory"), "")); m != nil {
		parts = append(parts, m[1])
	}
	return strings.Join(parts, " · ")
}

// serverBlock draws one server: its name and how busy it is, then one branch
// of the proxy for each app, and where its API answers. It returns what each
// line is, for the keys and the mouse.
func (t *tui) serverBlock(server string, width int, chosen treeRef) ([]string, []treeRef) {
	m := t.mapOf(server)
	right := append(statCellsOf(m.stats), alertCellsOf(m.alerts)...)
	machine := "   " + hardware(m.stats)
	used := 1 + utf8.RuneCountInString(server) + utf8.RuneCountInString(machine)
	for _, c := range right {
		used += utf8.RuneCountInString(c.text)
	}
	if used > width {
		machine = "" // a narrow window keeps the numbers that move
	}
	header := spread(width, []cell{{" " + server, colorBold}, {machine, colorDim}}, right)
	lines, refs := []string{header}, []treeRef{{server: server}}
	add := func(line string, r treeRef) {
		lines, refs = append(lines, line), append(refs, r)
	}
	switch {
	case !m.loaded:
		add(spread(width, []cell{{"   " + spinner[t.frame%len(spinner)] + " ", colorAccent}, {"Asking " + server, colorDim}}, nil), treeRef{server: server})
		return lines, refs
	case m.err != "":
		add(spread(width, []cell{{"   It does not answer: " + m.err, colorBad}}, nil), treeRef{server: server})
		return lines, refs
	case len(m.apps) == 0:
		add(spread(width, []cell{{"   No apps yet. In the directory of an app, run: ", colorDim}, {"chasen deploy", colorAccent}}, nil), treeRef{server: server})
		return lines, refs
	}

	api := strings.Join(statusValues(m.stats, "API"), "")
	bucket := strings.Join(statusValues(m.stats, "Bucket"), "")
	if strings.HasPrefix(bucket, "none") {
		bucket = ""
	}
	apiOnWeb := strings.HasPrefix(api, "https://")
	branches := len(m.apps)
	if apiOnWeb {
		branches++
	}
	l := mapLayout(width, m.apps)

	for i, a := range m.apps {
		r := treeRef{server, a.App}
		phase := i * 3
		last := i == branches-1
		live := a.Up && len(a.Domains) > 0

		first := t.branch(i, last, branches, l, phase, live, !a.Up)
		first = append(first, t.appCells(a, r == chosen, l.name, l.what)...)
		first = append(first, cell{"  ", ""})
		first = append(first, t.dataCells(a, bucket, phase, l.compact)...)
		add(spread(width, first, nil), r)

		under := []cell{{strings.Repeat(" ", l.trunk), ""}, {"┃", colorDim}, {strings.Repeat(" ", l.segment+1), ""}}
		if last {
			under[1].text = " "
		}
		domains := strings.Join(a.Domains, " ")
		if domains == "" {
			domains = "no domain"
		}
		second := append(slices.Clone(under), cell{"     ", ""}, cell{fit(domains, l.name+2+l.what), colorDim}, cell{"  ", ""})
		second = append(second, t.replicaCells(a, bucket, phase, l.compact)...)
		add(spread(width, second, nil), r)
		// The jobs run beside the web, from the same image: a line of their own.
		if a.Jobs != "" {
			dot := cell{"● ", colorAccent}
			if !strings.HasPrefix(a.Jobs, "Up") {
				dot.color = colorBad
			}
			add(spread(width, append(slices.Clone(under), cell{"     └ ", colorDim}, cell{"jobs  ", ""}, dot, cell{a.Jobs, colorDim}), nil), r)
		}
	}

	if apiOnWeb {
		branch := t.branch(len(m.apps), true, branches, l, len(m.apps)*3, true, false)
		branch = append(branch, cell{"   ◆ ", colorAccent}, cell{fit("API", l.name+2), colorBold}, cell{api, colorAccent})
		add(spread(width, branch, nil), treeRef{server: server})
	} else if api != "" {
		add(spread(width, []cell{{" you ", colorDim}, {strings.Repeat("─", l.segment) + " ssh " + strings.Repeat("─", l.segment) + "▶", colorDim}, {" API   " + api, colorDim}}, nil), treeRef{server: server})
	}
	return lines, refs
}

// layout is the room of the columns of a server, for the width of the window.
// A window under 100 columns gets the compact one: shorter lines and words.
type layout struct {
	compact bool
	segment int // the length of a line of traffic
	name    int // the names of the apps
	what    int // the version of an app, or what runs on it now
	trunk   int // the column of the trunk of the proxy
}

func mapLayout(width int, apps []appView) layout {
	l := layout{segment: 4, name: 6, what: 16}
	if width < 100 {
		l = layout{compact: true, segment: 2, name: 6, what: 14}
	}
	for _, a := range apps {
		l.name = max(l.name, utf8.RuneCountInString(a.App))
	}
	l.name = min(l.name, 18)
	// Under the ┳ of " internet ━━━━▶ proxy ━┳": the columns before it.
	l.trunk = utf8.RuneCountInString(" internet ▶ proxy ━") + l.segment
	return l
}

// branch draws the traffic to one app: from the internet through the proxy
// for the first, and from the trunk of the proxy for the others. All end in
// the same column.
func (t *tui) branch(i int, last bool, branches int, l layout, phase int, live, broken bool) []cell {
	var cells []cell
	if i == 0 {
		cells = append(cells, cell{" internet ", colorDim})
		cells = append(cells, t.flowCells(l.segment, phase, "━", live || broken, false)...)
		cells = append(cells, cell{"▶", arrowColor(live || broken, false)}, cell{" proxy ", colorBold})
		joint := "━━"
		if branches > 1 {
			joint = "━┳"
		}
		cells = append(cells, cell{joint, colorDim})
	} else {
		joint := "┣"
		if last {
			joint = "┗"
		}
		cells = append(cells, cell{strings.Repeat(" ", l.trunk), ""}, cell{joint, colorDim})
	}
	cells = append(cells, t.flowCells(l.segment, phase, "━", live, broken)...)
	return append(cells, cell{"▶", arrowColor(live, broken)})
}

func arrowColor(live, broken bool) string {
	switch {
	case broken:
		return colorBad
	case live:
		return colorAccent
	}
	return colorDim
}

// flowCells draws a line of length characters. A live line has a dot that
// moves along it, one step a frame. A broken line is red, with a cross in its
// middle. Any other line is dim and still.
func (t *tui) flowCells(length, phase int, base string, live, broken bool) []cell {
	switch {
	case broken:
		half := length / 2
		return []cell{{strings.Repeat(base, half) + "╳" + strings.Repeat(base, max(0, length-half-1)), colorBad}}
	case !live:
		return []cell{{strings.Repeat(base, length), colorDim}}
	}
	// Two frames with no dot between two passes, so the eye sees it start again.
	at := (t.frame + phase) % (length + 2)
	if at >= length {
		return []cell{{strings.Repeat(base, length), colorDim}}
	}
	return []cell{{strings.Repeat(base, at), colorDim}, {"•", colorAccent + ";" + colorBold}, {strings.Repeat(base, length-at-1), colorDim}}
}

// appCells draws an app at the end of its branch: the marker of the chosen
// app, its state, its name, and its version or what runs on it now.
func (t *tui) appCells(a appView, chosen bool, name, what int) []cell {
	marker := cell{"   ", ""}
	nameColor := ""
	if chosen {
		marker, nameColor = cell{" ▸ ", colorAccent}, colorBold
	}
	dot := cell{"● ", colorAccent}
	about := cell{fit(shortVersion(a.Version), what), colorDim}
	switch {
	case a.Running != "":
		dot = cell{spinner[t.frame%len(spinner)] + " ", colorAccent}
		about = cell{fit(a.Running, what), colorAccent}
	case !a.Up:
		dot = cell{"✗ ", colorBad}
		about = cell{fit(a.State, what), colorBad}
	}
	return []cell{marker, dot, {fit(a.App, name), nameColor}, {"  ", ""}, about}
}

// shortVersion is a commit in 7 characters, like git shows it.
func shortVersion(version string) string {
	if len(version) > 7 && strings.Trim(version, "0123456789abcdef") == "" {
		return version[:7]
	}
	return version
}

// dataCells draws where the data of an app goes each hour: its databases,
// the backup on the server, and its copy in the bucket. The line moves while
// a backup runs.
func (t *tui) dataCells(a appView, bucket string, phase int, compact bool) []cell {
	switch {
	case a.Replica == "":
		return []cell{{"the data shows after the next update of the server", colorDim}}
	case a.Backup == "off":
		return []cell{{"backups off", colorDim}}
	case a.Backup == "":
		return []cell{{"no backup yet", colorDim}}
	}
	running := strings.HasPrefix(a.Running, "backup")
	line, age := 3, cell{fit("backup "+t.stampAge(a.Backup), 18), ""}
	if compact {
		line, age = 2, cell{fit(t.stampAge(a.Backup), 11), ""}
	}
	if at, err := time.Parse(stampLayout, a.Backup); err == nil && t.now().Sub(at) > 2*time.Hour {
		age.color = colorBad
	}
	cells := []cell{{fit(strconv.Itoa(a.Databases)+" db", 5) + " ", colorDim}}
	cells = append(cells, t.flowCells(line, phase, "─", running, false)...)
	cells = append(cells, cell{"▶ ", arrowColor(running, false)}, age, cell{" ", ""})
	if bucket == "" {
		return append(cells, cell{"this server only", colorDim})
	}
	cells = append(cells, t.flowCells(line, phase+1, "─", running, false)...)
	return append(cells, cell{"▶ ", arrowColor(running, false)}, cell{bucket, ""})
}

// replicaCells draws the live replica: each change of the databases on its
// way to the bucket, about a second behind.
func (t *tui) replicaCells(a appView, bucket string, phase int, compact bool) []cell {
	pad := cell{strings.Repeat(" ", 6), ""}
	bucket = cmp.Or(bucket, "the bucket")
	line, label := 3, fit("live replica", 18)
	if compact {
		line, label = 2, fit("replica", 11)
	}
	switch a.Replica {
	case "live":
		cells := []cell{pad}
		cells = append(cells, t.flowCells(line, phase, "·", true, false)...)
		cells = append(cells, cell{"▶ ", colorAccent}, cell{label, ""}, cell{" ", ""})
		cells = append(cells, t.flowCells(line, phase+1, "·", true, false)...)
		return append(cells, cell{"▶ ", colorAccent}, cell{bucket, ""})
	case "behind":
		cells := []cell{pad}
		cells = append(cells, t.flowCells(line, phase, "·", false, true)...)
		return append(cells, cell{"▶ ", colorBad}, cell{"live replica behind: chasen verify", colorBad})
	}
	return nil
}

// stampAge is how long ago a backup was made: "12 min ago".
func (t *tui) stampAge(stamp string) string {
	at, err := time.Parse(stampLayout, stamp)
	if err != nil {
		return stamp
	}
	return ago(t.now(), at)
}

// mapFirstLine is the first line on the map: the name, and how many servers
// and apps it shows.
func (t *tui) mapFirstLine(width int) string {
	servers, apps := len(t.mapServers()), len(t.mapRefs())
	count := plural(servers, "server") + " · " + plural(apps, "app") + " "
	right := []cell{{count, colorDim}}
	if t.update != "" && 100 < width {
		right = append([]cell{{t.update + " installs when you quit    ", colorAccent}}, right...)
	}
	return spread(width, []cell{{" chasen", colorAccent + ";" + colorBold}}, right)
}
