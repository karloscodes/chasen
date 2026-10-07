package protocol

import (
	"slices"
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	at := func(text string) time.Time {
		moment, err := time.Parse("2006-01-02 15:04 Mon", text)
		if err != nil {
			t.Fatal(err)
		}
		return moment
	}
	cases := []struct {
		schedule string
		runs     []string
		skips    []string
	}{
		{"0 4 * * *", []string{"2026-10-03 04:00 Sat"}, []string{"2026-10-03 04:01 Sat", "2026-10-03 05:00 Sat"}},
		{"*/15 * * * *", []string{"2026-10-03 10:00 Sat", "2026-10-03 10:45 Sat"}, []string{"2026-10-03 10:20 Sat"}},
		{"0 9 * * 1-5", []string{"2026-10-05 09:00 Mon"}, []string{"2026-10-03 09:00 Sat"}},
		{"30 2 1 * *", []string{"2026-11-01 02:30 Sun"}, []string{"2026-11-02 02:30 Mon"}},
		{"0 0 * * 7", []string{"2026-10-04 00:00 Sun"}, []string{"2026-10-05 00:00 Mon"}},
		{"0 0 1 * 1", []string{"2026-10-01 00:00 Thu", "2026-10-05 00:00 Mon"}, []string{"2026-10-06 00:00 Tue"}},
		{"0,30 8-10 * * *", []string{"2026-10-03 08:30 Sat", "2026-10-03 10:00 Sat"}, []string{"2026-10-03 11:00 Sat"}},
		{"@daily", []string{"2026-10-03 00:00 Sat"}, []string{"2026-10-03 01:00 Sat"}},
		{"@hourly", []string{"2026-10-03 13:00 Sat"}, []string{"2026-10-03 13:05 Sat"}},
	}
	for _, c := range cases {
		t.Run(c.schedule, func(t *testing.T) {
			s, err := ParseSchedule(c.schedule)

			if err != nil {
				t.Fatal(err)
			}
			for _, moment := range c.runs {
				if !s.Matches(at(moment)) {
					t.Errorf("it does not run at %s", moment)
				}
			}
			for _, moment := range c.skips {
				if s.Matches(at(moment)) {
					t.Errorf("it runs at %s", moment)
				}
			}
		})
	}

	t.Run("refuses a schedule that cron does not have", func(t *testing.T) {
		for _, bad := range []string{"", "0 4 * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "a * * * *", "@yearly"} {
			if _, err := ParseSchedule(bad); err == nil {
				t.Errorf("%q is valid", bad)
			}
		}
	})
}

func TestCommandWords(t *testing.T) {
	t.Run("splits on spaces, and keeps quoted text together", func(t *testing.T) {
		words, err := CommandWords(`sh -c 'bin/rails runner "Report.send" && echo done'`)

		want := []string{"sh", "-c", `bin/rails runner "Report.send" && echo done`}
		if err != nil || !slices.Equal(words, want) {
			t.Fatalf("CommandWords = %q, %v, want %q", words, err, want)
		}
	})

	t.Run("refuses an empty command, an open quote, and an option first", func(t *testing.T) {
		for _, bad := range []string{"", "   ", `echo "open`, "--rm ls"} {
			if _, err := CommandWords(bad); err == nil {
				t.Errorf("%q is valid", bad)
			}
		}
	})
}

func TestCheckAppName(t *testing.T) {
	t.Run("refuses the names that the server gives its own containers", func(t *testing.T) {
		for _, name := range []string{"api", "chasen-server", "shop-next", "chasen-check-shop", "Shop", "-shop"} {
			if CheckAppName(name) == nil {
				t.Errorf("%q is accepted", name)
			}
		}
	})

	t.Run("accepts an ordinary name", func(t *testing.T) {
		for _, name := range []string{"shop", "next", "shop-nextgen", "lognorth-demo"} {
			if err := CheckAppName(name); err != nil {
				t.Errorf("%q: %v", name, err)
			}
		}
	})
}
