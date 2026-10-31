package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/karloscodes/chasen/internal/engine"
)

// The image scan. Once a night, when the owner has turned it on, and whenever
// someone runs `chasen scan`. Trivy runs as a container that starts, reads
// one image, and exits. chasen-server stays a static binary.
//
// The container does not get the Docker socket and does not get a network
// while it scans. This process exports the image that is running to a tar,
// which it can do because it already talks to Docker, and mounts that file
// read-only. The vulnerability database is downloaded first, by a second
// container that has a network and nothing else: no tar, no socket. The
// database stays in a cache directory for the next night.
//
// Both containers are limited to one CPU and 1 GB, so a scan on a small
// server cannot take the machine from the apps.

const (
	// scanImage is Trivy v0.76.1, pinned by the digest of its index so the
	// server pulls the architecture it runs. Bump it by hand after reading
	// the release notes of Trivy.
	scanImage = "ghcr.io/aquasecurity/trivy@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c"

	// The nightly scan is 04:15 UTC: after the quiet hour the server uses for
	// its own update, and before the 05:30 auto-update, so the two do not run
	// together.
	scanHour   = 4
	scanMinute = 15

	// The scanner runs as a user that is not root. The cache and the work
	// directory are owned by this user. 65532 is nobody in the image's
	// number space; the name does not need to exist.
	scanUID = 65532
)

// scanDue reports that the minutes from..to hold the nightly scan.
func scanDue(from, to time.Time) bool {
	for minute := from.UTC(); !minute.After(to.UTC()); minute = minute.Add(time.Minute) {
		if minute.Hour() == scanHour && minute.Minute() == scanMinute {
			return true
		}
	}
	return false
}

// nightlyScan runs the scan when the owner has turned it on. Findings are
// an exit code of 1 and the report already holds them, so they are not an
// error in the log.
func nightlyScan(self string) {
	cfg, err := loadServerConfig()
	if err != nil || !cfg.Scan {
		return
	}
	if err := run(self, "scan"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return
		}
		log.Print("scan: ", err)
	}
}

func serverScan(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: chasen-server scan [app]")
	}
	only := ""
	if len(args) == 1 {
		only = args[0]
		if only != "proxy" {
			if err := checkAppName(only); err != nil {
				return err
			}
			if _, err := loadApp(only); err != nil {
				return err
			}
		}
	}

	dir, lock, err := lockScan()
	if err != nil {
		return err
	}
	defer lock.Close()

	targets, err := scanTargets(only)
	if err != nil {
		// A named app that is not running is this command, not the nightly
		// report: the other apps keep the findings they have.
		if only != "" && strings.Contains(err.Error(), "is not running") {
			fmt.Printf("The scan did not run: %s\n", err.Error())
			os.Exit(2)
		}
		report := scanReport{Error: err.Error()}
		if saveErr := storeScan(report); saveErr != nil {
			fmt.Printf("The report was not saved: %s\n", oneLine(saveErr.Error()))
		}
		writeScan(os.Stdout, report)
		os.Exit(2)
	}
	cfg, _ := loadServerConfig()
	report := performScan(dir, targets, cfg.ScanIgnore)
	// Print this run. The stored report keeps the other apps when the
	// command named one of them, and the webhook compares that whole set.
	writeScan(os.Stdout, report)
	if err := storeScan(mergeScan(report, only)); err != nil {
		fmt.Printf("The report was not saved: %s\n", oneLine(err.Error()))
		os.Exit(2)
	}
	if code := scanExit(report); code != 0 {
		os.Exit(code)
	}
	return nil
}

// lockScan holds the scan directory so two scans do not export images at
// once. The lock drops when the process exits.
func lockScan() (string, *os.File, error) {
	dir := root() + "/var/matcha/.chasen-scan"
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	// The directory is under /var/matcha, which the API container and the
	// host share at the same path. docker save writes the tar here, and the
	// daemon mounts that host path into Trivy. A file in the container's own
	// /tmp would not be the path the daemon sees.
	file, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return "", nil, errors.New("a scan is already running")
	}
	// A scan that was killed can leave its export behind.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "image-*.tar")); len(leftovers) > 0 {
		for _, name := range leftovers {
			os.Remove(name)
		}
	}
	return dir, file, nil
}

// performScan exports each running image and scans the tar. A failure of the
// scanner itself is report.Error. A failure of one image is that target's Error.
func performScan(dir string, targets []scanTarget, ignore []string) scanReport {
	if len(targets) == 0 {
		return scanReport{ScannedAt: time.Now().UTC()}
	}
	if err := prepareScanner(dir); err != nil {
		return scanReport{Error: err.Error()}
	}
	report := scanReport{ScannedAt: time.Now().UTC()}
	for _, target := range targets {
		findings, unfixed, err := scanOne(dir, target.Image, ignore)
		target.Findings, target.Unfixed = findings, unfixed
		if err != nil {
			target.Error = err.Error()
		}
		report.Targets = append(report.Targets, target)
	}
	return report
}

// imageID is a Docker image id. Nothing else is passed to docker save.
var imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// scanIgnoreID is a vulnerability id: CVE-2024-5535, GO-2026-6860, GHSA-....
var scanIgnoreID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,80}$`)

// parseScanIgnore reads the comma-separated ids of `settings scan_ignore`.
// An empty value clears the list.
func parseScanIgnore(value string) ([]string, error) {
	var ids []string
	for _, id := range strings.Split(value, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !scanIgnoreID.MatchString(id) {
			return nil, fmt.Errorf("%q is not a vulnerability id", id)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// scanTargets lists the images that are running. An app and its jobs
// container share an image, so the jobs container is a second scan only when
// its image is a different one.
func scanTargets(only string) ([]scanTarget, error) {
	images, err := runningImages()
	if err != nil {
		return nil, err
	}
	var names []string
	if only == "proxy" {
		names = nil
	} else if only != "" {
		names = []string{only}
	} else {
		apps, err := listApps()
		if err != nil {
			return nil, err
		}
		names = engine.ListAppsSorted(apps)
	}
	var targets []scanTarget
	for _, name := range names {
		var web []string
		for _, container := range []string{name, name + "-next"} {
			if id := images[container]; imageIDPattern.MatchString(id) {
				web = append(web, id)
				targets = append(targets, scanTarget{Name: name, Image: id})
			}
		}
		for _, container := range []string{jobsName(name), jobsName(name) + "-next"} {
			id := images[container]
			if imageIDPattern.MatchString(id) && !slices.Contains(web, id) {
				targets = append(targets, scanTarget{Name: name + " jobs", Image: id})
			}
		}
	}
	if only == "" || only == "proxy" {
		if id := images[proxyContainer]; imageIDPattern.MatchString(id) {
			targets = append(targets, scanTarget{Name: "proxy", Image: id})
		}
	}
	if only == "proxy" && len(targets) == 0 {
		return nil, errors.New("the proxy is not running, so there is no image to scan")
	}
	if only != "" && only != "proxy" && len(targets) == 0 {
		return nil, fmt.Errorf("%s is not running, so there is no image to scan", only)
	}
	return dedupeTargets(targets), nil
}

func dedupeTargets(targets []scanTarget) []scanTarget {
	seen := map[string]bool{}
	var out []scanTarget
	for _, target := range targets {
		if seen[target.Name+"\x00"+target.Image] {
			continue
		}
		seen[target.Name+"\x00"+target.Image] = true
		out = append(out, target)
	}
	return out
}

// runningImages maps a container name to the id of the image it runs.
func runningImages() (map[string]string, error) {
	var list []struct {
		Names   []string
		State   string
		ImageID string
	}
	if err := dockerAPI("GET", "/containers/json?all=1", nil, &list); err != nil {
		return nil, fmt.Errorf("Docker does not answer: %s", err.Error())
	}
	images := map[string]string{}
	for _, container := range list {
		if container.State != "running" {
			continue
		}
		for _, name := range container.Names {
			images[strings.TrimPrefix(name, "/")] = container.ImageID
		}
	}
	return images, nil
}

// prepareScanner pulls the pinned image and refreshes the vulnerability
// database. The database container has a network. It does not see an image
// and it does not get the Docker socket.
func prepareScanner(dir string) error {
	if out, err := docker("pull", scanImage); err != nil {
		return fmt.Errorf("cannot pull the scanner: %s", lastLine(out))
	}
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	if err := os.Chown(cache, scanUID, scanUID); err != nil {
		return err
	}
	_, err := runTrivy(context.Background(), 10*time.Minute, "chasen-scan-db", "bridge", []string{cache + ":/cache"},
		"image", "--download-db-only", "--cache-dir", "/cache", "--quiet", "--skip-version-check", "--disable-telemetry", "--timeout", "10m")
	if err != nil {
		return fmt.Errorf("cannot refresh the vulnerability database: %s", err.Error())
	}
	return nil
}

// scanOne exports one image and scans the tar with no network and no socket.
func scanOne(dir, image string, ignore []string) ([]scanFinding, int, error) {
	if !imageIDPattern.MatchString(image) {
		return nil, 0, fmt.Errorf("unexpected image id %q", image)
	}
	tar, err := os.CreateTemp(dir, "image-*.tar")
	if err != nil {
		return nil, 0, err
	}
	tar.Close()
	defer os.Remove(tar.Name())
	if out, err := docker("save", "-o", tar.Name(), image); err != nil {
		return nil, 0, fmt.Errorf("cannot export the image: %s", lastLine(out))
	}
	// The scanner runs as a user that is not root, and reads this file.
	if err := os.Chmod(tar.Name(), 0644); err != nil {
		return nil, 0, err
	}

	work := filepath.Join(dir, "work")
	os.RemoveAll(work)
	if err := os.MkdirAll(work, 0700); err != nil {
		return nil, 0, err
	}
	if err := os.Chown(work, scanUID, scanUID); err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(work)

	cache := filepath.Join(dir, "cache")
	out, err := runTrivy(context.Background(), 16*time.Minute, "chasen-scan", "none",
		[]string{cache + ":/cache", tar.Name() + ":/image.tar:ro", work + ":/tmp"},
		"image", "--input", "/image.tar", "--cache-dir", "/cache",
		"--skip-db-update", "--skip-java-db-update", "--offline-scan",
		"--scanners", "vuln", "--severity", "HIGH,CRITICAL",
		"--format", "json", "--quiet", "--skip-version-check", "--disable-telemetry", "--timeout", "15m")
	if err != nil {
		return nil, 0, err
	}
	findings, unfixed, err := parseTrivy([]byte(out), ignore)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read the scanner's report: %s", oneLine(err.Error()))
	}
	return findings, unfixed, nil
}

// runTrivy starts the pinned image and returns its standard output. The
// container gets no Docker socket. network is "none" for a scan and "bridge"
// for the database download. mounts are host:container pairs.
func runTrivy(ctx context.Context, timeout time.Duration, name, network string, mounts []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	docker("rm", "-f", name)
	run := []string{
		"run", "--rm", "--name", name, "--pull", "never",
		"--network", network,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--cpus", "1",
		"--memory", "1g",
		"--memory-swap", "1g",
		"--pids-limit", "256",
		"--user", fmt.Sprintf("%d:%d", scanUID, scanUID),
	}
	for _, mount := range mounts {
		run = append(run, "-v", mount)
	}
	run = append(run, scanImage)
	run = append(run, args...)
	cmd := exec.CommandContext(ctx, "docker", run...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	docker("rm", "-f", name)
	if ctx.Err() != nil {
		return "", errors.New("the scanner did not finish in time")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 137 {
		return "", errors.New("the scanner used its 1 GB memory limit and stopped")
	}
	if stdout.Len() > 8<<20 {
		return "", errors.New("the scanner wrote too much")
	}
	if err != nil {
		msg := lastLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}

// mergeScan keeps the other apps when the command named one of them. A scan
// of shop replaces shop in the stored report and leaves the proxy alone. A
// target that failed keeps the findings it had, so a broken scan does not
// look like the vulnerabilities are gone.
func mergeScan(report scanReport, only string) scanReport {
	if report.Error != "" {
		return report
	}
	prev, ok := loadScan()
	if !ok {
		return report
	}
	previous := map[string]scanTarget{}
	for _, target := range prev.Targets {
		previous[target.Name+"\x00"+target.Image] = target
	}
	for i, target := range report.Targets {
		if target.Error == "" {
			continue
		}
		if old, ok := previous[target.Name+"\x00"+target.Image]; ok {
			old.Error = target.Error
			report.Targets[i] = old
		}
	}
	if only == "" {
		return report
	}
	var kept []scanTarget
	for _, target := range prev.Targets {
		if target.Name == only || strings.HasPrefix(target.Name, only+" ") {
			continue
		}
		kept = append(kept, target)
	}
	report.Targets = append(kept, report.Targets...)
	return report
}

// storeScan writes the report. A run that did not start keeps the findings
// it had and records why. The webhook is called only when the fixable set
// changed, and a failure to call it does not change the exit code.
func storeScan(report scanReport) error {
	prev, _ := loadScan()
	stored := report
	notify := report
	if report.Error != "" {
		prev.Error = report.Error
		stored = prev
		notify = scanReport{}
	}
	if err := saveScan(stored); err != nil {
		return err
	}
	if report.Error == "" && findingSet(prev) != findingSet(notify) {
		cfg, err := loadServerConfig()
		if err == nil && cfg.ScanWebhook != "" {
			body, err := json.Marshal(notify)
			if err == nil {
				postScanWebhook(cfg.ScanWebhook, body)
			}
		}
	}
	return nil
}

func loadScan() (scanReport, bool) {
	db, err := openServerDB()
	if err != nil {
		return scanReport{}, false
	}
	defer db.Close()
	var data string
	if err := db.QueryRow("SELECT report FROM scan_report WHERE id = 1").Scan(&data); err != nil {
		return scanReport{}, false
	}
	var report scanReport
	if json.Unmarshal([]byte(data), &report) != nil {
		return scanReport{}, false
	}
	return report, true
}

func saveScan(report scanReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO scan_report (id, report) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET report = excluded.report`, string(data))
	return err
}

// postScanWebhook sends the report. The URL is a secret: a failure is logged
// without it, and the error from the client is not logged because it
// contains the URL.
func postScanWebhook(url string, body []byte) {
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Print("scan webhook: the call failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		log.Print("scan webhook: the call failed")
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("scan webhook: the call answered %d", resp.StatusCode)
	}
}

// scanAlerts reads the stored report. It does not start a scan.
func scanAlerts(cfg serverConfig, now time.Time) []alert {
	report, ok := loadScan()
	if !ok {
		return nil
	}
	var found []alert
	if report.Error != "" {
		found = append(found, alert{"scan", false, "the image scan failed: " + oneLine(report.Error),
			"chasen scan runs it again and shows why."})
	}
	for _, target := range report.Targets {
		if target.Error != "" {
			again := "chasen scan runs it again."
			if target.Name != "proxy" && !strings.HasSuffix(target.Name, " jobs") {
				again = "chasen scan " + target.Name + " runs it again."
			}
			found = append(found, alert{"scan", false, "the scan of " + target.Name + " failed: " + oneLine(target.Error), again})
		}
		for _, finding := range target.Findings {
			found = append(found, alert{"scan", false, scanAlertWhat(target.Name, finding), scanAlertFix(target.Name, finding)})
		}
		if target.Unfixed > 0 {
			found = append(found, alert{"scan", false, fmt.Sprintf("%s: %d more, no fix yet", target.Name, target.Unfixed),
				"Nothing to deploy until a fix exists."})
		}
	}
	if cfg.Scan && !report.ScannedAt.IsZero() && now.Sub(report.ScannedAt) > scanStale {
		found = append(found, alert{"scan", false, "the image scan is " + age(now.Sub(report.ScannedAt)) + " old",
			"chasen scan runs it now. The nightly scan is on: it runs at 04:15 UTC."})
	}
	return found
}

func scanAlertWhat(name string, finding scanFinding) string {
	return fmt.Sprintf("%s has %s in %s %s, fixed in %s (%s)", name, finding.ID, finding.Package, finding.Installed, finding.Fixed, finding.kindText())
}

func scanAlertFix(name string, finding scanFinding) string {
	if name == "proxy" {
		return "chasen-server proxy update starts the proxy of this release, when that release has the fix."
	}
	if finding.Kind == "os" {
		return "The fix is in the base image. A new image, then chasen deploy, replaces it."
	}
	return "The fix is in the app. It needs a new build, then chasen deploy."
}
