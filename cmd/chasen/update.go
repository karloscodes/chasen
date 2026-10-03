package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// The CLI updates itself for a person at a terminal: once a day it looks for
// a newer release, and after a command that worked it installs it, with the
// check of the checksum. Never in CI, where a program that changes between
// two commands is a surprise, and never for a development build.
// CHASEN_NO_UPDATE_CHECK=1 turns it off. `chasen update` installs it now.

const (
	releaseFiles  = "https://github.com/karloscodes/chasen/releases/latest/download"
	latestRelease = "https://api.github.com/repos/karloscodes/chasen/releases/latest"
)

// update replaces this program with the newest release, after it checked the
// checksum of the download.
func update() error {
	// A build from source is newer than any release, or different on purpose.
	if !newer(version, "v0.0.0") {
		return fmt.Errorf("this chasen is a development build (%s), not a release. To install the newest release: curl -fsSL https://chasenhq.com/cli | sh", version)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	changed, err := updateFile(self, strings.TrimRight(cmp.Or(os.Getenv("CHASEN_DOWNLOADS"), releaseFiles), "/"))
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("chasen is up to date:", version)
		return nil
	}
	installed, _ := exec.Command(self, "version").Output()
	fmt.Printf("Updated: %s (it was %s)\n", strings.TrimSpace(string(installed)), version)
	return nil
}

// errNeedsSudo means the file of chasen belongs to another user: root, when
// the install used sudo.
var errNeedsSudo = errors.New("Run: sudo chasen update")

// autoUpdate installs the release latest after a command, and says so in
// one line. When it cannot, it says what to run.
func autoUpdate(latest string) {
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "\nUpdating chasen to %s...\n", latest)
	changed, err := updateFile(self, strings.TrimRight(cmp.Or(os.Getenv("CHASEN_DOWNLOADS"), releaseFiles), "/"))
	switch {
	case errors.Is(err, errNeedsSudo):
		fmt.Fprintf(os.Stderr, "chasen %s is out. You have %s, in %s, which needs sudo. Run: sudo chasen update\n", latest, version, filepath.Dir(self))
	case err != nil:
		fmt.Fprintf(os.Stderr, "chasen %s is out, and the update failed: %v. Run: chasen update\n", latest, err)
	case changed:
		fmt.Fprintf(os.Stderr, "Updated chasen to %s (it was %s).\n", latest, version)
	}
}

// updateFile replaces the program file with the one of the release at base.
// changed is false when the file is that release already.
func updateFile(self, base string) (changed bool, err error) {
	asset := "chasen-" + runtime.GOOS + "-" + runtime.GOARCH

	sums, err := download(base+"/checksums.txt", time.Minute)
	if err != nil {
		return false, err
	}
	want := ""
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
		return false, nil
	}

	fresh, err := download(base+"/"+asset, 5*time.Minute)
	if err != nil {
		return false, err
	}
	if checksum(fresh) != want {
		return false, errors.New("the checksum of the download is wrong. Nothing changed")
	}
	// Write the new file next to the old one, then replace it in one step.
	next := self + ".new"
	if err := os.WriteFile(next, fresh, 0755); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return false, fmt.Errorf("%s belongs to another user. %w", self, errNeedsSudo)
		}
		return false, err
	}
	if err := os.Rename(next, self); err != nil {
		os.Remove(next)
		return false, err
	}
	return true, nil
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func download(url string, limit time.Duration) ([]byte, error) {
	client := http.Client{Timeout: limit}
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

// newer reports if the release a is newer than the release b: v0.4.0 is
// newer than v0.3.2. A name that is not a release, like dev, is never newer
// and never older.
func newer(a, b string) bool {
	parse := func(v string) (parts [3]int, ok bool) {
		fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
		if len(fields) != 3 {
			return parts, false
		}
		for i, field := range fields {
			n, err := strconv.Atoi(field)
			if err != nil {
				return parts, false
			}
			parts[i] = n
		}
		return parts, true
	}
	x, okX := parse(a)
	y, okY := parse(b)
	if !okX || !okY {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// releaseCheck is what the last look at the releases found.
type releaseCheck struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

func releaseCheckPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "chasen", "release.json")
}

// newerRelease returns the name of a release that is newer than this program,
// or "". It asks GitHub at most once a day, and only for a person at a
// terminal: not in CI, not in a pipe, and not for a development build.
func newerRelease() string {
	if !term.IsTerminal(int(os.Stderr.Fd())) || os.Getenv("CI") != "" || os.Getenv("CHASEN_NO_UPDATE_CHECK") != "" {
		return ""
	}
	return newerReleaseFrom(cmp.Or(os.Getenv("CHASEN_LATEST_RELEASE"), latestRelease))
}

func newerReleaseFrom(address string) string {
	var last releaseCheck
	if data, err := os.ReadFile(releaseCheckPath()); err == nil {
		json.Unmarshal(data, &last)
	}
	if time.Since(last.Checked) > 24*time.Hour {
		// Keep the time of this look also when it fails, so a machine with no
		// network does not wait on every command.
		last.Checked = time.Now()
		if data, err := download(address, 2*time.Second); err == nil {
			var release struct {
				Tag string `json:"tag_name"`
			}
			if json.Unmarshal(data, &release) == nil && release.Tag != "" {
				last.Latest = release.Tag
			}
		}
		if data, err := json.Marshal(last); err == nil {
			os.MkdirAll(filepath.Dir(releaseCheckPath()), 0755)
			os.WriteFile(releaseCheckPath(), data, 0644)
		}
	}
	if newer(last.Latest, version) {
		return last.Latest
	}
	return ""
}
