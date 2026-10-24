package engine

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// healthInterval is the wait between two questions to the health path.
// Tests set it shorter.
var healthInterval = time.Second

// WaitHealthy asks the health path of a container once a second, at its
// address in the network, until it answers 200, the container stops, or the
// timeout passes. It returns how long that took, and the problem, or "" for
// a healthy container. `chasen check` and the deploy of a private app use
// it: neither has the proxy to ask.
func WaitHealthy(container string, port int, path string, timeout time.Duration) (time.Duration, string) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		// A health path must not redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	last := "no answer"
	for time.Since(start) < timeout {
		state, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container).Output()
		running, ip, _ := strings.Cut(strings.TrimSpace(string(state)), " ")
		if running != "true" {
			return time.Since(start), "the container stopped"
		}
		if resp, err := client.Get("http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + path); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(start), ""
			}
			last = "it answered " + resp.Status
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				last += ": a health path must not redirect"
			}
		}
		time.Sleep(healthInterval)
	}
	return time.Since(start), fmt.Sprintf("%s on port %d in %d seconds", last, port, int(timeout.Seconds()))
}
