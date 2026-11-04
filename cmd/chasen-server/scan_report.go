package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// A scan report is what `chasen scan` prints and what `chasen alerts` reads.
// It is the last run, kept in the database. Alerts never start a scan.

// scanStale is how old a report may be before alerts say the nightly scan
// has stopped. The scan runs once a night, so a day and a half is a miss.
const scanStale = 36 * time.Hour

// scanFinding is one fixable high or critical vulnerability.
type scanFinding struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed"`
	// Kind is "os" for a package of the image's operating system, and "app"
	// for a language dependency compiled into the app. An OS package is
	// replaced by a new image. An app dependency needs a new build.
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
}

// scanTarget is one image that was running: an app, its jobs container when
// that image is a different one, or the proxy.
type scanTarget struct {
	Name     string        `json:"name"`
	Image    string        `json:"image"`
	Error    string        `json:"error,omitempty"`
	Unfixed  int           `json:"unfixed,omitempty"`
	Findings []scanFinding `json:"findings,omitempty"`
}

// scanReport is the whole run.
type scanReport struct {
	ScannedAt time.Time    `json:"scanned_at"`
	Error     string       `json:"error,omitempty"`
	Targets   []scanTarget `json:"targets,omitempty"`
}

// scanExit is the exit code of `chasen scan`: 0 when nothing fixable is at
// high or critical, 1 when something is, 2 when the scanner failed. A failed
// image wins over findings, so a script can tell an unfinished scan from a
// clean one.
func scanExit(report scanReport) int {
	if report.Error != "" {
		return 2
	}
	findings := 0
	for _, target := range report.Targets {
		if target.Error != "" {
			return 2
		}
		findings += len(target.Findings)
	}
	if findings > 0 {
		return 1
	}
	return 0
}

// findingSet is the fixable findings, in a stable order, so two reports can
// be compared. The count of unfixed vulnerabilities is not part of it: an
// unfixed one has no action, and a webhook fires only when this set changes.
func findingSet(report scanReport) string {
	var keys []string
	for _, target := range report.Targets {
		for _, finding := range target.Findings {
			keys = append(keys, strings.Join([]string{target.Name, finding.ID, finding.Package, finding.Installed, finding.Fixed, finding.Kind, finding.Severity}, "\t"))
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, "\n")
}

// parseTrivy reads one Trivy JSON report and keeps the fixable high and
// critical findings. Unfixed ones are counted. Ids in ignore are dropped.
// Medium and low are dropped: a warning on every scan teaches people not to
// read it.
func parseTrivy(data []byte, ignore []string) (findings []scanFinding, unfixed int, err error) {
	var doc struct {
		Results []struct {
			Class           string `json:"Class"`
			Vulnerabilities []struct {
				VulnerabilityID  string `json:"VulnerabilityID"`
				PkgName          string `json:"PkgName"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
				Severity         string `json:"Severity"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, 0, err
	}
	skip := map[string]bool{}
	for _, id := range ignore {
		skip[id] = true
	}
	seen := map[string]bool{}
	for _, result := range doc.Results {
		kind := "app"
		if result.Class == "os-pkgs" {
			kind = "os"
		}
		for _, vuln := range result.Vulnerabilities {
			if vuln.Severity != "HIGH" && vuln.Severity != "CRITICAL" {
				continue
			}
			if skip[vuln.VulnerabilityID] {
				continue
			}
			key := vuln.VulnerabilityID + "\x00" + vuln.PkgName + "\x00" + vuln.InstalledVersion
			if seen[key] {
				continue
			}
			seen[key] = true
			if strings.TrimSpace(vuln.FixedVersion) == "" {
				unfixed++
				continue
			}
			findings = append(findings, scanFinding{
				ID: vuln.VulnerabilityID, Package: vuln.PkgName,
				Installed: vuln.InstalledVersion, Fixed: strings.TrimSpace(vuln.FixedVersion),
				Kind: kind, Severity: vuln.Severity,
			})
		}
	}
	slices.SortFunc(findings, func(a, b scanFinding) int {
		if c := severityRank(a.Severity) - severityRank(b.Severity); c != 0 {
			return c
		}
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return strings.Compare(a.Package, b.Package)
	})
	return findings, unfixed, nil
}

func severityRank(severity string) int {
	if severity == "CRITICAL" {
		return 0
	}
	return 1
}

func (f scanFinding) kindText() string {
	if f.Kind == "os" {
		return "OS package"
	}
	return "app dependency"
}

// writeScan prints the report for a person.
func writeScan(w io.Writer, report scanReport) {
	if report.Error != "" {
		fmt.Fprintf(w, "The scan did not run: %s\n", oneLine(report.Error))
		return
	}
	fixable, failed := 0, 0
	for _, target := range report.Targets {
		fmt.Fprintf(w, "%s  %s\n", target.Name, target.Image)
		if target.Error != "" {
			failed++
			fmt.Fprintf(w, "  The scan failed: %s\n", oneLine(target.Error))
			continue
		}
		if len(target.Findings) == 0 {
			fmt.Fprintln(w, "  No fixable findings.")
		}
		for _, finding := range target.Findings {
			fixable++
			fmt.Fprintf(w, "  %s  %s  %s  %s -> %s  %s\n", finding.Severity, finding.ID, finding.Package, finding.Installed, finding.Fixed, finding.kindText())
		}
		if target.Unfixed > 0 {
			fmt.Fprintf(w, "  %d more, no fix yet\n", target.Unfixed)
		}
	}
	if len(report.Targets) == 0 {
		fmt.Fprintf(w, "Nothing is running, so there is nothing to scan.\n\nScanned at %s UTC.\n", report.ScannedAt.UTC().Format("15:04"))
		return
	}
	fmt.Fprintf(w, "\n%s. Scanned at %s UTC.\n", scanSummary(fixable, failed), report.ScannedAt.UTC().Format("15:04"))
}

func scanSummary(fixable, failed int) string {
	found := "Nothing fixable"
	if fixable == 1 {
		found = "1 fixable finding"
	} else if fixable > 1 {
		found = fmt.Sprintf("%d fixable findings", fixable)
	}
	if failed == 0 {
		return found
	}
	images := "The scan of 1 image failed"
	if failed > 1 {
		images = fmt.Sprintf("The scan of %d images failed", failed)
	}
	if fixable == 0 {
		return images
	}
	return images + ", and " + found
}

// oneLine makes a scanner error fit on the line of an alert.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:199]) + "…"
	}
	return s
}
