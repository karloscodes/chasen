package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// serverQuietHour prints the hour of the day in which the apps of this server
// get the fewest requests: "03:00", in the time zone of the server. It is the
// hour for work that takes the server away, like the reboot for a new kernel.
// The numbers come from the log of the proxy, which has one line for each
// request.
func serverQuietHour() error {
	logs := exec.Command("docker", "logs", "--since", "168h", "matcha-proxy")
	out, err := logs.StdoutPipe()
	if err != nil {
		return err
	}
	logs.Stderr = logs.Stdout // the proxy writes its own messages to the other stream
	if err := logs.Start(); err != nil {
		return err
	}
	hour, ok := quietHour(out, time.Now())
	if err := logs.Wait(); err != nil {
		return errors.New("cannot read the log of the proxy. Does the proxy run? chasen-server setup starts it")
	}
	if !ok {
		return errors.New("the log of the proxy has less than one day of requests: no hour is known yet")
	}
	fmt.Printf("%02d:00\n", hour)
	return nil
}

// quietHour reads the log of the proxy and returns the hour of the day with
// the fewest requests to apps, in the time zone of now. It counts whole days
// only, 7 at most, so every hour of the day has the same weight. It reports
// false when the log covers less than one day or has no request.
func quietHour(log io.Reader, now time.Time) (int, bool) {
	type line struct {
		Time    time.Time `json:"time"`
		Msg     string    `json:"msg"`
		Service string    `json:"service"`
	}
	var first time.Time
	var requests []time.Time
	lines := bufio.NewScanner(log)
	lines.Buffer(make([]byte, 64*1024), 1024*1024)
	for lines.Scan() {
		var l line
		if json.Unmarshal(lines.Bytes(), &l) != nil || l.Time.IsZero() {
			continue
		}
		if first.IsZero() || l.Time.Before(first) {
			first = l.Time
		}
		// A request for a host that no app has is a scanner, not a visitor.
		if l.Msg == "Request" && l.Service != "" {
			requests = append(requests, l.Time)
		}
	}
	if first.IsZero() {
		return 0, false
	}
	days := min(int(now.Sub(first)/(24*time.Hour)), 7)
	since := now.Add(-time.Duration(days) * 24 * time.Hour)

	var byHour [24]int
	total := 0
	for _, t := range requests {
		if !t.Before(since) && !t.After(now) {
			byHour[t.In(now.Location()).Hour()]++
			total++
		}
	}
	if days == 0 || total == 0 {
		return 0, false
	}
	// The quietest hour is the middle of the quietest three hours: one busy
	// hour next to an empty one is not a quiet time.
	best, bestCount := 0, -1
	for hour := range byHour {
		count := byHour[(hour+23)%24] + byHour[hour] + byHour[(hour+1)%24]
		if bestCount < 0 || count < bestCount {
			best, bestCount = hour, count
		}
	}
	return best, true
}
