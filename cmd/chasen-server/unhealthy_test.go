package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/karloscodes/matcha"
)

func TestUnhealthyReport(t *testing.T) {
	app := matcha.AppConfig{Port: 8080, HealthPath: "/up"}
	proxy := errors.New("target failed to become healthy")

	t.Run("an app that ran out of memory is told to ask for more", func(t *testing.T) {
		report := unhealthyReport(app, &matcha.UnhealthyError{Err: proxy, Status: "restarting", OOMKilled: true, Restarts: 2})

		if !strings.Contains(report, "it used more than its memory, 512m") || !strings.Contains(report, "memory: 1g") {
			t.Errorf("report = %q", report)
		}
	})

	t.Run("an app that crashes shows how it ended and its last lines", func(t *testing.T) {
		report := unhealthyReport(app, &matcha.UnhealthyError{Err: proxy, Status: "restarting", ExitCode: 1, Restarts: 2, Logs: "KeyError: 'SMTP_ADDRESS'"})

		if !strings.Contains(report, "It stopped with exit code 1, 3 times") || !strings.Contains(report, "\n  KeyError: 'SMTP_ADDRESS'") {
			t.Errorf("report = %q", report)
		}
	})

	t.Run("an app on another port is told which port to declare", func(t *testing.T) {
		report := unhealthyReport(app, &matcha.UnhealthyError{Err: proxy, Status: "running", Listening: []int{3000}})

		if !strings.Contains(report, "It listens on port 3000, not on 8080. Add EXPOSE 3000 to the Dockerfile, or port: 3000 to chasen.yml") {
			t.Errorf("report = %q", report)
		}
	})

	t.Run("an app on the right port is told what the health path must do", func(t *testing.T) {
		report := unhealthyReport(app, &matcha.UnhealthyError{Err: proxy, Status: "running", Listening: []int{8080}})

		if !strings.Contains(report, "/up does not answer 200") || !strings.Contains(report, "no redirect to https") {
			t.Errorf("report = %q", report)
		}
	})
}
