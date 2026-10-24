package engine

import (
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// UnhealthyError is a new container that did not pass its health check. The
// engine removes the container; these are the facts that it left, so the
// caller can say why it failed.
type UnhealthyError struct {
	Err       error  // what the health check said
	Logs      string // the last lines of the output of the container
	Status    string // the state of Docker: "running", "restarting", "exited"
	ExitCode  int    // the exit code of its last run, when it stopped
	OOMKilled bool   // Docker stopped it because it used more than its memory
	Restarts  int    // how often Docker started it again
	Listening []int  // the TCP ports it listened on, when it was running
}

func (e *UnhealthyError) Error() string { return "the health check failed: " + e.Err.Error() }
func (e *UnhealthyError) Unwrap() error { return e.Err }

// unhealthy collects the facts of a container that failed its health check.
// Each fact is best effort: a fact that Docker cannot give stays empty.
func (m *Engine) unhealthy(container string, err error) *UnhealthyError {
	failed := &UnhealthyError{Err: err}
	if out, err := exec.Command("docker", "logs", "--tail", "40", container).CombinedOutput(); err == nil {
		failed.Logs = strings.TrimRight(string(out), "\n")
	}
	if out, err := m.runDocker("inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}} {{.RestartCount}}", container); err == nil {
		if f := strings.Fields(out); len(f) == 4 {
			failed.Status = f[0]
			failed.ExitCode, _ = strconv.Atoi(f[1])
			failed.OOMKilled = f[2] == "true"
			failed.Restarts, _ = strconv.Atoi(f[3])
		}
	}
	// The ports it listens on, read from the kernel in its network: a short
	// container of the same image joins that network and prints the table.
	// An image with no cat gives nothing, and that is fine.
	if failed.Status == "running" {
		if out, err := m.runDocker("run", "--rm", "--network", "container:"+container, "--entrypoint", "cat", m.config.AppImage, "/proc/net/tcp", "/proc/net/tcp6"); err == nil {
			failed.Listening = listeningPorts(out)
		}
	}
	return failed
}

// listeningPorts reads the TCP ports in the LISTEN state from the text of
// /proc/net/tcp and /proc/net/tcp6.
func listeningPorts(table string) []int {
	var ports []int
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		// sl local_address rem_address st ...; the state 0A is LISTEN.
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		_, hex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		if port, err := strconv.ParseUint(hex, 16, 16); err == nil && !slices.Contains(ports, int(port)) {
			ports = append(ports, int(port))
		}
	}
	slices.Sort(ports)
	return ports
}
