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
// starts the API again. Then the binary on disk, new or not, starts the proxy
// of its release when another one runs, and the timer moves to the quiet hour
// of the server. A timer runs it each night (see installTimer). It is safe to
// run at any time:
//
//   - The same binary: the binary stays.
//   - A deploy runs now: nothing happens. The next night tries again.
//   - The new API does not answer: the previous binary comes back.
//
// The apps do not restart. The API container does, and the proxy only when
// a release names a new one.
func serverUpdate() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	busy, err := updateBinary(self)
	if err != nil || busy {
		return err
	}
	// The binary on disk knows the proxy of its release; this process may be
	// the one before it.
	out, err := exec.Command(self, "proxy", "update").CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		return fmt.Errorf("the proxy: %s", lastLine(string(out)))
	}
	followQuietHour(self)
	return nil
}

// updateBinary replaces the binary when the release has another one. It
// reports busy when a deploy runs: then nothing else runs tonight.
func updateBinary(self string) (busy bool, err error) {
	base := strings.TrimRight(cmp.Or(os.Getenv("CHASEN_DOWNLOADS"), releases), "/")
	asset := "chasen-server-linux-" + runtime.GOARCH

	sums, err := download(base + "/checksums.txt")
	if err != nil {
		return false, err
	}
	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		if sum, name, ok := strings.Cut(line, "  "); ok && strings.TrimSpace(name) == asset {
			want = sum
		}
	}
	if want == "" {
		return false, fmt.Errorf("the release has no checksum for %s", asset)
	}
	current, err := os.ReadFile(self)
	if err != nil {
		return false, err
	}
	if checksum(current) == want {
		fmt.Println("chasen-server is up to date.")
		return false, nil
	}

	// Do not stop the API in the middle of a deploy.
	if err := os.MkdirAll(filepath.Dir(configPath()), 0755); err != nil {
		return false, err
	}
	// The lock ends with this function: the proxy update after it takes it again.
	held, err := os.OpenFile(filepath.Dir(configPath())+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer held.Close()
	if syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Println("A deploy runs now. The update waits for the next run.")
		return true, nil
	}

	fresh, err := download(base + "/" + asset)
	if err != nil {
		return false, err
	}
	if checksum(fresh) != want {
		return false, errors.New("the checksum of the download is wrong. Nothing changed")
	}
	next, previous := self+".new", self+".previous"
	if err := os.WriteFile(next, fresh, 0755); err != nil {
		return false, err
	}
	defer os.Remove(next)
	if err := os.WriteFile(previous, current, 0755); err != nil {
		return false, err
	}
	// Replace the file in one step: a running process keeps its old copy.
	if err := os.Rename(next, self); err != nil {
		return false, err
	}

	if err := restartAPI(self); err != nil {
		os.Rename(previous, self)
		if back := restartAPI(self); back != nil {
			return false, fmt.Errorf("the new version did not start (%v), and the previous one did not start again (%v). Run: chasen-server setup", err, back)
		}
		return false, fmt.Errorf("the new version did not start: %v. The previous version runs again", err)
	}
	version, _ := exec.Command(self, "version").Output()
	fmt.Printf("Updated to %s. The previous binary is %s\n", strings.TrimSpace(string(version)), previous)
	return false, nil
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

// The timer that keeps a server up to date: once a night, in the quiet hour
// of the server, at a random minute of it, so all servers do not ask for the
// release in the same second. Before an hour is known, it is 03:00.
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
OnCalendar=*-*-* %02d:00:00
RandomizedDelaySec=50min
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
	if err := os.WriteFile(timerPath, fmt.Appendf(nil, updateTimer, timerHour()), 0644); err != nil {
		return err
	}
	if out, err := exec.Command("sh", "-c", "systemctl daemon-reload && systemctl enable --now chasen-update.timer").CombinedOutput(); err != nil {
		return fmt.Errorf("cannot start the update timer: %s", lastLine(string(out)))
	}
	return nil
}

const timerPath = "/etc/systemd/system/chasen-update.timer"

// timerHour is the hour the timer runs at now: the quiet hour it moved to, or
// 03:00.
func timerHour() int {
	data, _ := os.ReadFile(timerPath)
	return timerHourOf(string(data))
}

func timerHourOf(timer string) int {
	var hour int
	for _, line := range strings.Split(timer, "\n") {
		if _, err := fmt.Sscanf(line, "OnCalendar=*-*-* %d:00:00", &hour); err == nil && hour >= 0 && hour < 24 {
			return hour
		}
	}
	return 3
}

// followQuietHour moves the timer to the quiet hour of the server, when the
// log of the proxy knows one and it is another hour. The swap of the proxy,
// which can make visitors wait, then runs when the fewest of them come.
func followQuietHour(self string) {
	if _, err := os.Stat(timerPath); err != nil {
		return // no timer: the update runs by hand
	}
	hour, ok, err := measureQuietHour()
	if err != nil || !ok || hour == timerHour() {
		return
	}
	if os.WriteFile(timerPath, fmt.Appendf(nil, updateTimer, hour), 0644) != nil {
		return
	}
	exec.Command("sh", "-c", "systemctl daemon-reload && systemctl restart chasen-update.timer").Run()
	fmt.Printf("The nightly update runs at %02d:00 from now on: the quiet hour of this server.\n", hour)
}
