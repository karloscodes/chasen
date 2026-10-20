package main

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
	"github.com/karloscodes/chasen/protocol"
)

// openRepo opens the screen in the git repository of shop: four commits, the
// oldest first, and a mock server that ran the second and then the third. The
// commands of the CLI that the screen runs here go to ran, not to a shell.
func openRepo(t *testing.T) (sc *screen, hashes []string, ran *[][]string) {
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
	for _, subject := range []string{"Start the shop", "Upgrade Rails to 8.1", "Fix login redirect", "Add the invoice export"} {
		git("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", subject)
		hashes = append(hashes, git("rev-parse", "HEAD"))
	}

	server := mock.New(time.Now())
	server.Wait = func(ctx context.Context, d time.Duration) bool { return true }
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	client := protocol.Client{URL: web.URL, Token: server.Token}
	for _, hash := range hashes[1:3] {
		if code, err := client.Run(context.Background(), "deploy", []string{"shop", hash[:7]}, protocol.Settings{Image: "ghcr.io/you/shop:" + hash}.Body(nil), io.Discard); code != 0 || err != nil {
			t.Fatalf("deploy %s: %d, %v", hash[:7], code, err)
		}
	}

	ui := newTUI(func(ctx context.Context, out io.Writer, args ...string) (int, error) {
		return client.Run(ctx, args[0], args[1:], nil, out)
	}, "mock", "shop")
	ui.repo, ui.onServer = true, false
	ui.width, ui.height = 120, 30
	ran = &[][]string{}
	ui.cli = func(ctx context.Context, out io.Writer, args ...string) (int, error) {
		*ran = append(*ran, args)
		fmt.Fprintln(out, "Deployed by the CLI")
		return 0, nil
	}
	sc = &screen{t, ui}
	t.Cleanup(ui.stopLogs)
	ui.loadApps()
	sc.settle()
	return sc, hashes, ran
}

func TestCommitsTab(t *testing.T) {
	t.Run("the app of this directory has its commits, with what each one is on the server", func(t *testing.T) {
		sc, hashes, _ := openRepo(t)

		sc.press("6")

		sc.shows("commits", "$ git log", "d deploy it")
		for i, want := range []string{"not deployed", "kept: R rolls back", "runs now", "not deployed"} {
			if line := lineWith(sc.text(), hashes[i][:7]); !strings.Contains(line, want) {
				t.Errorf("commit %d: %q, want %q", i, line, want)
			}
		}
	})

	t.Run("d on the newest commit builds and deploys it here, after a yes", func(t *testing.T) {
		sc, hashes, ran := openRepo(t)
		sc.press("6")

		sc.press("d")
		sc.shows("Deploy " + hashes[3][:7] + " Add the invoice export to shop? chasen builds its image here")
		sc.press("y")

		if len(*ran) != 1 || strings.Join((*ran)[0], " ") != "deploy" {
			t.Errorf("the CLI ran %v, want deploy", *ran)
		}
		sc.shows("$ chasen deploy", "Deployed by the CLI")
	})

	t.Run("d on an older commit deploys its image from the registry, and no answer runs nothing", func(t *testing.T) {
		sc, hashes, ran := openRepo(t)
		sc.press("6", "end")

		sc.press("d")
		sc.shows("Deploy " + hashes[0][:7] + " Start the shop to shop from its image in the registry?")
		sc.press("n")
		if len(*ran) != 0 {
			t.Fatalf("the CLI ran %v with no yes", *ran)
		}
		sc.press("d", "y")

		if len(*ran) != 1 || strings.Join((*ran)[0], " ") != "deploy --tag "+hashes[0] {
			t.Errorf("the CLI ran %v, want deploy --tag %s", *ran, hashes[0])
		}
	})

	t.Run("R on a kept commit rolls back to it, and d on the one that runs says so", func(t *testing.T) {
		sc, hashes, ran := openRepo(t)
		sc.press("6", "down")

		sc.press("d")
		sc.shows(hashes[2][:7] + " runs now.")
		sc.press("down", "R")
		sc.shows("Roll back shop to " + hashes[1][:7] + " Upgrade Rails to 8.1? The server kept its image")
		sc.press("y")

		sc.shows("rollback shop " + hashes[1][:7])
		if len(*ran) != 0 {
			t.Errorf("a rollback went through the CLI: %v", *ran)
		}
	})

	t.Run("o opens the page of the commit on its forge", func(t *testing.T) {
		sc, hashes, _ := openRepo(t)
		opened := ""
		sc.tui.openURL = func(page string) error { opened = page; return nil }

		sc.press("6", "o")

		if opened != "https://github.com/you/shop/commit/"+hashes[3] {
			t.Errorf("o opened %q", opened)
		}
	})

	t.Run("only the app of this directory, on its server, has the tab", func(t *testing.T) {
		sc, _, _ := openRepo(t)

		sc.press("up", "6")
		if strings.Contains(lineWith(sc.text(), "overview"), "commits") || sc.tui.tab == tabCommits {
			t.Errorf("lognorth has the commits of this directory:\n%s", sc.text())
		}
		sc.press("down")
		sc.tui.repoServer = "another"
		if strings.Contains(lineWith(sc.text(), "overview"), "commits") {
			t.Errorf("shop on a server that this directory does not deploy to has its commits:\n%s", sc.text())
		}
	})
}
