package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// TestRecordTheScreen is not a test: it records the screen for the site.
// It drives the real screen against the mock server through a list of keys,
// and writes each frame as HTML, with the key that made it and the colors of
// its theme. The site plays the frames.
//
//	CHASEN_RECORD=../chasenhq.com/src/data/screen.json go test ./cmd/chasen -run TestRecordTheScreen
func TestRecordTheScreen(t *testing.T) {
	path := os.Getenv("CHASEN_RECORD")
	if path == "" {
		t.Skip("set CHASEN_RECORD to the file of the frames")
	}
	// A computer with no saved choice of theme.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHASEN_THEME", "")
	t.Setenv("COLORTERM", "truecolor")
	keep := [3]string{colorAccent, colorDim, colorBad}
	colorAccent, chasenColors[0] = "38;2;245;184;61", "38;2;245;184;61"
	themeChoice = ""
	t.Cleanup(func() { colorAccent, colorDim, colorBad = keep[0], keep[1], keep[2]; chasenColors[0] = keep[0] })

	now := time.Date(2026, 10, 2, 10, 19, 0, 0, time.UTC)
	server := mock.New(now)
	server.UseBucket("backups-eu")
	server.Wait = func(ctx context.Context, d time.Duration) bool { return true }
	api := httptest.NewServer(server)
	defer api.Close()
	client := protocol.Client{URL: api.URL, Token: server.Token}

	// The screen opens in the repository of shop: four commits, and the
	// server ran the second and the third, each with its subject and its page.
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("remote", "add", "origin", "git@github.com:you/shop.git")
	var commits []string
	for _, subject := range []string{"Start the shop", "Add the cart", "Send the receipts by email", "Add gift cards"} {
		git("-c", "user.name=you", "-c", "user.email=you@example.com", "commit", "-q", "--allow-empty", "-m", subject)
		commits = append(commits, git("rev-parse", "HEAD"))
	}
	for _, hash := range commits[1:3] {
		settings := protocol.Settings{Image: "ghcr.io/you/shop:" + hash, Commit: commitOf(hash)}
		if code, err := client.Run(context.Background(), "deploy", []string{"shop", hash[:7]}, settings.Body(nil), io.Discard); code != 0 || err != nil {
			t.Fatalf("deploy %s: %d, %v", hash[:7], code, err)
		}
	}

	ui := newTUI(func(ctx context.Context, out io.Writer, args ...string) (int, error) {
		return client.Run(ctx, args[0], args[1:], nil, out)
	}, "example.com", "shop")
	ui.repo = true
	ui.cli = func(ctx context.Context, out io.Writer, args ...string) (int, error) { return 0, nil }
	ui.width, ui.height = 112, 30
	ui.now = func() time.Time { return now }
	ui.servers = []string{"example.com", "example.org"}
	run := ui.run
	// The second server holds a staging copy of shop: its own list, and the
	// answers of the mock for the rest.
	staging := func(ctx context.Context, out io.Writer, args ...string) (int, error) {
		if args[0] == "list" {
			io.WriteString(out, "NAME   VERSION       DOMAINS\nshop   9d8c7b6a1f2e  shop.example.org\n")
			return 0, nil
		}
		if args[0] == "overview" {
			io.WriteString(out, `[{"app":"shop","state":"Up 2 days","up":true,"version":"9d8c7b6a1f2e","domains":["shop.example.org"],"databases":2,"backup":"20261002T100000Z","replica":"off"}]`+"\n")
			return 0, nil
		}
		if args[0] == "load" {
			io.WriteString(out, "Load:     0.08 0.05 0.01 (2 cores, arm64)\nMemory:   0.9 GB of 3.8 GB (24%)\nDisk:     6 GB of 38 GB (16%)\nBucket:   none: the backups stay on this server\nAPI:      SSH only: not on the web\n")
			return 0, nil
		}
		return run(ctx, out, args...)
	}
	ui.change = func(server string) (runner, string, error) {
		if server == "example.org" {
			return staging, server, nil
		}
		return run, server, nil
	}
	colorsOn = true
	sc := &screen{t, ui}
	followTheme()
	ui.loadApps()
	sc.settle()

	type frame struct {
		Key   string `json:"key"`   // the key the person pressed, or ""
		Says  string `json:"says"`  // what the key does
		Theme string `json:"theme"` // the theme of the frame
		HTML  string `json:"html"`
	}
	var frames []frame
	shot := func(key, says string) {
		frames = append(frames, frame{key, says, currentTheme(), frameHTML(ui.view(), currentTheme())})
	}
	press := func(key, says string, keys ...string) {
		sc.press(keys...)
		shot(key, says)
	}

	shot("", "chasen: your servers on the left, the map of this one on the right")
	press("↓", "an app: its state, and its last changes", "down", "down", "down")
	press("2", "the history: each deploy names its commit, ↺ the server keeps its image", "2")
	press("R", "roll back to a kept version, in seconds: it asks first", "down", "R")
	press("6", "the commits of this directory, and what each one is on the server", "n", "6")
	press("d", "d deploys the chosen commit: the newest builds here", "d")
	press("3", "the backups: enter restores one", "n", "3")
	press(":", "run any command: the help narrows as you type", "1", ":", "r", "e")
	press("!", "the alerts of the server, refreshed every minute", "esc", "!")
	press("esc", "back to the map", "esc", "esc", "esc")
	press("↓", "every server is in the list: the map of the next one", "down", "down", "down", "down")
	press("t", "theme: monochrome", "t")
	press("t", "theme: the colors of your terminal, which follow Omarchy", "t")
	press("t", "theme: chasen", "t")
	press("?", "every key", "?")

	data, err := json.MarshalIndent(frames, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d frames in %s", len(frames), path)
}

// The colors of a code of the terminal, for the frames: the 16 colors of a
// common dark terminal, and the scale of 256.
var ansi16 = map[int]string{30: "#3f4451", 31: "#e06c75", 32: "#98c379", 33: "#e5c07b", 34: "#61afef", 35: "#c678dd", 36: "#56b6c2", 37: "#d7dae0", 90: "#7f848e"}

func color256(n int) string {
	switch {
	case n >= 232:
		g := 8 + 10*(n-232)
		return fmt.Sprintf("#%02x%02x%02x", g, g, g)
	case n >= 16:
		n -= 16
		level := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		return fmt.Sprintf("#%02x%02x%02x", level(n/36), level(n/6%6), level(n%6))
	}
	return "#d6d3d1"
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// frameHTML turns a view of the screen into HTML: one span for each run of
// text with the same style. A symbol keeps the width of one cell.
func frameHTML(view, theme string) string {
	view = strings.TrimPrefix(view, "\x1b[H")
	view = strings.TrimSuffix(view, "\x1b[J")
	view = strings.ReplaceAll(view, "\x1b[K", "")
	view = strings.ReplaceAll(view, "\r\n", "\n")

	var out strings.Builder
	var fg string
	var bold, dim, under, reverse bool
	style := func() string {
		var s []string
		if fg != "" && !reverse {
			s = append(s, "color:"+fg)
		}
		if reverse {
			s = append(s, "background:currentColor", "color:var(--bg)")
		}
		if bold {
			s = append(s, "font-weight:700")
		}
		if dim {
			s = append(s, "opacity:.55")
		}
		if under {
			s = append(s, "text-decoration:underline", "text-underline-offset:3px")
		}
		return strings.Join(s, ";")
	}
	open := false
	text := func(s string) {
		if s == "" {
			return
		}
		if st := style(); st != "" && !open {
			out.WriteString(`<span style="` + st + `">`)
			open = true
		}
		for _, r := range s {
			switch {
			case r >= 0x2500 && r <= 0x259f && (bold || under):
				// A line or a block in bold has another width, or gaps: keep it plain, in one cell.
				out.WriteString(`<span class="g" style="font-weight:400;text-decoration:none">` + string(r) + `</span>`)
			case r == ' ' && under:
				out.WriteString(`<span style="text-decoration:none"> </span>`)
			case r < 0x80, r >= 0x2500 && r <= 0x259f:
				out.WriteString(html.EscapeString(string(r)))
			default:
				out.WriteString(`<span class="g">` + html.EscapeString(string(r)) + `</span>`)
			}
		}
	}
	rest := view
	for {
		loc := sgr.FindStringSubmatchIndex(rest)
		if loc == nil {
			text(rest)
			break
		}
		text(rest[:loc[0]])
		if open {
			out.WriteString("</span>")
			open = false
		}
		codes := strings.Split(rest[loc[2]:loc[3]], ";")
		for i := 0; i < len(codes); i++ {
			switch n, _ := strconv.Atoi(codes[i]); {
			case n == 0:
				fg, bold, dim, under, reverse = "", false, false, false, false
			case n == 1:
				bold = true
			case n == 2:
				dim = true
			case n == 4:
				under = true
			case n == 7:
				reverse = true
			case n == 38 && i+4 < len(codes) && codes[i+1] == "2":
				r, _ := strconv.Atoi(codes[i+2])
				g, _ := strconv.Atoi(codes[i+3])
				b, _ := strconv.Atoi(codes[i+4])
				fg, i = fmt.Sprintf("#%02x%02x%02x", r, g, b), i+4
			case n == 38 && i+2 < len(codes) && codes[i+1] == "5":
				c, _ := strconv.Atoi(codes[i+2])
				fg, i = color256(c), i+2
			case ansi16[n] != "":
				fg = ansi16[n]
			}
		}
		rest = rest[loc[1]:]
	}
	if open {
		out.WriteString("</span>")
	}
	return out.String()
}
