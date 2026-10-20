package main

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// openMap opens the screen as chasen does outside the directory of an app:
// on the server, with its map, in front of mock servers. The first is the
// current one. A server with a bucket has the live replica of its apps.
func openMap(t *testing.T, buckets ...string) *screen {
	now := time.Date(2026, 10, 20, 12, 10, 0, 0, time.UTC)
	runners := map[string]runner{}
	var names []string
	for i, bucket := range buckets {
		server := mock.New(now)
		if bucket != "" {
			server.UseBucket(bucket)
		}
		web := httptest.NewServer(server)
		t.Cleanup(web.Close)
		client := protocol.Client{URL: web.URL, Token: server.Token}
		name := []string{"shop-server", "lab"}[i]
		names = append(names, name)
		runners[name] = func(ctx context.Context, out io.Writer, args ...string) (int, error) {
			return client.Run(ctx, args[0], args[1:], nil, out)
		}
	}
	ui := newTUI(runners[names[0]], names[0], "")
	if len(names) > 1 {
		ui.servers = names
		ui.change = func(server string) (runner, string, error) {
			if run, ok := runners[server]; ok {
				return run, server, nil
			}
			return nil, "", errors.New("no login for " + server)
		}
	}
	ui.width, ui.height = 140, 34
	ui.now = func() time.Time { return now }
	colorsOn = true
	t.Cleanup(ui.stopLogs)
	sc := &screen{t, ui}
	ui.loadApps()
	sc.settle()
	return sc
}

func TestMap(t *testing.T) {
	t.Run("the first page is the server on the left, and its map on the right", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.shows(" server ", "▸shop-server", "● blog", "● shop", "map   shop-server", "$ chasen overview",
			"amd64 · 4 cores · 7.6 GB", "! 2 warnings",
			"internet ", " proxy ", "3f9a2c1", "shop.example.com shop.com", "└ jobs  ● Up 3 hours",
			"2 db", "backup 10 min ago", "backups-eu", "live replica", "no backup yet",
			"you ", " ssh ", "API   SSH only: not on the web",
			"A moving line is an open path, or data on its way. It does not count requests.")
		if strings.Contains(sc.text(), "▸ ●") {
			t.Errorf("an app is chosen on the first page:\n%s", sc.text())
		}
	})

	t.Run("without a bucket, the backups stay on the server, and no replica runs", func(t *testing.T) {
		sc := openMap(t, "")

		sc.shows("backup 10 min ago", "this server only")
		if strings.Contains(sc.text(), "live replica") {
			t.Errorf("a server with no bucket has a live replica:\n%s", sc.text())
		}
	})

	t.Run("a line moves from one frame to the next while its path is open", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		before := sc.text()

		sc.tui.handle(tickEvent{})

		if after := sc.text(); after == before {
			t.Errorf("the map is the same after a frame:\n%s", after)
		}
	})

	t.Run("the map only shows: enter, the arrow right, a tab, and a click on it change nothing", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.press("enter", "right", "tab", "2", "mouse:0:80:8")

		sc.shows("map   shop-server", "▸shop-server")
		if !sc.tui.onServer || sc.tui.inPane {
			t.Errorf("the keys went into the map: on the server %v, in the pane %v", sc.tui.onServer, sc.tui.inPane)
		}
	})

	t.Run("down goes to the first app with its tabs, and up or esc comes back to the map", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.press("down")
		sc.shows("▸ ● blog", "overview   history", "$ chasen -a blog status")
		sc.press("up")
		sc.shows("map   shop-server", "▸shop-server")
		sc.press("down", "down", "esc")

		sc.shows("map   shop-server")
	})

	t.Run("a click on the server shows its map, and a click on an app opens it", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		sc.text() // a click finds what the last drawing put under it

		sc.press("mouse:0:5:" + itoa(sc.tui.hit.appTop+3))
		sc.shows("▸ ● shop", "overview   history")
		sc.press("mouse:0:5:" + itoa(sc.tui.hit.appTop))

		sc.shows("map   shop-server")
	})

	t.Run("with several servers, down past the last app goes to the map of the next server", func(t *testing.T) {
		sc := openMap(t, "backups-eu", "")
		sc.ticks(480) // the list of the other server

		sc.press("down", "down", "down", "down")
		sc.shows(" servers ", "▸lab", "map   lab", "this server only")
		sc.press("up")

		if sc.tui.server != "shop-server" || sc.tui.app() != "shop" {
			t.Errorf("up from the map of lab went to %s on %s, want shop on shop-server", sc.tui.app(), sc.tui.server)
		}
	})

	t.Run("an app that is down has a red cross on its line, and the line stands still", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		shop := `[{"app":"shop","state":"Exited (1) 2 minutes ago","up":false,"version":"3f9a2c1","domains":["shop.com"],"databases":1,"backup":"20261020T120000Z","replica":"live"}]`
		sc.tui.handle(overviewEvent{server: "shop-server", output: shop})
		before := sc.text()

		sc.tui.handle(tickEvent{})

		sc.shows("✗ shop", "Exited (1)", "╳")
		after := strings.SplitN(lineWith(sc.text(), "Exited"), "▶", 3)
		if was := strings.SplitN(lineWith(before, "Exited"), "▶", 3); after[1] != was[1] {
			t.Errorf("the line from the proxy to a down app moves: %q, then %q", was[1], after[1])
		}
		if line := lineWith(sc.tui.view(), "Exited"); !strings.Contains(line, "\x1b["+colorBad+"m━━╳━") {
			t.Errorf("the cross is not red:\n%q", line)
		}
	})

	t.Run("an API on the internet is a branch of the proxy", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		shop := `[{"app":"shop","state":"Up 1 hour","up":true,"version":"3f9a2c1","domains":["shop.com"],"databases":1,"backup":"20261020T120000Z","replica":"live"}]`
		sc.tui.handle(overviewEvent{server: "shop-server", output: shop})

		sc.tui.handle(statsEvent{output: "API:      https://api.example.com, on the internet\n"})

		sc.shows("━┳", "┗", "◆ API", "https://api.example.com, on the internet")
		if strings.Contains(sc.text(), " ssh ") {
			t.Errorf("the map shows the way through SSH too:\n%s", sc.text())
		}
	})

	t.Run("a server from before the map fields has its apps, and says when its data shows", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.tui.handle(overviewEvent{server: "shop-server", output: `[{"app":"shop","state":"Up 1 hour","up":true,"version":"3f9a2c1"}]`})

		sc.shows("● shop", "no domain", "the data shows after the next update of the server")
	})

	t.Run("the text of a server reaches the map without its escape codes", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.tui.handle(overviewEvent{server: "shop-server", output: `[{"app":"shop","state":"Up","up":true,"version":"x\u001b]52;c;aGk=\u0007","domains":["shop.com\u001b[2J"],"replica":"off","backup":""}]`})

		if view := sc.tui.view(); strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x1b[2J") {
			t.Errorf("an escape code of the server reached the terminal:\n%q", view)
		}
		sc.shows("shop.com")
	})
}

// lineWith returns the first line of a text that has a word.
func lineWith(text, word string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, word) {
			return line
		}
	}
	return ""
}
