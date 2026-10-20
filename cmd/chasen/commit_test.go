package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCommitOfADeploy(t *testing.T) {
	const hash = "3f9a2c1d5e8b7a6f0e1d2c3b4a5968778695a4b3"

	t.Run("the page of a commit comes from the origin, on each forge", func(t *testing.T) {
		for origin, want := range map[string]string{
			"git@github.com:you/shop.git":             "https://github.com/you/shop/commit/" + hash,
			"https://github.com/you/shop.git":         "https://github.com/you/shop/commit/" + hash,
			"ssh://git@gitlab.com:2222/acme/web/shop": "https://gitlab.com/acme/web/shop/-/commit/" + hash,
			"https://codeberg.org/you/shop":           "https://codeberg.org/you/shop/commit/" + hash,
			"git@bitbucket.org:you/shop.git":          "https://bitbucket.org/you/shop/commits/" + hash,
			"/home/you/shop.git":                      "",
			"":                                        "",
		} {
			if got := commitURL(origin, hash); got != want {
				t.Errorf("commitURL(%q) = %q, want %q", origin, got, want)
			}
		}
	})

	t.Run("the token in an origin never goes into the page", func(t *testing.T) {
		got := commitURL("https://x-access-token:ghs_secret@github.com/you/shop.git", hash)

		if got != "https://github.com/you/shop/commit/"+hash || strings.Contains(got, "ghs_secret") {
			t.Errorf("commitURL = %q", got)
		}
	})

	t.Run("git gives the subject of the commit, by its short hash", func(t *testing.T) {
		t.Chdir(t.TempDir())
		for _, args := range [][]string{
			{"init", "-q"},
			{"remote", "add", "origin", "git@github.com:you/shop.git"},
			{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "Fix login redirect"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s", args, out)
			}
		}
		full, _ := exec.Command("git", "rev-parse", "HEAD").Output()
		head := strings.TrimSpace(string(full))

		commit := commitOf(head[:7])

		if commit == nil || commit.Subject != "Fix login redirect" || commit.URL != "https://github.com/you/shop/commit/"+head {
			t.Errorf("commitOf = %+v", commit)
		}
		if commitOf("abcdef0") != nil {
			t.Error("a hash that git does not know has a commit")
		}
	})
}
