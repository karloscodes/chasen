package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// proxyLog makes the log of the proxy: one line for each request to an app
// at each of the times.
func proxyLog(times ...time.Time) string {
	var log strings.Builder
	for _, t := range times {
		fmt.Fprintf(&log, `{"time":%q,"level":"INFO","msg":"Request","host":"shop.example.com","status":200,"service":"shop"}`+"\n", t.Format(time.RFC3339Nano))
	}
	return log.String()
}

func TestQuietHour(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	// Three days of a shop: visitors in every hour of the day but 02:00 to 04:59.
	var busy []time.Time
	for day := 1; day <= 3; day++ {
		for hour := range 24 {
			if hour < 2 || hour > 4 {
				busy = append(busy, now.Add(-time.Duration(day)*24*time.Hour).Truncate(24*time.Hour).Add(time.Duration(hour)*time.Hour+30*time.Minute))
			}
		}
	}

	t.Run("it is the middle of the hours with the fewest requests", func(t *testing.T) {
		hour, ok := quietHour(strings.NewReader(proxyLog(busy...)), now)

		if !ok || hour != 3 {
			t.Fatalf("got %02d:00 (known: %v), want 03:00", hour, ok)
		}
	})

	t.Run("it is in the time zone of the server", func(t *testing.T) {
		tokyo := time.FixedZone("JST", 9*60*60)

		hour, ok := quietHour(strings.NewReader(proxyLog(busy...)), now.In(tokyo))

		if !ok || hour != 12 {
			t.Fatalf("got %02d:00 (known: %v), want 12:00: 03:00 UTC in Tokyo", hour, ok)
		}
	})

	t.Run("a request for a host that no app has does not count", func(t *testing.T) {
		scanner := fmt.Sprintf(`{"time":%q,"msg":"Request","host":"203.0.113.9","status":404,"service":""}`+"\n", now.Add(-30*time.Hour).Format(time.RFC3339Nano))
		log := strings.Repeat(scanner, 50) + proxyLog(busy...)

		hour, ok := quietHour(strings.NewReader(log), now)

		if !ok || hour != 3 {
			t.Fatalf("got %02d:00 (known: %v), want 03:00", hour, ok)
		}
	})

	t.Run("a log of less than one day gives no hour", func(t *testing.T) {
		recent := slices.DeleteFunc(slices.Clone(busy), func(t time.Time) bool { return now.Sub(t) > 20*time.Hour })

		_, ok := quietHour(strings.NewReader(proxyLog(recent...)), now)

		if ok {
			t.Fatal("got an hour from 20 hours of log: the hours that the log does not cover look quiet")
		}
	})

	t.Run("a log with no request gives no hour", func(t *testing.T) {
		log := fmt.Sprintf(`{"time":%q,"level":"INFO","msg":"Server started"}`+"\n", now.Add(-72*time.Hour).Format(time.RFC3339Nano))

		_, ok := quietHour(strings.NewReader(log), now)

		if ok {
			t.Fatal("got an hour from a log with no request")
		}
	})
}
