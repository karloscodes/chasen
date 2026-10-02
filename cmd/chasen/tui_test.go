package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// testServer answers the API of a Chasen server with fixed output, and keeps
// the commands it got.
type testServer struct {
	*httptest.Server
	mu       sync.Mutex
	commands []string
	apps     string
}

func newTestServer(t *testing.T) *testServer {
	s := &testServer{apps: "NAME      VERSION       DOMAINS\nblog      a1b2c3d4e5f6  blog.example.com\nlognorth  latest        lognorth.example.com\nshop      3f9a2c1d5e8b  shop.example.com shop.com\n"}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		args := r.URL.Query()["arg"]
		command := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/") + " " + strings.Join(args, " "))
		s.mu.Lock()
		s.commands = append(s.commands, command)
		apps := s.apps
		s.mu.Unlock()

		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		output := ""
		switch strings.Fields(command)[0] {
		case "load":
			// This server is from before the load command.
			io.WriteString(w, "Error: unknown command \"load\"\n"+protocol.ExitMarker+"1\n")
			return
		case "list":
			output = apps
		case "status":
			output = fmt.Sprintf("App:      %s\nVersion:  3f9a2c1d5e8b\nState:    Up 3 hours (healthy)\nURL:      https://%s.example.com\nURL:      https://shop.com\nBackup:   20261001T110000Z\nReplica:  live\n", name, name)
			if name == "blog" {
				output = "App:      blog\nVersion:  a1b2c3d4e5f6\nState:    not running\nURL:      https://blog.example.com\nBackup:   none\nReplica:  off\n"
			}
		case "history":
			output = "ID  WHEN (UTC)           ACTION          RESULT\n5   2026-10-01 12:03:21  deploy 6cff7df  failed\n4   2026-10-01 12:03:02  restore         succeeded\n"
			if len(args) == 2 {
				output = "Pulling ghcr.io/you/shop:6cff7df\n\x1b[2J\x1b[31mThe app did not answer /up\x1b[0m\n"
			}
		case "backups":
			output = "20261001T110000Z  server + offsite\n20261001T100000Z  server\nlive              offsite, continuous\n"
		case "domains":
			output = "shop.example.com\nshop.com\n"
		case "logs":
			output = "GET /up 200\nGET / 200\n"
		case "backup":
			output = name + ": backup 20261001T120000Z (on the server and offsite)\n"
		default:
			output = "done: " + command + "\n"
		}
		io.WriteString(w, output+protocol.ExitMarker+"0\n")
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *testServer) got(command string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.commands, command)
}

// screen is the TUI without a terminal: the test sends keys and reads what
// the screen shows.
type screen struct {
	t   *testing.T
	tui *tui
}

func openScreen(t *testing.T, s *testServer, cwdApp string) *screen {
	client := protocol.Client{URL: s.URL, Token: "token"}
	ui := newTUI(func(ctx context.Context, out io.Writer, args ...string) (int, error) {
		return client.Run(ctx, args[0], args[1:], nil, out)
	}, "example.com", cwdApp)
	ui.width, ui.height = 100, 30
	ui.now = func() time.Time { return time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC) }
	colorsOn = true
	sc := &screen{t, ui}
	ui.loadApps()
	sc.settle()
	return sc
}

// settle handles the events until the server has answered everything.
func (sc *screen) settle() {
	// Logs never stop arriving, so there is also a limit.
	limit := time.After(400 * time.Millisecond)
	for {
		select {
		case event := <-sc.tui.events:
			sc.tui.handle(event)
		case <-time.After(60 * time.Millisecond):
			return
		case <-limit:
			return
		}
	}
}

func (sc *screen) press(keys ...string) {
	for _, key := range keys {
		sc.tui.handle(keyEvent(key))
		sc.settle()
	}
}

// text is what the screen shows, without the colors.
func (sc *screen) text() string {
	return ansi.ReplaceAllString(sc.tui.view(), "")
}

func (sc *screen) shows(want ...string) {
	sc.t.Helper()
	text := sc.text()
	for _, w := range want {
		if !strings.Contains(text, w) {
			sc.t.Errorf("the screen does not show %q:\n%s", w, text)
		}
	}
}

func TestScreen(t *testing.T) {
	t.Run("opens on the app of the directory, with its state and its last changes", func(t *testing.T) {
		sc := openScreen(t, newTestServer(t), "shop")

		sc.shows("chasen  example.com", "3 apps", "▸ ● shop", "● blog", "Up 3 hours (healthy)",
			"https://shop.example.com", "https://shop.com", "Oct 1 11:00 UTC · 3 h ago", "Last changes", "deploy 6cff7df", "✗ failed", "Oct 1 12:03 · 1 h ago")
	})

	t.Run("the arrows go to another app and another tab", func(t *testing.T) {
		sc := openScreen(t, newTestServer(t), "shop")

		sc.press("up", "up")
		sc.shows("▸ ● blog", "not running", "none yet", "off: the backups stay on the server")

		sc.press("down", "down", "3")
		sc.shows("▸ ● shop", "Oct 1 11:00 UTC · 3 h ago", "server + offsite", "live replica")

		sc.press("tab")
		sc.shows("shop.example.com", "shop.com")

		sc.press("tab")
		sc.shows("GET /up 200")

		sc.press("shift+tab", "shift+tab")
		sc.shows("live replica")
	})

	t.Run("left and right go to a side, and up and down stay in it", func(t *testing.T) {
		sc := openScreen(t, newTestServer(t), "shop")

		// On the left, down and up change the app.
		sc.press("up")
		sc.shows("▸ ● lognorth")

		// On the right, they move in the rows, and the app stays.
		sc.press("down", "2", "down")
		if sc.tui.app() != "shop" || sc.tui.cursor != 1 {
			t.Errorf("the app is %s and the row is %d, want shop and row 1", sc.tui.app(), sc.tui.cursor)
		}
		sc.press("right", "up")
		if sc.tui.app() != "shop" || sc.tui.cursor != 0 {
			t.Errorf("after right and up: the app is %s and the row is %d, want shop and row 0", sc.tui.app(), sc.tui.cursor)
		}

		// Back on the left, the same keys change the app again.
		sc.press("left", "up")
		sc.shows("▸ ● lognorth")
	})

	t.Run("enter on a history entry shows its output, without its escape codes", func(t *testing.T) {
		sc := openScreen(t, newTestServer(t), "shop")

		sc.press("2", "enter")

		sc.shows("history shop 5", "The app did not answer /up")
		if strings.Contains(sc.tui.view(), "\x1b[2J") {
			t.Error("an escape code of the server reached the terminal")
		}
		sc.press("esc")
		sc.shows("WHEN (UTC)")
	})

	t.Run("b backs up the app and shows what the server says", func(t *testing.T) {
		s := newTestServer(t)
		sc := openScreen(t, s, "shop")

		sc.press("b")

		sc.shows("backup shop", "✓ done", "shop: backup 20261001T120000Z")
		if !s.got("backup shop") {
			t.Errorf("the server got %v", s.commands)
		}
	})

	t.Run("a restart and a restore ask first, and n changes nothing", func(t *testing.T) {
		s := newTestServer(t)
		sc := openScreen(t, s, "shop")

		sc.press("r")
		sc.shows("Restart shop?")
		sc.press("n")
		if s.got("restart shop") {
			t.Fatal("n restarted the app")
		}

		sc.press("r", "y")
		sc.shows("restart shop", "✓ done")
		sc.press("esc", "3", "down", "enter")
		sc.shows("Restore shop to the backup of Oct 1 10:00 UTC")
		sc.press("y")

		if !s.got("restart shop") || !s.got("restore shop 20261001T100000Z") {
			t.Errorf("the server got %v", s.commands)
		}
	})

	t.Run("a adds the domain that the user types, and x removes the chosen one", func(t *testing.T) {
		s := newTestServer(t)
		sc := openScreen(t, s, "shop")

		sc.press("4", "a", "s", "h", "o", "p", "x", "backspace", ".", "o", "r", "g")
		sc.shows("Add a domain to shop: shop.org")
		sc.press("enter", "esc", "down", "x", "y")

		if !s.got("domains shop add shop.org") || !s.got("domains shop rm shop.com") {
			t.Errorf("the server got %v", s.commands)
		}
	})

	t.Run("an older server has no load command: the corner stays empty, and the screen asks once", func(t *testing.T) {
		s := newTestServer(t)
		sc := openScreen(t, s, "shop")

		sc.press("g", "g")

		if text := sc.text(); strings.Contains(text, "mem ") || strings.Contains(text, "unknown command") {
			t.Errorf("the screen shows numbers or an error:\n%s", text)
		}
		asked := 0
		s.mu.Lock()
		for _, command := range s.commands {
			if command == "load" {
				asked++
			}
		}
		s.mu.Unlock()
		if asked != 1 {
			t.Errorf("asked for the load %d times, want 1", asked)
		}
	})

	t.Run("the screen does not deploy: d does nothing, in the directory of an app too", func(t *testing.T) {
		s := newTestServer(t)
		sc := openScreen(t, s, "shop")

		sc.press("d", "y")

		for _, command := range s.commands {
			if strings.HasPrefix(command, "deploy") {
				t.Errorf("the screen sent %q", command)
			}
		}
		if sc.tui.overlay != nil || sc.tui.prompt != nil {
			t.Error("d opened something")
		}
	})

	t.Run("s goes to another server that the user is logged in to", func(t *testing.T) {
		one, two := newTestServer(t), newTestServer(t)
		two.apps = "NAME   VERSION  DOMAINS\nwiki   9d8c7b6  wiki.example.org\n"
		sc := openScreen(t, one, "shop")
		sc.tui.servers = []string{"example.com", "example.org"}
		sc.tui.change = func(server string) (runner, string, error) {
			client := protocol.Client{URL: two.URL, Token: "token"}
			return func(ctx context.Context, out io.Writer, args ...string) (int, error) {
				return client.Run(ctx, args[0], args[1:], nil, out)
			}, server, nil
		}
		sc.shows("s servers")

		sc.press("s")
		sc.shows("servers", "▸ example.com", "example.org")
		sc.press("down", "enter")

		sc.shows("chasen  example.org", "1 app", "▸ ● wiki")
		if text := sc.text(); strings.Contains(text, "● blog") || strings.Contains(text, "● lognorth") {
			t.Errorf("the apps of the first server are still on the screen:\n%s", text)
		}
	})

	t.Run("a server with no apps says how to deploy the first one", func(t *testing.T) {
		s := newTestServer(t)
		s.apps = "NAME  VERSION  DOMAINS\n"

		sc := openScreen(t, s, "")

		sc.shows("No apps on example.com yet", "chasen deploy")
	})

	t.Run("a token that the server refuses says how to log in again", func(t *testing.T) {
		s := newTestServer(t)
		client := protocol.Client{URL: s.URL, Token: "wrong"}
		ui := newTUI(func(ctx context.Context, out io.Writer, args ...string) (int, error) {
			return client.Run(ctx, args[0], args[1:], nil, out)
		}, "example.com", "")
		sc := &screen{t, ui}

		ui.loadApps()
		sc.settle()

		sc.shows("does not accept the token", "chasen add server")
	})

	t.Run("no line is wider than the window, at any size", func(t *testing.T) {
		sc := openScreen(t, newTestServer(t), "shop")
		for _, size := range [][2]int{{60, 12}, {72, 20}, {100, 30}, {200, 60}} {
			sc.tui.width, sc.tui.height = size[0], size[1]
			for _, keys := range [][]string{{"1"}, {"2"}, {"3"}, {"4"}, {"5"}, {"?"}, {"esc", "b"}, {"esc", "r"}, {"n"}} {
				sc.press(keys...)
				lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sc.text(), "\x1b[H"), "\x1b[J"), "\r\n")
				if len(lines) > size[1] {
					t.Errorf("%dx%d after %v: %d lines", size[0], size[1], keys, len(lines))
				}
				for _, line := range lines {
					if n := utf8.RuneCountInString(line); n > size[0] {
						t.Errorf("%dx%d after %v: a line of %d columns: %q", size[0], size[1], keys, n, line)
					}
				}
			}
		}
	})
}

func TestDecodeKeys(t *testing.T) {
	got := decodeKeys([]byte("j\x1b[A\x1b[6~\r\x7f\x1b[1;5Cq\x1b"))

	want := []string{"j", "up", "pgdn", "enter", "backspace", "q", "esc"}
	if keys := decodeKeys([]byte("\x1b[Z\t")); !slices.Equal(keys, []string{"shift+tab", "tab"}) {
		t.Errorf("decodeKeys of shift+tab and tab = %q", keys)
	}
	if !slices.Equal(got, want) {
		t.Errorf("decodeKeys = %q, want %q", got, want)
	}
}

func TestParseApps(t *testing.T) {
	apps := parseApps("NAME   VERSION  DOMAINS\nshop   3f9a2c1  shop.example.com shop.com\nnew             new.example.com\n")

	want := []appRow{{"shop", "3f9a2c1", "shop.example.com shop.com"}, {"new", "", "new.example.com"}}
	if !slices.Equal(apps, want) {
		t.Errorf("parseApps = %+v, want %+v", apps, want)
	}
}

// The screen against the mock server of bin/dev, over HTTP.
func TestScreenOnTheMockServer(t *testing.T) {
	open := func(t *testing.T) *screen {
		server := mock.New(time.Now())
		// Steps that take no time.
		server.Wait = func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Millisecond):
				return true
			}
		}
		web := httptest.NewServer(server)
		t.Cleanup(web.Close)
		client := protocol.Client{URL: web.URL, Token: server.Token}
		ui := newTUI(func(ctx context.Context, out io.Writer, args ...string) (int, error) {
			return client.Run(ctx, args[0], args[1:], nil, out)
		}, "mock", "shop")
		ui.width, ui.height = 100, 30
		sc := &screen{t, ui}
		t.Cleanup(ui.stopLogs)
		ui.loadApps()
		sc.settle()
		return sc
	}

	t.Run("opens on the shop, with its failed deploy in the history", func(t *testing.T) {
		sc := open(t)

		sc.shows("3 apps", "▸ ● shop", "Up 3 hours (healthy)", "https://shop.com", "✗ failed", "deploy 3f9a2c1")
	})

	t.Run("a restart changes the state, and the history has it", func(t *testing.T) {
		sc := open(t)

		sc.press("r", "y")
		sc.shows("restart shop", "✓ done", "Restarted shop")
		sc.press("esc")
		sc.shows("Up 1 second (healthy)")
		sc.press("2")

		if rows := sc.tui.rows(); len(rows) != 5 || !strings.Contains(rows[0], "restart") || !strings.Contains(rows[0], "succeeded") {
			t.Errorf("the history is %q", rows)
		}
	})

	t.Run("the corner shows the load, the memory, and the disk of the server", func(t *testing.T) {
		sc := open(t)

		sc.shows("server", " load ", " mem  ", " disk ", "━━━─────   41%", "3 apps")
		if !strings.Contains(sc.text(), whisk[5]) {
			t.Errorf("the whisk is not under the apps:\n%s", sc.text())
		}

		// A low window has no room under the apps: the first line has the numbers.
		sc.tui.height = 12
		sc.shows("mem ", "disk 41%  3 apps")
		if strings.Contains(sc.text(), "━") {
			t.Errorf("a low window still draws the bars:\n%s", sc.text())
		}
	})

	t.Run("a backup, a new domain, and the logs all answer", func(t *testing.T) {
		sc := open(t)

		sc.press("b", "esc", "3")
		if rows := sc.tui.rows(); len(rows) != 8 {
			t.Errorf("want 7 backups and the live replica, got %q", rows)
		}
		sc.press("4", "a", "x", ".", "i", "o", "enter", "esc")
		sc.shows("x.io")
		sc.press("5")
		sc.shows("shop GET /up 200")
	})
}

func TestWhisk(t *testing.T) {
	if len(whisk) != 7 {
		t.Fatalf("the whisk is %d lines, want 7", len(whisk))
	}
	for _, line := range whisk {
		if utf8.RuneCountInString(line) != 15 {
			t.Errorf("a line of the whisk is %d columns, want 15: %q", utf8.RuneCountInString(line), line)
		}
	}
}

func TestScreenIsStillWhenNothingChanges(t *testing.T) {
	sc := openScreen(t, newTestServer(t), "shop")
	before := sc.tui.view()

	// Three seconds of the clock, with no key and no answer from the server.
	for range 24 {
		sc.tui.frame++
	}

	if sc.tui.view() != before {
		t.Error("the screen changed with nothing to show: something moves by itself")
	}
}
