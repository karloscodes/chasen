package main

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The alerts of a server: what is wrong now (an error), and what puts the
// server at risk or will go wrong soon (a warning). `chasen alerts` prints
// them, and the screen asks for them every minute. The server looks on each
// call: nothing runs in the background, and nothing is kept.
//
// The open source version does not harden a server: the operating system is
// the owner's. These alerts say what is missing and how to fix it by hand.

type alert struct {
	ID    string // a short name, for chasen-server settings quiet_alerts
	Error bool
	What  string
	Fix   string
}

// hostPath is a path on the host. The API runs in a container that sees the
// /etc and the /run of the host under CHASEN_HOST, read-only. Run by hand on
// the host, CHASEN_HOST is empty.
func hostPath(path string) string { return os.Getenv("CHASEN_HOST") + path }

// The backups run every hour. Older than this, the hourly run fails.
const backupLate = 2*time.Hour + 10*time.Minute

func serverAlerts() error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	now := time.Now()
	printAlerts(os.Stdout, checkAlerts(cfg, now), cfg.QuietAlerts, now)
	return nil
}

// checkAlerts runs every check, and leaves out the alerts that the owner
// made quiet.
func checkAlerts(cfg serverConfig, now time.Time) []alert {
	var found []alert
	for _, check := range []func() []alert{
		appAlerts,
		func() []alert { return backupAlerts(now) },
		diskAlerts,
		memoryAlerts,
		sshAlerts,
		firewallAlerts,
		updateAlerts,
		func() []alert { return rebootAlerts(now) },
		func() []alert { return selfUpdateAlerts(cfg) },
	} {
		for _, a := range check() {
			if !slices.Contains(cfg.QuietAlerts, a.ID) {
				found = append(found, a)
			}
		}
	}
	// The errors first: they are what is wrong now.
	slices.SortStableFunc(found, func(a, b alert) int {
		switch {
		case a.Error && !b.Error:
			return -1
		case !a.Error && b.Error:
			return 1
		}
		return 0
	})
	return found
}

// printAlerts writes each alert as its level, what is wrong, and an indented
// line with the fix. The screen reads this text.
func printAlerts(w io.Writer, found []alert, quiet []string, now time.Time) {
	errors := 0
	for _, a := range found {
		level := "WARNING"
		if a.Error {
			level, errors = "ERROR", errors+1
		}
		fmt.Fprintf(w, "%-8s %s\n", level, a.What)
		if a.Fix != "" {
			fmt.Fprintf(w, "%-8s %s\n", "", a.Fix)
		}
	}
	checked := "Checked at " + now.UTC().Format("15:04") + " UTC."
	if len(quiet) > 0 {
		checked += " Quiet: " + strings.Join(quiet, ", ") + "."
	}
	var counts []string
	if errors > 0 {
		counts = append(counts, count(errors, "error"))
	}
	if warnings := len(found) - errors; warnings > 0 {
		counts = append(counts, count(warnings, "warning"))
	}
	if len(found) == 0 {
		fmt.Fprintln(w, "No alerts. "+checked)
	} else {
		fmt.Fprintf(w, "\n%s. %s\n", strings.Join(counts, ", "), checked)
	}
}

// appAlerts reports an app whose container does not run.
func appAlerts() []alert {
	apps, err := listApps()
	if err != nil || len(apps) == 0 {
		return nil
	}
	out, err := docker("ps", "-a", "--format", "{{.Names}} {{.State}}")
	if err != nil {
		return []alert{{"docker", true, "Docker does not answer: " + lastLine(out), "Check it on the server: systemctl status docker"}}
	}
	state := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, s, ok := strings.Cut(line, " "); ok {
			state[name] = s
		}
	}
	var found []alert
	for _, name := range slices.Sorted(maps.Keys(apps)) {
		// During a deploy an app has two containers: one that runs is enough.
		now := cmp.Or(state[name], state[name+"-next"])
		if state[name] == "running" || state[name+"-next"] == "running" {
			now = "running"
		}
		switch now {
		case "running":
		case "restarting":
			found = append(found, alert{"app", true, name + " starts again and again: it stops soon after each start",
				fmt.Sprintf("chasen -a %s logs shows why.", name)})
		default:
			found = append(found, alert{"app", true, fmt.Sprintf("%s is down: its container is %s", name, cmp.Or(now, "gone")),
				fmt.Sprintf("chasen -a %s logs shows why, and chasen -a %s restart starts it again.", name, name)})
		}
	}
	return found
}

// backupAlerts reports an app whose newest backup is too old, and a live
// replica that misses changes.
func backupAlerts(now time.Time) []alert {
	apps, err := listApps()
	if err != nil {
		return nil
	}
	var found []alert
	for _, name := range slices.Sorted(maps.Keys(apps)) {
		stamps := localBackups(name)
		if !backedUp(name) || len(stamps) == 0 {
			continue // no backups by choice, or no database yet
		}
		newest, err := time.Parse(stampLayout, stamps[0])
		if err == nil && now.Sub(newest) > backupLate {
			found = append(found, alert{"backups", true, fmt.Sprintf("the newest backup of %s is %s old: the hourly backup fails", name, age(now.Sub(newest))),
				fmt.Sprintf("chasen -a %s backup runs one now and shows why it fails.", name)})
		}
	}
	behind, _ := replicasBehind("", now)
	for _, b := range behind {
		found = append(found, alert{"replica", true, b, "chasen bucket shows the bucket. The replica starts again with the next restart of the API."})
	}
	return found
}

// diskAlerts reports a disk that is almost full: the disk of the system and
// the disk of the apps, when they are two.
func diskAlerts() []alert {
	var found []alert
	seen := map[uint64]bool{}
	for _, path := range []string{hostPath("/etc"), root() + "/var/matcha"} {
		var fs syscall.Statfs_t
		if syscall.Statfs(path, &fs) != nil || fs.Blocks == 0 {
			continue
		}
		id := uint64(fs.Fsid.X__val[0])<<32 | uint64(uint32(fs.Fsid.X__val[1]))
		if seen[id] {
			continue
		}
		seen[id] = true
		used := 100 - int(fs.Bavail*100/fs.Blocks)
		free := float64(fs.Bavail) * float64(fs.Bsize) / 1e9
		if used < 80 {
			continue
		}
		what := fmt.Sprintf("the disk is %d%% full: %.1f GB free", used, free)
		found = append(found, alert{"disk", used >= 90, what,
			"docker image prune -a removes the images that no container uses. Or make the disk larger at your provider."})
	}
	return found
}

// memoryAlerts reports a server with little memory left for a new version of
// an app: a deploy runs the old and the new version at the same time.
func memoryAlerts() []alert {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	info := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, ":"); ok {
			info[name], _ = strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
		}
	}
	total, available := info["MemTotal"], info["MemAvailable"]
	if total == 0 || available*10 >= total {
		return nil
	}
	return []alert{{"memory", false, fmt.Sprintf("the memory is almost all in use: %d MB of %d MB free", available/1024, total/1024),
		"A deploy runs two versions of an app for a moment. Give an app less with memory: in chasen.yml, or move to a larger server."}}
}

// sshAlerts reports an SSH server that accepts passwords. A password can be
// guessed; a key cannot.
func sshAlerts() []alert {
	value, found := sshdSetting(hostPath("/etc/ssh/sshd_config"), "passwordauthentication")
	if found && value == "no" {
		return nil
	}
	if _, err := os.Stat(hostPath("/etc/ssh/sshd_config")); err != nil {
		return nil // no SSH server
	}
	return []alert{{"ssh", false, "SSH accepts passwords: a bot can guess one",
		"Log in with a key, then add PasswordAuthentication no to /etc/ssh/sshd_config and run: systemctl reload ssh"}}
}

// sshdSetting returns the value of a setting of sshd, as sshd reads it: the
// first value wins, and Include brings in other files at that place. The
// settings in Match blocks are for some users only, so the search stops there.
func sshdSetting(path, name string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		fields := strings.Fields(strings.TrimSpace(lines.Text()))
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch key := strings.ToLower(fields[0]); key {
		case "match":
			return "", false
		case "include":
			for _, pattern := range fields[1:] {
				if !filepath.IsAbs(pattern) {
					pattern = "/etc/ssh/" + pattern
				}
				matches, _ := filepath.Glob(hostPath(pattern))
				slices.Sort(matches)
				for _, included := range matches {
					if value, ok := sshdSetting(included, name); ok {
						return value, true
					}
				}
			}
		case name:
			return strings.ToLower(fields[1]), true
		}
	}
	return "", false
}

// firewallAlerts reports a server with no firewall of its own: ufw, or
// nftables with rules that drop what they do not allow.
func firewallAlerts() []alert {
	if ufw, err := os.ReadFile(hostPath("/etc/ufw/ufw.conf")); err == nil && strings.Contains(string(ufw), "ENABLED=yes") {
		return nil
	}
	nft, err := os.ReadFile(hostPath("/etc/nftables.conf"))
	_, enabled := os.Stat(hostPath("/etc/systemd/system/multi-user.target.wants/nftables.service"))
	if err == nil && enabled == nil && strings.Contains(string(nft), "policy drop") {
		return nil
	}
	return []alert{{"firewall", false, "no firewall runs on this server: every port that a program opens is open to the internet",
		"Let in SSH, 80, and 443: ufw allow OpenSSH && ufw allow 80,443/tcp && ufw enable. When the firewall of your provider does it, run: chasen-server settings quiet_alerts firewall"}}
}

// updateAlerts reports a Debian or Ubuntu server that does not install its
// security updates by itself.
func updateAlerts() []alert {
	if _, err := os.Stat(hostPath("/etc/apt")); err != nil {
		return nil // not Debian or Ubuntu: the owner has another way
	}
	periodic, _ := os.ReadFile(hostPath("/etc/apt/apt.conf.d/20auto-upgrades"))
	_, installed := os.Stat(hostPath("/etc/apt/apt.conf.d/50unattended-upgrades"))
	if installed == nil && strings.Contains(strings.ReplaceAll(string(periodic), " ", ""), `Unattended-Upgrade"1"`) {
		return nil
	}
	return []alert{{"updates", false, "the server does not install its security updates by itself",
		"apt-get install -y unattended-upgrades && dpkg-reconfigure -f noninteractive unattended-upgrades. The docs show how to let it reboot at night: https://chasenhq.com/docs/getting-started/#updates"}}
}

// rebootAlerts reports an update that waits for a reboot for more than a day:
// until then the server runs the old kernel, with its known holes.
func rebootAlerts(now time.Time) []alert {
	info, err := os.Stat(hostPath("/run/reboot-required"))
	if err != nil || now.Sub(info.ModTime()) < 24*time.Hour {
		return nil
	}
	what := "a reboot waits since " + age(now.Sub(info.ModTime())) + ": the server runs an old kernel"
	if pkgs, err := os.ReadFile(hostPath("/run/reboot-required.pkgs")); err == nil && len(strings.Fields(string(pkgs))) > 0 {
		what += ", for " + strings.Join(slices.Compact(slices.Sorted(slices.Values(strings.Fields(string(pkgs))))), ", ")
	}
	return []alert{{"reboot", false, what,
		"Run reboot on the server: about one minute of downtime. The docs show how to let it reboot at night: https://chasenhq.com/docs/getting-started/#updates"}}
}

// selfUpdateAlerts reports a server that does not update chasen-server.
func selfUpdateAlerts(cfg serverConfig) []alert {
	if cfg.AutoUpdate == nil || *cfg.AutoUpdate {
		return nil
	}
	return []alert{{"auto_update", false, "chasen-server does not update itself: fixes reach this server only by hand",
		"chasen-server settings auto_update on, or chasen-server update from time to time."}}
}
