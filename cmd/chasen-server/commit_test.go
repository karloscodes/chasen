package main

import (
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
)

func TestCommitOfAnEntry(t *testing.T) {
	t.Run("the subject of the commit goes into the entry of the deploy, as one line of text", func(t *testing.T) {
		body := strings.NewReader(`{"env":{},"commit":{"subject":"Fix  login\u001b[31m\tredirect","url":"https://github.com/you/shop/commit/3f9a2c1"}}` + "\nthe files\n")

		got := withSubject("deploy 3f9a2c1", body)

		if got != "deploy 3f9a2c1 Fix login redirect" {
			t.Errorf("withSubject = %q", got)
		}
		if body.Len() != int(body.Size()) {
			t.Error("the body is not read again from its start")
		}
	})

	t.Run("a long subject is cut, and a deploy with no commit keeps its entry", func(t *testing.T) {
		long := strings.Repeat("word ", 30)

		got := withSubject("deploy 3f9a2c1", strings.NewReader(`{"commit":{"subject":"`+long+`"}}`+"\n"))

		if n := len([]rune(got)); n != len("deploy 3f9a2c1 ")+60 || !strings.HasSuffix(got, "…") {
			t.Errorf("withSubject = %q (%d runes)", got, n)
		}
		if got := withSubject("deploy 3f9a2c1", strings.NewReader(`{"env":{}}`+"\n")); got != "deploy 3f9a2c1" {
			t.Errorf("withSubject with no commit = %q", got)
		}
	})

	t.Run("only an https page with no login is a link", func(t *testing.T) {
		for link, want := range map[string]bool{
			"https://github.com/you/shop/commit/3f9a2c1": true,
			"http://github.com/you/shop/commit/3f9a2c1":  false,
			"https://token@github.com/you/shop":          false,
			"https://github.com/you/shop\nCommit: x":     false,
			"javascript:alert(1)":                        false,
		} {
			if got := commitLink(&protocol.Commit{URL: link}) != ""; got != want {
				t.Errorf("commitLink(%q) = %v, want %v", link, got, want)
			}
		}
		if commitLink(nil) != "" {
			t.Error("no commit has a link")
		}
	})
}
