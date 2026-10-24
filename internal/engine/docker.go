package engine

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries = 3
)

// runDocker executes a docker command and returns output.
func (m *Engine) runDocker(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

// ensureDocker installs Docker if not present.
func (m *Engine) ensureDocker() error {
	// Check if already installed
	if _, err := m.runDocker("version"); err == nil {
		return nil
	}

	// Install Docker
	cmd := exec.Command("bash", "-c", "curl -fsSL https://get.docker.com | sh")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker install failed: %w\n%s", err, string(out))
	}

	// Start and enable
	for _, c := range [][]string{
		{"systemctl", "start", "docker"},
		{"systemctl", "enable", "docker"},
	} {
		if err := exec.Command(c[0], c[1:]...).Run(); err != nil {
			return fmt.Errorf("%s failed: %w", c[1], err)
		}
	}

	return nil
}

// createNetwork creates the Docker network if it doesn't exist.
func (m *Engine) createNetwork() error {
	name := m.NetworkName()
	if _, err := m.runDocker("network", "inspect", name); err == nil {
		return nil // already exists
	}

	_, err := m.runDocker("network", "create", name)
	return err
}

// pullRetryDelay is the base wait between pull attempts. Tests set it to 0.
var pullRetryDelay = 2 * time.Second

// logLimit caps the log of a container at 3 files of 10 MB. Without a limit,
// Docker keeps every line, and the logs of a busy app fill the disk.
var logLimit = []string{"--log-opt", "max-size=10m", "--log-opt", "max-file=3"}

// pullImage pulls one image, retrying with a growing wait.
func (m *Engine) pullImage(image string) error {
	for i := 0; i < maxRetries; i++ {
		if _, err := m.runDocker("pull", image); err == nil {
			return nil
		} else if i == maxRetries-1 {
			return fmt.Errorf("failed to pull %s after %d retries: %w", image, maxRetries, err)
		}
		time.Sleep(time.Duration(i+1) * pullRetryDelay)
	}
	return nil
}

// isRunning checks if a container is running.
func (m *Engine) isRunning(name string) bool {
	out, err := m.runDocker("ps", "-q", "--filter", "name=^"+name+"$")
	return err == nil && strings.TrimSpace(out) != ""
}

// findActiveContainer returns the name of the currently running app container.
// Checks both "{name}" and "{name}-next" to find which is active.
// Returns the base name as fallback (first deploy or neither running).
func (m *Engine) findActiveContainer() string {
	base := m.config.Name
	next := base + "-next"

	if m.isRunning(next) {
		return next
	}
	if m.isRunning(base) {
		return base
	}
	return base
}

// nextContainerName returns the alternate container name for zero-downtime swap.
// "{name}" ↔ "{name}-next"
func (m *Engine) nextContainerName(current string) string {
	base := m.config.Name
	if current == base {
		return base + "-next"
	}
	return base
}

// stopAndRemove stops and removes a container.
func (m *Engine) stopAndRemove(name string) error {
	m.runDocker("stop", name)
	m.runDocker("rm", "-f", name)
	return nil
}

// deployApp deploys an app container.
func (m *Engine) deployApp(name string) error {
	// Remove stale container with same name (e.g. leftover -next from failed deploy)
	m.runDocker("rm", "-f", name)

	args := []string{
		"run", "-d",
		"--name", name,
		"--network", m.NetworkName(),
	}

	// A private app listens on an address of the server, not on the proxy.
	if m.listen != "" {
		args = append(args, "-p", m.listen+":"+strconv.Itoa(m.config.AppPort))
	}

	// Mount volumes from config (resolved from container paths)
	for _, v := range m.resolveVolumes(m.config.Volumes) {
		hostPath := strings.SplitN(v, ":", 2)[0]
		os.MkdirAll(hostPath, 0755)
		args = append(args, "-v", v)
	}

	// Env vars from the record of the app
	app, _ := m.record()
	prefix := m.EnvPrefix()
	for k, v := range app.Env {
		args = append(args, "-e", k+"="+v)
		// Backwards compat: also set prefixed version for known keys
		if k == "PRIVATE_KEY" {
			args = append(args, "-e", prefix+"_PRIVATE_KEY="+v)
		}
	}

	// Auto-generated env vars
	args = append(args,
		"-e", fmt.Sprintf("%s_DOMAIN=%s", prefix, m.domain),
		"-e", fmt.Sprintf("%s_APP_PORT=%d", prefix, m.config.AppPort),
		"-e", fmt.Sprintf("%s_ENV=production", prefix),
	)

	// A stop sends SIGTERM, then SIGKILL after 30 seconds, like the job roles
	// of Kamal: an app that runs its jobs in its own process gets the time to
	// finish one. An app that stops sooner loses nothing: docker waits only
	// until it exits.
	// No raw sockets: an app on the shared network must not spoof the
	// addresses of its neighbours and read their traffic. Docker answers
	// ping without them.
	args = append(args,
		"--cap-drop", "NET_RAW",
		"--memory="+cmp.Or(app.Memory, "512m"),
		"--stop-timeout", "30",
		"--restart", "unless-stopped",
	)
	args = append(args, logLimit...)
	args = append(args, m.config.AppImage)

	_, err := m.runDocker(args...)
	return err
}

// deployProxy starts the kamal-proxy container.
func (m *Engine) deployProxy() error {
	name := m.ProxyContainerName()
	m.stopAndRemove(name)

	proxyDataDir := "/var/matcha/proxy"
	if m.config.DataDirBase != "" {
		proxyDataDir = m.config.DataDirBase + "/proxy"
	}
	os.MkdirAll(proxyDataDir, 0755)
	// kamal-proxy runs as uid 1001 and needs write access for ACME certs
	exec.Command("chown", "1001:1001", proxyDataDir).Run()

	args := []string{
		"run", "-d",
		"--name", name,
		"--network", m.NetworkName(),
		"-p", "80:80",
		"-p", "443:443",
		"-p", "443:443/udp",
		"-v", proxyDataDir + ":/home/kamal-proxy/.config/kamal-proxy",
		"--memory=128m",
		"--restart", "unless-stopped",
	}
	args = append(args, logLimit...)
	args = append(args, m.config.ProxyImage)

	_, err := m.runDocker(args...)
	return err
}

// StopApp stops and removes the app container.
// StopApp stops and removes all app containers (both base and -next).
func (m *Engine) StopApp() error {
	base := m.config.Name
	m.stopAndRemove(base)
	m.stopAndRemove(base + "-next")
	return nil
}
