package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The drawing of the screen. Every line is built from cells of plain text
// that are cut to their width first and get their color after, so the escape
// codes never count as width.

// The colors: one accent, the amber of the Chasen mark, one red for what is
// wrong, and one gray for what matters less. NO_COLOR turns them off.
var (
	colorAccent = "38;5;214"
	colorDim    = "38;5;245"
	colorBad    = "38;5;203"
	colorBold   = "1"
	colorChosen = "7" // the chosen row: the colors change places
)

var colorsOn = os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"

func init() {
	if c := os.Getenv("COLORTERM"); c == "truecolor" || c == "24bit" {
		colorAccent = "38;2;245;184;61"
	}
}

func paint(text, color string) string {
	if !colorsOn || color == "" || text == "" {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

// clip cuts text to a width, with an ellipsis when something is left out.
func clip(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= width {
		return text
	}
	runes := []rune(text)
	return string(runes[:width-1]) + "…"
}

// fit cuts text to a width and fills the rest with spaces.
func fit(text string, width int) string {
	text = clip(text, width)
	return text + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(text)))
}

// cell is a piece of a line with one color.
type cell struct{ text, color string }

// spread puts the left cells at the start of a line and the right cells at
// its end. When the line is too short, the left side is cut.
func spread(width int, left, right []cell) string {
	rightWidth := 0
	for _, c := range right {
		rightWidth += utf8.RuneCountInString(c.text)
	}
	if rightWidth > width {
		right, rightWidth = nil, 0
	}
	var line strings.Builder
	room := width - rightWidth
	for _, c := range left {
		text := clip(c.text, room)
		room -= utf8.RuneCountInString(text)
		line.WriteString(paint(text, c.color))
	}
	line.WriteString(strings.Repeat(" ", max(0, room)))
	for _, c := range right {
		line.WriteString(paint(c.text, c.color))
	}
	return line.String()
}

// wrap breaks a text into lines of a width, at the spaces.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines, line = append(lines, line), ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}

// window returns the part of the lines that a height shows, with the chosen
// line in view.
func window(count, height, chosen int) (start, end int) {
	start = max(0, min(chosen-height/2, count-height))
	return start, min(count, start+height)
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// view draws the whole screen: a first line, the body, and a last line with
// the keys.
func (t *tui) view() string {
	width, height := t.width, t.height
	var lines []string
	if width < 60 || height < 12 {
		lines = []string{"", " chasen needs a window of 60 columns and 12 lines.", " Make the window larger, or press q."}
	} else {
		rule := paint(strings.Repeat("─", width), colorDim)
		lines = append(lines, t.firstLine(width), rule)
		lines = append(lines, t.body(width, height-4)...)
		lines = append(lines, rule, t.lastLine(width))
	}

	var screen strings.Builder
	screen.WriteString("\x1b[H")
	for i, line := range lines {
		if i > 0 {
			screen.WriteString("\r\n")
		}
		screen.WriteString(line + "\x1b[K")
	}
	screen.WriteString("\x1b[J")
	return screen.String()
}

func (t *tui) firstLine(width int) string {
	count := ""
	switch {
	case t.appsLoaded && len(t.apps) == 1:
		count = "1 app "
	case t.appsLoaded:
		count = itoa(len(t.apps)) + " apps "
	}
	left := []cell{{" chasen", colorAccent + ";" + colorBold}, {"  " + t.server, ""}}
	// In the corner: how busy the server is, then the count of the apps. A
	// narrow window drops the notice of a new release first, then the numbers.
	right := []cell{{count, colorDim}}
	if stats := t.statCells(); !t.cornerFits() && utf8.RuneCountInString(t.server)+60 < width {
		right = append(stats, right...)
	}
	if t.update != "" && utf8.RuneCountInString(t.server)+100 < width {
		right = append([]cell{{t.update + " is out: chasen update    ", colorAccent}}, right...)
	}
	return spread(width, left, right)
}

var (
	percent  = regexp.MustCompile(`\((\d+)%\)`)
	loadLine = regexp.MustCompile(`^([0-9.]+) .*\((\d+) cores\)`)
)

// statCells shows the load, the memory, and the disk of the server in a few
// characters: "load 0.42  mem 31%  disk 41%". A number that needs attention
// is red: a load above the count of the cores, or 90% and more in use.
func (t *tui) statCells() []cell {
	var cells []cell
	if parts := loadLine.FindStringSubmatch(strings.Join(statusValues(t.stats, "Load"), "")); parts != nil {
		color := colorDim
		load, _ := strconv.ParseFloat(parts[1], 64)
		if cores, _ := strconv.Atoi(parts[2]); cores > 0 && load > float64(cores) {
			color = colorBad
		}
		cells = append(cells, cell{"load ", colorDim}, cell{parts[1] + "  ", color})
	}
	for _, name := range []string{"Memory", "Disk"} {
		parts := percent.FindStringSubmatch(strings.Join(statusValues(t.stats, name), ""))
		if parts == nil {
			continue
		}
		color := colorDim
		if used, _ := strconv.Atoi(parts[1]); used >= 90 {
			color = colorBad
		}
		label := map[string]string{"Memory": "mem ", "Disk": "disk "}[name]
		cells = append(cells, cell{label, colorDim}, cell{parts[1] + "%  ", color})
	}
	return cells
}

func itoa(n int) string {
	digits := ""
	for {
		digits = string(rune('0'+n%10)) + digits
		if n /= 10; n == 0 {
			return digits
		}
	}
}

// keys returns the keys that work now, most useful first.
func (t *tui) keys() [][2]string {
	switch {
	case t.overlay != nil && t.overlay.pick != nil:
		return [][2]string{{"↑↓", "choose"}, {"enter", "go"}, {"esc", "back"}}
	case t.overlay != nil:
		return [][2]string{{"↑↓", "scroll"}, {"esc", "close"}}
	case len(t.apps) == 0:
		return [][2]string{{"g", "load again"}, {"s", "servers"}, {"?", "keys"}, {"q", "close"}}
	case !t.inPane:
		return [][2]string{{"↑↓", "app"}, {"→", "its " + tabNames[t.tab]}, {"tab", "next tab"}, {"r", "restart"}, {"b", "backup"}, {"o", "open"}, {"?", "keys"}, {"q", "close"}}
	}
	switch t.tab {
	case tabHistory:
		return [][2]string{{"↑↓", "entry"}, {"enter", "its output"}, {"←", "apps"}, {"tab", "next tab"}, {"?", "keys"}}
	case tabBackups:
		return [][2]string{{"↑↓", "backup"}, {"enter", "restore"}, {"b", "back up now"}, {"←", "apps"}, {"tab", "next tab"}}
	case tabDomains:
		return [][2]string{{"↑↓", "domain"}, {"a", "add"}, {"x", "remove"}, {"←", "apps"}, {"tab", "next tab"}}
	case tabLogs:
		return [][2]string{{"↑↓", "scroll"}, {"end", "follow"}, {"←", "apps"}, {"tab", "next tab"}}
	}
	return [][2]string{{"↑↓", "scroll"}, {"←", "apps"}, {"tab", "next tab"}, {"r", "restart"}, {"b", "backup"}, {"o", "open"}}
}

func (t *tui) lastLine(width int) string {
	if p := t.prompt; p != nil {
		cells := []cell{{" " + p.label, colorAccent}}
		if p.text {
			cells = append(cells, cell{" " + p.value, ""}, cell{"▏", colorAccent})
		}
		return spread(width, cells, nil)
	}
	if t.message != "" {
		return spread(width, []cell{{" " + t.message, ""}}, nil)
	}
	var cells []cell
	used := 0
	for _, key := range t.keys() {
		size := utf8.RuneCountInString(key[0]) + utf8.RuneCountInString(key[1]) + 4
		if used+size > width {
			break
		}
		used += size
		cells = append(cells, cell{" " + key[0], colorAccent}, cell{" " + key[1] + "  ", colorDim})
	}
	return spread(width, cells, nil)
}

// body is everything between the two rules.
func (t *tui) body(width, height int) []string {
	var lines []string
	switch {
	case t.overlay != nil:
		lines = t.overlayLines(t.overlay, width, height)
	case !t.appsLoaded && t.appsErr == "":
		// The first moment: the whisk stirs while the server answers.
		lines = []string{"", ""}
		for _, line := range whisk(1, t.frame) {
			lines = append(lines, "   "+paint(line, colorAccent))
		}
		lines = append(lines, "", "   "+paint("chasen", colorBold)+paint("  asks "+t.server+" for its apps", colorDim))
	case t.appsErr != "":
		lines = []string{""}
		for _, line := range wrap(t.appsErr, width-4) {
			lines = append(lines, "  "+paint(line, colorBad))
		}
		lines = append(lines, "", paint("  g  ", colorAccent)+"try again")
	case len(t.apps) == 0:
		lines = []string{"", "  " + paint("No apps on "+t.server+" yet.", colorBold), ""}
		lines = append(lines, "  Go to the directory of an app, one with a Dockerfile, and run:", "", "    "+paint("chasen deploy", colorAccent), "", "  It shows up here when it is live.")
	default:
		lines = t.columns(width, height)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// columns draws the apps on the left and the chosen app on the right.
func (t *tui) columns(width, height int) []string {
	left := 22
	for _, app := range t.apps {
		left = max(left, utf8.RuneCountInString(app.Name)+7)
	}
	left = min(left, 30, width/3)
	right := width - left - 3

	label := colorDim
	if !t.inPane {
		label = colorAccent
	}
	leftLines := []string{paint(fit(" apps", left), label), strings.Repeat(" ", left)}
	start, end := window(len(t.apps), height-2, t.selected)
	for i := start; i < end; i++ {
		leftLines = append(leftLines, t.appLine(i, left))
	}

	// The corner under the apps: the server, and the whisk.
	if corner := t.corner(left, height-len(leftLines)-1); len(corner) > 0 {
		for len(leftLines) < height-len(corner) {
			leftLines = append(leftLines, strings.Repeat(" ", left))
		}
		leftLines = append(leftLines, corner...)
	}

	rightLines := []string{t.tabLine(right), ""}
	rightLines = append(rightLines, t.tabLines(right, height-2)...)

	lines := make([]string, height)
	for i := range lines {
		l, r := strings.Repeat(" ", left), ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		lines[i] = l + paint(" │ ", colorDim) + r
	}
	return lines
}

// stateColor is the color for the state of a container.
func stateColor(state string) string {
	switch {
	case state == "":
		return colorDim
	case strings.HasPrefix(state, "Up") && !strings.Contains(state, "unhealthy"):
		return colorAccent
	}
	return colorBad
}

func (t *tui) appLine(i, width int) string {
	app := t.apps[i]
	state := strings.Join(statusValues(t.pane(app.Name, tabOverview).lines, "State"), "")
	dot := cell{"● ", stateColor(state)}
	if state == "" {
		dot.text = "○ "
	}
	if i == t.selected {
		// The marker has the accent on the side that has the keys.
		marker := colorAccent
		if t.inPane {
			marker = colorDim
		}
		return spread(width, []cell{{" ▸ ", marker}, dot, {app.Name, colorBold}}, nil)
	}
	return spread(width, []cell{{"   ", ""}, dot, {app.Name, ""}}, nil)
}

func (t *tui) tabLine(width int) string {
	var cells []cell
	for i, name := range tabNames {
		color := colorDim
		if i == t.tab && t.inPane {
			color = colorAccent + ";" + colorBold + ";4"
		} else if i == t.tab {
			color = colorBold + ";4"
		}
		cells = append(cells, cell{name, color}, cell{"   ", ""})
	}
	return spread(width, cells, nil)
}

// tabLines draws the tab of the chosen app.
func (t *tui) tabLines(width, height int) []string {
	app := t.app()
	if t.tab == tabLogs {
		return t.logLines(width, height)
	}
	p := t.pane(app, t.tab)
	switch {
	case p.err != "":
		var lines []string
		for _, line := range wrap(p.err, width) {
			lines = append(lines, paint(line, colorBad))
		}
		return lines
	case !p.loaded:
		return []string{paint(spinner[t.frame%len(spinner)], colorAccent) + paint(" Loading", colorDim)}
	}
	switch t.tab {
	case tabOverview:
		return t.overviewLines(app, p.lines, width)
	case tabHistory:
		return t.rowLines(width, height, func(row string) []cell { return t.historyCells(row, width) })
	case tabBackups:
		if len(t.rows()) == 0 {
			return []string{paint("No backups yet.", colorDim), "", "Press " + paint("b", colorAccent) + " to make one now. The server also makes one each hour, and before each deploy."}
		}
		return t.rowLines(width, height, t.backupCells)
	}
	if len(t.rows()) == 0 {
		return []string{paint("No domains.", colorDim)}
	}
	return t.rowLines(width, height, func(row string) []cell { return []cell{{row, ""}} })
}

// overviewLines shows the state of an app, and the last things that happened.
func (t *tui) overviewLines(app string, status []string, width int) []string {
	state := strings.Join(statusValues(status, "State"), "")
	lines := []string{
		spread(width, []cell{{app, colorBold}}, []cell{{"● ", stateColor(state)}, {state, ""}}),
		"",
	}
	field := func(name string, values ...string) {
		for i, value := range values {
			label := fit(name, 10)
			if i > 0 {
				label = fit("", 10)
			}
			lines = append(lines, spread(width, []cell{{label, colorDim}, {value, ""}}, nil))
		}
	}
	field("Version", statusValues(status, "Version")...)
	field("URL", statusValues(status, "URL")...)
	for _, stamp := range statusValues(status, "Backup") {
		if stamp == "none" {
			field("Backup", "none yet")
		} else {
			field("Backup", t.stampText(stamp))
		}
	}
	for _, replica := range statusValues(status, "Replica") {
		if replica == "off" {
			replica = "off: the backups stay on the server"
		}
		field("Replica", replica)
	}

	history := t.pane(app, tabHistory).lines
	if len(history) > 1 {
		lines = append(lines, "", paint("Last changes", colorDim))
		for _, row := range history[1:min(len(history), 6)] {
			lines = append(lines, spread(width, t.historyCells(row, width), nil))
		}
	}
	return lines
}

// actionWidth is the room for the action of a history row: what the id, the
// time, and the result leave.
func actionWidth(width int) int { return max(12, min(30, width-5-28-12)) }

// historyCells draws one row of `chasen history`: ID, WHEN, ACTION, RESULT.
func (t *tui) historyCells(row string, width int) []cell {
	parts := columns.Split(strings.TrimSpace(row), 4)
	if len(parts) < 4 {
		return []cell{{row, ""}}
	}
	result := cell{"✓ " + parts[3], colorAccent}
	switch parts[3] {
	case "failed":
		result = cell{"✗ failed", colorBad}
	case "running":
		result = cell{spinner[t.frame%len(spinner)] + " running", colorAccent}
	}
	when := parts[1]
	if at, err := time.Parse("2006-01-02 15:04:05", when); err == nil {
		when = at.Format("Jan 2 15:04") + " · " + ago(t.now(), at)
	}
	return []cell{{fit(parts[0], 5), colorDim}, {fit(when, 28), colorDim}, {fit(parts[2], actionWidth(width)), ""}, result}
}

// backupCells draws one row of `chasen backups`: the name of the backup, and
// where it is.
func (t *tui) backupCells(row string) []cell {
	parts := columns.Split(strings.TrimSpace(row), 2)
	if len(parts) < 2 {
		return []cell{{row, ""}}
	}
	if parts[0] == "live" {
		return []cell{{fit("live replica", 34), colorAccent}, {parts[1], colorDim}}
	}
	return []cell{{fit(t.stampText(parts[0]), 34), ""}, {parts[1], colorDim}}
}

// rowLines draws rows that the cursor can choose. The first line of the
// history is its header.
func (t *tui) rowLines(width, height int, cells func(row string) []cell) []string {
	var lines []string
	if t.tab == tabHistory {
		lines = append(lines, spread(width, []cell{{fit("ID", 5) + fit("WHEN (UTC)", 28) + fit("ACTION", actionWidth(width)) + "RESULT", colorDim}}, nil))
		height--
	}
	rows := t.rows()
	start, end := window(len(rows), height, t.cursor)
	for i := start; i < end; i++ {
		if i == t.cursor && t.inPane {
			// One color for the whole chosen row.
			text := ""
			for _, c := range cells(rows[i]) {
				text += c.text
			}
			lines = append(lines, paint(fit(text, width), colorChosen))
			continue
		}
		lines = append(lines, spread(width, cells(rows[i]), nil))
	}
	return lines
}

func (t *tui) logLines(width, height int) []string {
	if len(t.logs) == 0 {
		return []string{paint(spinner[t.frame%len(spinner)], colorAccent) + paint(" Waiting for the logs of "+t.app(), colorDim)}
	}
	start := max(0, len(t.logs)-height)
	if !t.logsFollow {
		start = min(t.scroll, start)
	}
	var lines []string
	for _, line := range t.logs[start:min(len(t.logs), start+height)] {
		lines = append(lines, clip(line, width))
	}
	return lines
}

// overlayLines draws the overlay over the whole body.
func (t *tui) overlayLines(o *overlay, width, height int) []string {
	state := cell{}
	switch {
	case o.pick != nil || (!o.running && len(o.lines) > 0 && o.cancel == nil):
	case o.running:
		state = cell{spinner[t.frame%len(spinner)] + " running ", colorAccent}
	case o.failed:
		state = cell{"✗ failed ", colorBad}
	default:
		state = cell{"✓ done ", colorAccent}
	}
	lines := []string{spread(width, []cell{{" " + o.title, colorAccent + ";" + colorBold}}, []cell{state}), ""}

	if o.pick != nil {
		for i, choice := range o.choices {
			if i == o.cursor {
				lines = append(lines, spread(width, []cell{{" ▸ ", colorAccent}, {choice, colorBold}}, nil))
			} else {
				lines = append(lines, "   "+clip(choice, width-3))
			}
		}
		return lines
	}

	room := height - 2
	start := max(0, len(o.lines)-room)
	if !o.follow {
		start = min(o.scroll, start)
	}
	for _, line := range o.lines[start:min(len(o.lines), start+room)] {
		color := ""
		switch {
		case strings.HasPrefix(line, "Error:"):
			color = colorBad
		case o.headings && line != "" && !strings.HasPrefix(line, " "):
			color = colorAccent // a heading of the help
		}
		lines = append(lines, " "+paint(clip(line, width-2), color))
	}
	return lines
}

// --- the corner: the server and the whisk -------------------------------------

const (
	graphWidth = 8 // the width of a line of the server
	stirFrames = 9 // how long the whisk stirs, in ticks of the clock
)

// cornerFits reports if the window has room under the apps for the numbers
// of the server. Without it, the first line has them.
func (t *tui) cornerFits() bool {
	return len(t.stats) > 0 && len(t.apps) > 0 && t.overlay == nil && t.height-4-2-len(t.apps)-1 >= 4 && t.width >= 60
}

// corner draws what goes under the apps, from the bottom: the whisk when
// there is room for it, and above it the load, the memory, and the disk of
// the server.
func (t *tui) corner(width, room int) []string {
	var lines []string
	if t.cornerFits() {
		lines = t.gauges(width)
	}
	if room >= len(lines)+len(whiskShape)/2+2 {
		force := 0.0
		if left := t.stirUntil - t.frame; left > 0 && left <= stirFrames {
			force = math.Sin(math.Pi * float64(left) / stirFrames)
		}
		if len(lines) > 0 {
			lines = append(lines, strings.Repeat(" ", width))
		}
		for _, line := range whisk(force, t.frame) {
			lines = append(lines, paint(fit("   "+line, width), colorAccent))
		}
	}
	return lines
}

// gauges draws the server in three lines: how much of the cores, of the
// memory, and of the disk is in use. Each line is thin, so the three do not
// run into each other in a terminal with tight lines.
func (t *tui) gauges(width int) []string {
	lines := []string{paint(fit(" server", width), colorDim)}
	line := func(label string, used int, value string, bad bool) {
		color := colorAccent
		if bad {
			color = colorBad
		}
		filled := max(0, min(used*graphWidth/100, graphWidth))
		if used > 0 && filled == 0 {
			filled = 1
		}
		lines = append(lines, spread(width, []cell{
			{label, colorDim},
			{strings.Repeat("━", filled), color},
			{strings.Repeat("─", graphWidth-filled), colorDim},
			{value, ""},
		}, nil))
	}

	if parts := loadLine.FindStringSubmatch(strings.Join(statusValues(t.stats, "Load"), "")); parts != nil {
		// A load equal to the count of the cores is a full line.
		load, _ := strconv.ParseFloat(parts[1], 64)
		cores, _ := strconv.Atoi(parts[2])
		line(" load ", int(load*100/float64(max(cores, 1))), fmt.Sprintf(" %5s", parts[1]), load > float64(cores))
	}
	for _, name := range []string{"Memory", "Disk"} {
		if parts := percent.FindStringSubmatch(strings.Join(statusValues(t.stats, name), "")); parts != nil {
			used, _ := strconv.Atoi(parts[1])
			line(map[string]string{"Memory": " mem  ", "Disk": " disk "}[name], used, fmt.Sprintf(" %4d%%", used), used >= 90)
		}
	}
	return lines
}

// whiskShape is the Chasen mark: a bamboo whisk of seven tines, a binding,
// and a handle, on a grid of 15 by 14 pixels. Two pixels make one character,
// one above the other.
var whiskShape = func() [14][15]bool {
	var grid [14][15]bool
	for row := 9; row <= 13; row++ {
		from, to := 6, 8 // the handle
		switch row {
		case 9:
			from, to = 3, 11 // the binding
		case 10:
			from, to = 4, 10
		}
		for col := from; col <= to; col++ {
			grid[row][col] = true
		}
	}
	return grid
}()

// whisk draws the mark. force is how hard it stirs, from 0 to 1: the tines
// bend from their base, and the wave runs across them.
func whisk(force float64, frame int) []string {
	grid := whiskShape
	for tine := range 7 {
		for row := range 9 {
			up := float64(8-row) / 8 // 1 at the tip, 0 at the binding
			lean := math.Pow(up, 1.5)
			flare := float64(tine-3) * 0.4 * lean // at rest the outer tines lean out
			bend := force * 1.25 * lean * math.Sin(float64(frame)*1.9-float64(tine)*0.45)
			col := 1 + tine*2 + int(math.Round(flare+bend))
			grid[row][max(0, min(col, 14))] = true
		}
	}
	lines := make([]string, 7)
	for line := range lines {
		for col := range 15 {
			top, bottom := grid[line*2][col], grid[line*2+1][col]
			switch {
			case top && bottom:
				lines[line] += "█"
			case top:
				lines[line] += "▀"
			case bottom:
				lines[line] += "▄"
			default:
				lines[line] += " "
			}
		}
	}
	return lines
}
