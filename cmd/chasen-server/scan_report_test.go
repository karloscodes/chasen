package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseTrivy(t *testing.T) {
	raw := []byte(`{
		"Results": [
			{
				"Class": "os-pkgs",
				"Vulnerabilities": [
					{"VulnerabilityID": "CVE-2024-5535", "PkgName": "libssl3", "InstalledVersion": "3.3.2-r0", "FixedVersion": "3.3.3-r0", "Severity": "HIGH"},
					{"VulnerabilityID": "CVE-2023-0001", "PkgName": "musl", "InstalledVersion": "1.2.5", "FixedVersion": "", "Severity": "HIGH"},
					{"VulnerabilityID": "CVE-2020-0001", "PkgName": "busybox", "InstalledVersion": "1", "FixedVersion": "2", "Severity": "MEDIUM"},
					{"VulnerabilityID": "CVE-2024-5535", "PkgName": "libssl3", "InstalledVersion": "3.3.2-r0", "FixedVersion": "3.3.3-r0", "Severity": "HIGH"}
				]
			},
			{
				"Class": "lang-pkgs",
				"Vulnerabilities": [
					{"VulnerabilityID": "GO-2026-6860", "PkgName": "golang.org/x/net", "InstalledVersion": "v0.61.0", "FixedVersion": "v0.62.0", "Severity": "CRITICAL"},
					{"VulnerabilityID": "GO-2024-0001", "PkgName": "golang.org/x/crypto", "InstalledVersion": "v0.1.0", "FixedVersion": "v0.2.0", "Severity": "HIGH"}
				]
			}
		]
	}`)

	findings, unfixed, err := parseTrivy(raw, []string{"GO-2024-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if unfixed != 1 {
		t.Errorf("unfixed = %d, want 1", unfixed)
	}
	if len(findings) != 2 || findings[0].ID != "GO-2026-6860" || findings[0].Kind != "app" || findings[1].ID != "CVE-2024-5535" || findings[1].Kind != "os" {
		t.Errorf("findings = %+v, want the critical app dependency first, then the OS package, with the ignored id and the medium one gone", findings)
	}
}

func TestScanExit(t *testing.T) {
	clean := scanReport{Targets: []scanTarget{{Name: "shop", Unfixed: 3}}}
	if scanExit(clean) != 0 {
		t.Errorf("unfixed only: exit %d, want 0", scanExit(clean))
	}
	found := scanReport{Targets: []scanTarget{{Findings: []scanFinding{{ID: "CVE-1"}}}}}
	if scanExit(found) != 1 {
		t.Errorf("a fixable finding: exit %d, want 1", scanExit(found))
	}
	broken := scanReport{Error: "docker"}
	if scanExit(broken) != 2 {
		t.Errorf("a failed run: exit %d, want 2", scanExit(broken))
	}
	partial := scanReport{Targets: []scanTarget{{Error: "trivy"}, {Findings: []scanFinding{{ID: "CVE-1"}}}}}
	if scanExit(partial) != 2 {
		t.Errorf("one image failed: exit %d, want 2", scanExit(partial))
	}
}

func TestFindingSet(t *testing.T) {
	shop := scanFinding{ID: "GO-2026-6860", Package: "golang.org/x/net", Installed: "v0.61.0", Fixed: "v0.62.0", Kind: "app", Severity: "CRITICAL"}
	a := scanReport{Targets: []scanTarget{{Name: "shop", Unfixed: 1, Findings: []scanFinding{shop}}, {Name: "proxy"}}}
	b := scanReport{Targets: []scanTarget{{Name: "proxy"}, {Name: "shop", Unfixed: 9, Findings: []scanFinding{shop}}}}
	if findingSet(a) != findingSet(b) {
		t.Errorf("order or the unfixed count changed the set:\n%s\n%s", findingSet(a), findingSet(b))
	}
	c := scanReport{Targets: []scanTarget{{Name: "shop", Findings: []scanFinding{shop, {ID: "CVE-1", Package: "libssl3"}}}}}
	if findingSet(a) == findingSet(c) {
		t.Error("a new finding did not change the set")
	}
	d := scanReport{Targets: []scanTarget{{Name: "shop"}}}
	if findingSet(a) == findingSet(d) {
		t.Error("a finding that was fixed did not change the set")
	}
}

func TestWriteScan(t *testing.T) {
	report := scanReport{
		ScannedAt: time.Date(2026, 10, 31, 4, 15, 0, 0, time.UTC),
		Targets: []scanTarget{{
			Name: "shop", Image: "sha256:abc", Unfixed: 4,
			Findings: []scanFinding{
				{ID: "GO-2026-6860", Package: "golang.org/x/net", Installed: "v0.61.0", Fixed: "v0.62.0", Kind: "app", Severity: "CRITICAL"},
				{ID: "CVE-2024-5535", Package: "libssl3", Installed: "3.3.2-r0", Fixed: "3.3.3-r0", Kind: "os", Severity: "HIGH"},
			},
		}},
	}
	var buf bytes.Buffer
	writeScan(&buf, report)
	text := buf.String()
	for _, want := range []string{
		"shop  sha256:abc",
		"CRITICAL  GO-2026-6860  golang.org/x/net  v0.61.0 -> v0.62.0  app dependency",
		"HIGH  CVE-2024-5535  libssl3  3.3.2-r0 -> 3.3.3-r0  OS package",
		"4 more, no fix yet",
		"2 fixable findings. Scanned at 04:15 UTC.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestScanAlertsReadTheStoredReport(t *testing.T) {
	t.Setenv("CHASEN_ROOT", t.TempDir())
	report := scanReport{
		ScannedAt: time.Now().Add(-40 * time.Hour),
		Targets: []scanTarget{{
			Name: "shop", Unfixed: 4,
			Findings: []scanFinding{
				{ID: "GO-2026-6860", Package: "golang.org/x/net", Installed: "v0.61.0", Fixed: "v0.62.0", Kind: "app", Severity: "CRITICAL"},
				{ID: "CVE-2024-5535", Package: "libssl3", Installed: "3.3.2-r0", Fixed: "3.3.3-r0", Kind: "os", Severity: "HIGH"},
			},
		}},
	}
	if err := saveScan(report); err != nil {
		t.Fatal(err)
	}

	found := scanAlerts(serverConfig{Scan: true}, time.Now())
	var what []string
	for _, alert := range found {
		if alert.ID != "scan" || alert.Error {
			t.Errorf("alert = %+v, want a scan warning", alert)
		}
		what = append(what, alert.What+" | "+alert.Fix)
	}
	text := strings.Join(what, "\n")
	for _, want := range []string{
		"shop has GO-2026-6860 in golang.org/x/net v0.61.0, fixed in v0.62.0 (app dependency)",
		"The fix is in the app. It needs a new build, then chasen deploy.",
		"shop has CVE-2024-5535 in libssl3 3.3.2-r0, fixed in 3.3.3-r0 (OS package)",
		"The fix is in the base image. A new image, then chasen deploy, replaces it.",
		"shop: 4 more, no fix yet",
		"the image scan is",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	off := scanAlerts(serverConfig{}, time.Now())
	for _, alert := range off {
		if strings.Contains(alert.What, "old") {
			t.Errorf("a scan that is off warned that the report is old: %s", alert.What)
		}
	}
}

func TestScanSettingsHideTheWebhook(t *testing.T) {
	t.Setenv("CHASEN_ROOT", t.TempDir())
	cfg := serverConfig{
		Domain: "example.com", Token: "secret", Scan: true,
		ScanIgnore:  []string{"CVE-2024-5535", "GO-2026-6860"},
		ScanWebhook: "https://alerts.example/hook?token=super-secret",
	}
	if err := saveServerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Scan || loaded.ScanWebhook != cfg.ScanWebhook || strings.Join(loaded.ScanIgnore, ",") != "CVE-2024-5535,GO-2026-6860" {
		t.Fatalf("loaded = %+v", loaded)
	}
	var buf bytes.Buffer
	writeSettings(&buf, loaded)
	if strings.Contains(buf.String(), "super-secret") || strings.Contains(buf.String(), "alerts.example") {
		t.Fatalf("the webhook URL was printed:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "scan           on\n") || !strings.Contains(buf.String(), "scan_webhook   set\n") {
		t.Fatalf("settings:\n%s", buf.String())
	}
}

func TestScanWebhookDoesNotLogTheURL(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	postScanWebhook("http://127.0.0.1:1/hook?token=super-secret", []byte(`{"targets":[]}`))

	if strings.Contains(buf.String(), "super-secret") || strings.Contains(buf.String(), "127.0.0.1") {
		t.Fatalf("the webhook URL was logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "scan webhook: the call failed") {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestMergeScanKeepsFindingsWhenOneImageFails(t *testing.T) {
	t.Setenv("CHASEN_ROOT", t.TempDir())
	old := scanFinding{ID: "GO-2026-6860", Package: "golang.org/x/net", Installed: "v0.61.0", Fixed: "v0.62.0", Kind: "app", Severity: "CRITICAL"}
	if err := saveScan(scanReport{Targets: []scanTarget{
		{Name: "shop", Image: "sha256:aaa", Findings: []scanFinding{old}},
		{Name: "proxy", Image: "sha256:bbb"},
	}}); err != nil {
		t.Fatal(err)
	}

	failed := scanReport{Targets: []scanTarget{{Name: "shop", Image: "sha256:aaa", Error: "the scanner wrote nothing"}}}
	merged := mergeScan(failed, "")
	if findingSet(merged) != findingSet(scanReport{Targets: []scanTarget{{Name: "shop", Image: "sha256:aaa", Findings: []scanFinding{old}}}}) {
		t.Fatalf("a failed image dropped its findings: %+v", merged.Targets)
	}
	if merged.Targets[0].Error == "" {
		t.Fatal("the failure was dropped with the findings")
	}

	shopOnly := mergeScan(scanReport{Targets: []scanTarget{{Name: "shop", Image: "sha256:aaa", Findings: nil}}}, "shop")
	var names []string
	for _, target := range shopOnly.Targets {
		names = append(names, target.Name)
	}
	if strings.Join(names, ",") != "proxy,shop" {
		t.Fatalf("targets = %v, want the proxy kept and shop replaced", names)
	}
}

func TestScanDue(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !scanDue(at("2026-10-31 04:15"), at("2026-10-31 04:15")) {
		t.Error("04:15 UTC is not due")
	}
	if !scanDue(at("2026-10-31 04:10"), at("2026-10-31 04:20")) {
		t.Error("a window that holds 04:15 is not due")
	}
	if scanDue(at("2026-10-31 04:16"), at("2026-10-31 05:30")) || scanDue(at("2026-10-31 04:15"), time.Time{}) {
		t.Error("a minute other than 04:15 is due")
	}
}

func TestParseScanIgnore(t *testing.T) {
	ids, err := parseScanIgnore(" CVE-2024-5535, GO-2026-6860, CVE-2024-5535 ")
	if err != nil || strings.Join(ids, ",") != "CVE-2024-5535,GO-2026-6860" {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	if _, err := parseScanIgnore("https://alerts.example/hook"); err == nil {
		t.Fatal("a URL was accepted as a vulnerability id")
	}
	ids, err = parseScanIgnore("")
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty = %v, %v", ids, err)
	}
}
