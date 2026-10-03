package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karloscodes/matcha"
)

// host makes a fake host for the alerts: the files of /etc and /run that
// they read, under CHASEN_HOST.
func host(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CHASEN_HOST", dir)
	for path, content := range files {
		os.MkdirAll(filepath.Dir(dir+path), 0755)
		if err := os.WriteFile(dir+path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// A host that a careful owner set up: SSH with keys only, ufw, and updates.
var hardened = map[string]string{
	"/etc/ssh/sshd_config":                      "Include /etc/ssh/sshd_config.d/*.conf\nPermitRootLogin prohibit-password\n",
	"/etc/ssh/sshd_config.d/50-cloud-init.conf": "PasswordAuthentication no\n",
	"/etc/ufw/ufw.conf":                         "ENABLED=yes\nLOGLEVEL=low\n",
	"/etc/apt/apt.conf.d/20auto-upgrades":       "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n",
	"/etc/apt/apt.conf.d/50unattended-upgrades": "// the package is installed\n",
}

func ids(found []alert) string {
	var names []string
	for _, a := range found {
		names = append(names, a.ID)
	}
	return strings.Join(names, ",")
}

func TestAlertsOfTheHost(t *testing.T) {
	t.Run("a hardened host has no alert", func(t *testing.T) {
		host(t, hardened)

		found := append(append(append(sshAlerts(), firewallAlerts()...), updateAlerts()...), rebootAlerts(time.Now())...)

		if len(found) != 0 {
			t.Errorf("alerts = %v", found)
		}
	})

	t.Run("a new host warns about passwords, the firewall, and the updates", func(t *testing.T) {
		host(t, map[string]string{"/etc/ssh/sshd_config": "PermitRootLogin yes\n", "/etc/apt/sources.list": ""})

		found := append(append(sshAlerts(), firewallAlerts()...), updateAlerts()...)

		if ids(found) != "ssh,firewall,updates" || found[0].Error {
			t.Errorf("alerts = %v, want three warnings", found)
		}
	})

	t.Run("the first value of SSH wins, as sshd reads it, and Match blocks are for some users only", func(t *testing.T) {
		host(t, map[string]string{"/etc/ssh/sshd_config": "PasswordAuthentication yes\nPasswordAuthentication no\n"})
		if ids(sshAlerts()) != "ssh" {
			t.Error("the second value won")
		}
		host(t, map[string]string{"/etc/ssh/sshd_config": "Match User deploy\n  PasswordAuthentication no\n"})
		if ids(sshAlerts()) != "ssh" {
			t.Error("a Match block counted for everyone")
		}
	})

	t.Run("an update that waits for a reboot for more than a day", func(t *testing.T) {
		host(t, map[string]string{"/run/reboot-required": "*** System restart required ***\n", "/run/reboot-required.pkgs": "linux-image-6.8.0-45-generic\nlinux-base\nlinux-base\n"})
		old := time.Now().Add(-50 * time.Hour)
		os.Chtimes(os.Getenv("CHASEN_HOST")+"/run/reboot-required", old, old)

		found := rebootAlerts(time.Now())

		if len(found) != 1 || !strings.Contains(found[0].What, "since 2 days") || !strings.Contains(found[0].What, "linux-base, linux-image-6.8.0-45-generic") {
			t.Errorf("alerts = %v", found)
		}
		if found := rebootAlerts(old.Add(time.Hour)); len(found) != 0 {
			t.Errorf("a reboot that waits for one hour is an alert: %v", found)
		}
	})
}

func TestAlertsOfTheBackups(t *testing.T) {
	t.Setenv("CHASEN_ROOT", t.TempDir())
	saveApp("shop", matcha.AppConfig{Image: "chasen.invalid/shop:1", Domain: "shop.localhost"})
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	os.MkdirAll(filepath.Join(backupsDir("shop"), now.Add(-5*time.Hour).Format(stampLayout)), 0755)

	found := backupAlerts(now)

	if len(found) != 1 || !found[0].Error || !strings.Contains(found[0].What, "the newest backup of shop is 5 hours old") {
		t.Errorf("alerts = %v", found)
	}
	if found := backupAlerts(now.Add(-4 * time.Hour)); len(found) != 0 {
		t.Errorf("a backup of one hour ago is an alert: %v", found)
	}
}

func TestPrintAlerts(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 12, 0, 0, time.UTC)

	t.Run("each alert with its fix, then the count and the time", func(t *testing.T) {
		var out strings.Builder
		found := []alert{{"ssh", false, "SSH accepts passwords", "Add PasswordAuthentication no"}, {"disk", true, "the disk is 93% full", "Make it larger"}}

		cfg := serverConfig{}
		printAlerts(&out, found, cfg.QuietAlerts, now)

		want := "WARNING  SSH accepts passwords\n         Add PasswordAuthentication no\nERROR    the disk is 93% full\n         Make it larger\n\n1 error, 1 warning. Checked at 09:12 UTC.\n"
		if out.String() != want {
			t.Errorf("printAlerts =\n%s", out.String())
		}
	})

	t.Run("an alert that the owner made quiet is left out, and named at the end", func(t *testing.T) {
		host(t, map[string]string{"/etc/ssh/sshd_config": "PasswordAuthentication no\n"})
		t.Setenv("CHASEN_ROOT", t.TempDir())
		cfg := serverConfig{QuietAlerts: []string{"firewall"}}

		found := checkAlerts(cfg, now)
		var out strings.Builder
		printAlerts(&out, found, cfg.QuietAlerts, now)

		if strings.Contains(ids(found), "firewall") || !strings.Contains(out.String(), "Quiet: firewall.") {
			t.Errorf("alerts = %v, output:\n%s", ids(found), out.String())
		}
	})
}
