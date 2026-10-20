package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// openMap opens the screen on its first page, the map, in front of mock
// servers: the first is the current one. A server with a bucket has the live
// replica of its apps.
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
	ui.width, ui.height = 120, 34
	ui.now = func() time.Time { return now }
	colorsOn = true
	t.Cleanup(ui.stopLogs)
	sc := &screen{t, ui}
	ui.loadMaps()
	sc.settle()
	return sc
}

func TestMap(t *testing.T) {
	t.Run("the first page draws the server: its machine, the way of the traffic, and where the data goes", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.shows("chasen", "1 server · 3 apps", "shop-server", "amd64 · 4 cores · 7.6 GB", "! 2 warnings",
			" internet ", " proxy ", "▸ ● blog", "● shop", "3f9a2c1", "shop.example.com shop.com",
			"└ jobs  ● Up 3 hours", "2 db", "backup 10 min ago", "backups-eu", "live replica", "no backup yet",
			"you ", " ssh ", "API   SSH only: not on the web",
			"A moving line is an open path, or data on its way. It does not count requests.")
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

	t.Run("an app that is down has a red cross on its line, and the line stands still", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		shop := `[{"app":"shop","state":"Exited (1) 2 minutes ago","up":false,"version":"3f9a2c1","domains":["shop.com"],"databases":1,"backup":"20261020T120000Z","replica":"live"}]`
		sc.tui.handle(mapEvent{server: "shop-server", overview: shop, load: "API:      SSH only: not on the web\n"})
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

		sc.tui.handle(mapEvent{server: "shop-server", overview: shop, load: "API:      https://api.example.com, on the internet\n"})

		sc.shows("━┳", "┗━", "◆ API", "https://api.example.com, on the internet")
		if strings.Contains(sc.text(), " ssh ") {
			t.Errorf("the map shows the way through SSH too:\n%s", sc.text())
		}
	})

	t.Run("a server from before the map has its apps, and says when its data shows", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		old := `[{"app":"shop","state":"Up 1 hour","up":true,"version":"3f9a2c1"}]`

		sc.tui.handle(mapEvent{server: "shop-server", overview: old})

		sc.shows("● shop", "no domain", "the data shows after the next update of the server")
	})

	t.Run("enter opens the chosen app with its tabs, and esc comes back to the map", func(t *testing.T) {
		sc := openMap(t, "backups-eu")

		sc.press("down", "down", "enter")
		sc.shows("overview   history", "▸ ● shop", "https://shop.com")
		sc.press("esc")

		sc.shows(" internet ", "▸ ● shop")
	})

	t.Run("every server is on the map, and an app of another server opens there", func(t *testing.T) {
		sc := openMap(t, "backups-eu", "")

		sc.shows("2 servers · 6 apps", "shop-server", "lab", "this server only")
		sc.press("end", "enter")

		if sc.tui.server != "lab" || sc.tui.app() != "shop" || sc.tui.home {
			t.Errorf("enter opened %s on %s (map: %v), want shop on lab", sc.tui.app(), sc.tui.server, sc.tui.home)
		}
	})

	t.Run("a server that does not answer says so, and the others still show", func(t *testing.T) {
		sc := openMap(t, "backups-eu", "")
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "broken\x1b]52;c;aGk=\x07\x1b[2J", http.StatusInternalServerError)
		}))
		t.Cleanup(broken.Close)
		client := protocol.Client{URL: broken.URL, Token: "token"}
		next := sc.tui.change
		sc.tui.change = func(server string) (runner, string, error) {
			if server == "lab" {
				return func(ctx context.Context, out io.Writer, args ...string) (int, error) {
					return client.Run(ctx, args[0], args[1:], nil, out)
				}, server, nil
			}
			return next(server)
		}

		sc.press("g")

		sc.shows("It does not answer:", "broken", "shop-server", "backups-eu")
		if view := sc.tui.view(); strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x1b[2J") {
			t.Errorf("an escape code of the server reached the terminal:\n%q", view)
		}
	})

	t.Run("a click chooses an app, and a click on the chosen app opens it", func(t *testing.T) {
		sc := openMap(t, "backups-eu")
		sc.text() // a click finds what the last drawing put under it
		row := -1
		for i, r := range sc.tui.hit.mapRefs {
			if r.app == "shop" {
				row = i
				break
			}
		}

		sc.press("mouse:0:40:" + itoa(sc.tui.hit.mapTop+row))
		sc.shows("▸ ● shop")
		sc.press("mouse:0:40:" + itoa(sc.tui.hit.mapTop+row))

		if sc.tui.home || sc.tui.app() != "shop" {
			t.Errorf("the second click did not open shop: map %v, app %q", sc.tui.home, sc.tui.app())
		}
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
