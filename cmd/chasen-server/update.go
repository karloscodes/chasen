package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// The newest release of the public repository. CHASEN_DOWNLOADS replaces it, for a test.
const releases = "https://github.com/karloscodes/chasen/releases/latest/download"

// serverUpdate replaces this binary with the one of the newest release and
// starts the API again. A timer runs it each night (see installTimer). It is
// safe to run at any time:
//
//   - The same binary: nothing happens.
//   - A deploy runs now: nothing happens. The next night tries again.
//   - The new API does not answer: the previous binary comes back.
//
// The apps do not restart. Only the API container does.
func serverUpdate() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	base := strings.TrimRight(cmp.Or(os.Getenv("CHASEN_DOWNLOADS"), releases), "/")
	asset := "chasen-server-linux-" + runtime.GOARCH

	sums, err := download(base + "/checksums.txt")
	if err != nil {
		return err
	}
	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		if sum, name, ok := strings.Cut(line, "  "); ok && strings.TrimSpace(name) == asset {
			want = sum
		}
	}
	if want == "" {
		return fmt.Errorf("the release has no checksum for %s", asset)
	}
	current, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if checksum(current) == want {
		fmt.Println("chasen-server is up to date.")
		return nil
	}

	// Do not stop the API in the middle of a deploy.
	if err := os.MkdirAll(filepath.Dir(configPath()), 0755); err != nil {
		return err
	}
	busy, err := os.OpenFile(filepath.Dir(configPath())+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer busy.Close()
	if syscall.Flock(int(busy.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Println("A deploy runs now. The update waits for the next run.")
		return nil
	}

	fresh, err := download(base + "/" + asset)
	if err != nil {
		return err
	}
	if checksum(fresh) != want {
		return errors.New("the checksum of the download is wrong. Nothing changed")
	}
	next, previous := self+".new", self+".previous"
	if err := os.WriteFile(next, fresh, 0755); err != nil {
		return err
	}
	defer os.Remove(next)
	if err := os.WriteFile(previous, current, 0755); err != nil {
		return err
	}
	// Replace the file in one step: a running process keeps its old copy.
	if err := os.Rename(next, self); err != nil {
		return err
	}

	if err := restartAPI(self); err != nil {
		os.Rename(previous, self)
		if back := restartAPI(self); back != nil {
			return fmt.Errorf("the new version did not start (%v), and the previous one did not start again (%v). Run: chasen-server setup", err, back)
		}
		return fmt.Errorf("the new version did not start: %v. The previous version runs again", err)
	}
	version, _ := exec.Command(self, "version").Output()
	fmt.Printf("Updated to %s. The previous binary is %s\n", strings.TrimSpace(string(version)), previous)
	return nil
}

// restartAPI runs the setup of the binary on disk, which starts the API
// container again, and waits until the API answers.
func restartAPI(self string) error {
	if out, err := exec.Command(self, "setup").CombinedOutput(); err != nil {
		return fmt.Errorf("setup failed: %s", lastLine(string(out)))
	}
	for range 30 {
		if ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", agentContainer); err == nil && ip != "" {
			if resp, err := http.Get("http://" + ip + ":" + apiPort + "/up"); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("the API did not answer in 30 seconds")
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func download(url string) ([]byte, error) {
	client := http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// The timer that keeps a server up to date: once a night, at a random minute,
// so all servers do not ask for the release in the same second.
const (
	updateService = `[Unit]
Description=Update chasen-server to the newest release
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s update
`
	updateTimer = `[Unit]
Description=Update chasen-server each night

[Timer]
OnCalendar=*-*-* 03:00:00
RandomizedDelaySec=2h
Persistent=true

[Install]
WantedBy=timers.target
`
)

// installTimer turns the nightly update on or off, on a server with systemd.
// `chasen-server settings auto_update off` turns it off.
func installTimer(self string, on bool) error {
	if _, err := os.Stat("/run/systemd/system"); err != nil || root() != "" {
		return nil // no systemd, or a test: nothing to install
	}
	if !on {
		exec.Command("systemctl", "disable", "--now", "chasen-update.timer").Run()
		return nil
	}
	if err := os.WriteFile("/etc/systemd/system/chasen-update.service", fmt.Appendf(nil, updateService, self), 0644); err != nil {
		return err
	}
	if err := os.WriteFile("/etc/systemd/system/chasen-update.timer", []byte(updateTimer), 0644); err != nil {
		return err
	}
	if out, err := exec.Command("sh", "-c", "systemctl daemon-reload && systemctl enable --now chasen-update.timer").CombinedOutput(); err != nil {
		return fmt.Errorf("cannot start the update timer: %s", lastLine(string(out)))
	}
	return nil
}
