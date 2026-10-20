package main

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The map shows a server as what it is: the traffic that comes from the
// internet through the proxy to each app, the jobs beside an app, and where
// the data of the app goes: the hourly backups on the server, their copy in
// the bucket, and the live replica. It is what the right side shows while
// the server, not one of its apps, is chosen on the left. It only shows:
// the keys stay on the left.
//
// A line moves when its path is open: an app that answers behind the proxy,
// a live replica, a backup that runs. It does not count requests: the server
// does not know them. A broken line is red, and it does not move.
//
// It draws what the screen asks the server for already: overview --json,
// load, and alerts. Nothing more.

// mapServers are the servers of the list on the left: every server with a
// login, or this one.
func (t *tui) mapServers() []string {
	if len(t.servers) > 1 && t.change != nil {
		return t.servers
	}
	return []string{t.server}
}

// mapTitle is the first line of the right side on the map: its name, and the
// command that prints the same.
func (t *tui) mapTitle(width int) string {
	right := []cell{{"$ chasen overview ", colorDim}}
	if 30+utf8.RuneCountInString(t.server) > width {
		right = nil
	}
	return spread(width, []cell{{"map", colorAccent + ";" + colorBold + ";4"}, {"   " + t.server, colorDim}}, right)
}

// --- the drawing ---------------------------------------------------------------

// mapLines draws the map of the server, and at the bottom what the lines
// mean, when there is room.
func (t *tui) mapLines(width, height int) []string {
	lines := t.serverBlock(width)
	if len(lines)+2 <= height {
		for len(lines) < height-1 {
			lines = append(lines, "")
		}
		lines = append(lines, spread(width, []cell{{"A moving line is an open path, or data on its way. It does not count requests.", colorDim}}, nil))
	}
	return lines[:min(len(lines), height)]
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

// serverBlock draws the server: its machine, then one branch of the proxy for
// each app, and where its API answers.
func (t *tui) serverBlock(width int) []string {
	// The title has the name, and the corner and the first line how busy the
	// server is and its alerts: here, the machine.
	header := spread(width, []cell{{cmp.Or(hardware(t.stats), t.server), colorDim}}, nil)
	lines := []string{header}
	add := func(line string) { lines = append(lines, line) }
	apps := t.overview
	switch {
	case t.noOverview:
		add(spread(width, []cell{{"This server has no map yet: its next update brings it.", colorDim}}, nil))
		return lines
	case !t.overviewLoaded:
		add(spread(width, []cell{{spinner[t.frame%len(spinner)] + " ", colorAccent}, {"Asking " + t.server, colorDim}}, nil))
		return lines
	case len(apps) == 0:
		add(spread(width, []cell{{"No apps yet. In the directory of an app, run: ", colorDim}, {"chasen deploy", colorAccent}}, nil))
		return lines
	}

	api := strings.Join(statusValues(t.stats, "API"), "")
	bucket := strings.Join(statusValues(t.stats, "Bucket"), "")
	if strings.HasPrefix(bucket, "none") {
		bucket = ""
	}
	apiOnWeb := strings.HasPrefix(api, "https://")
	branches := len(apps)
	if apiOnWeb {
		branches++
	}
	l := mapLayout(width, apps)

	for i, a := range apps {
		phase := i * 3
		last := i == branches-1
		live := a.Up && len(a.Domains) > 0

		first := t.branch(i, last, branches, l, phase, live, !a.Up)
		first = append(first, t.appCells(a, l.name, l.what)...)
		first = append(first, cell{"  ", ""})
		first = append(first, t.dataCells(a, bucket, phase, l.compact)...)
		add(spread(width, first, nil))

		under := []cell{{strings.Repeat(" ", l.trunk), ""}, {"┃", colorDim}, {strings.Repeat(" ", l.segment+1), ""}}
		if last {
			under[1].text = " "
		}
		domains := strings.Join(a.Domains, " ")
		if domains == "" {
			domains = "no domain"
		}
		second := append(slices.Clone(under), cell{"   ", ""}, cell{fit(domains, l.name+2+l.what), colorDim}, cell{"  ", ""})
		second = append(second, t.replicaCells(a, bucket, phase, l.compact)...)
		add(spread(width, second, nil))
		// The jobs run beside the web, from the same image: a line of their own.
		if a.Jobs != "" {
			dot := cell{"● ", colorAccent}
			if !strings.HasPrefix(a.Jobs, "Up") {
				dot.color = colorBad
			}
			add(spread(width, append(slices.Clone(under), cell{"   └ ", colorDim}, cell{"jobs  ", ""}, dot, cell{a.Jobs, colorDim}), nil))
		}
	}

	if apiOnWeb {
		branch := t.branch(len(apps), true, branches, l, len(apps)*3, true, false)
		branch = append(branch, cell{" ◆ ", colorAccent}, cell{fit("API", l.name+2), colorBold}, cell{api, colorAccent})
		add(spread(width, branch, nil))
	} else if api != "" {
		add(spread(width, []cell{{"you ", colorDim}, {strings.Repeat("─", l.segment) + " ssh " + strings.Repeat("─", l.segment) + "▶", colorDim}, {" API   " + api, colorDim}}, nil))
	}
	return lines
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
	// Under the ┳ of "internet ━━━━▶ proxy ━┳": the columns before it.
	l.trunk = utf8.RuneCountInString("internet ▶ proxy ━") + l.segment
	return l
}

// branch draws the traffic to one app: from the internet through the proxy
// for the first, and from the trunk of the proxy for the others. All end in
// the same column.
func (t *tui) branch(i int, last bool, branches int, l layout, phase int, live, broken bool) []cell {
	var cells []cell
	if i == 0 {
		cells = append(cells, cell{"internet ", colorDim})
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

// appCells draws an app at the end of its branch: its state, its name, and
// its version or what runs on it now.
func (t *tui) appCells(a appView, name, what int) []cell {
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
	return []cell{{" ", ""}, dot, {fit(a.App, name), colorBold}, {"  ", ""}, about}
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
