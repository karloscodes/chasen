package main

import (
	"testing"
	"time"

	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
)

func TestDueCron(t *testing.T) {
	at := func(text string) time.Time {
		moment, err := time.Parse("2006-01-02 15:04:05", text)
		if err != nil {
			t.Fatal(err)
		}
		return moment
	}
	server := func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		if err := saveApp("shop", matcha.AppConfig{Image: "chasen.invalid/shop:1"}); err != nil {
			t.Fatal(err)
		}
		if err := saveSettings("shop", protocol.Settings{Cron: []protocol.CronJob{
			{Schedule: "0 4 * * *", Run: "bin/reset"},
			{Schedule: "*/15 * * * *", Run: "bin/sync"},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	runs := func(due []cronJob) []string {
		var names []string
		for _, d := range due {
			names = append(names, d.app+" "+d.job.Run)
		}
		return names
	}

	t.Run("runs the jobs of the minute, once", func(t *testing.T) {
		server(t)
		dueCron(at("2026-10-03 03:59:00"))

		first, _, _ := dueCron(at("2026-10-03 04:00:00"))
		again, _, _ := dueCron(at("2026-10-03 04:00:30"))

		if got := runs(first); len(got) != 2 || got[0] != "shop bin/reset" || got[1] != "shop bin/sync" {
			t.Errorf("at 04:00 the due jobs are %q, want bin/reset and bin/sync", got)
		}
		if len(again) != 0 {
			t.Errorf("the same minute runs %q again", runs(again))
		}
	})

	t.Run("a restart of the API over the start of a minute skips no job", func(t *testing.T) {
		server(t)
		dueCron(at("2026-10-03 03:58:00"))

		// The API is away at 03:59 and 04:00, and looks again at 04:01.
		due, _, _ := dueCron(at("2026-10-03 04:01:00"))

		if got := runs(due); len(got) != 2 {
			t.Errorf("after the restart the due jobs are %q, want the two of 04:00", got)
		}
	})

	t.Run("after a long stop, it runs only the jobs of now", func(t *testing.T) {
		server(t)
		dueCron(at("2026-10-03 01:00:00"))

		due, _, _ := dueCron(at("2026-10-03 04:05:00"))

		if len(due) != 0 {
			t.Errorf("after three hours away it runs %q, want none: 04:05 matches no job", runs(due))
		}
	})
}
