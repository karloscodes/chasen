package main

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// The files of the live replica, by level. Litestream writes each change as a
// small file (level 0), and merges the files of a level into one file of the
// next level: every 30 seconds, every 5 minutes, every hour. Once a day it
// writes a full copy of the database (level 9). After each full copy it
// deletes the full copies that are older than one day, and then every file
// from before the oldest full copy that it keeps. So the replica holds about
// two days of changes, and it does not grow past that.
var replicaLevels = []struct{ level, what, kept string }{
	{"0000", "changes, as they happen", "kept 5 minutes"},
	{"0001", "merged each 30 seconds", ""},
	{"0002", "merged each 5 minutes", ""},
	{"0003", "merged each hour", ""},
	{"0009", "full copies, one each day", "kept 1 day"},
}

// bucketUsage says what the bucket holds: for each app, its snapshots and
// its live replica, and what the replica is made of. An object has the key
// <app>/snapshots/<stamp>/<file> or <app>/live/<database>/<level>/<file>.
func bucketUsage(objects []s3Object, now time.Time) string {
	type app struct {
		snapshots, live int64
		stamps          map[string]bool
	}
	apps := map[string]*app{}
	levelFiles, levelSize := map[string]int{}, map[string]int64{}
	var total, other int64
	var oldest time.Time
	for _, object := range objects {
		total += object.Size
		parts := strings.Split(object.Key, "/")
		if len(parts) < 4 || (parts[1] != "snapshots" && parts[1] != "live") {
			other += object.Size
			continue
		}
		a := apps[parts[0]]
		if a == nil {
			a = &app{stamps: map[string]bool{}}
			apps[parts[0]] = a
		}
		if parts[1] == "snapshots" {
			a.snapshots += object.Size
			a.stamps[parts[2]] = true
			continue
		}
		a.live += object.Size
		level := parts[len(parts)-2]
		levelFiles[level]++
		levelSize[level] += object.Size
		if oldest.IsZero() || object.LastModified.Before(oldest) {
			oldest = object.LastModified
		}
	}
	if len(objects) == 0 {
		return "The bucket is empty.\n"
	}

	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tSNAPSHOTS\tLIVE REPLICA\tTOTAL")
	names := make([]string, 0, len(apps))
	for name := range apps {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		a := apps[name]
		snapshots := "none"
		if len(a.stamps) > 0 {
			snapshots = fmt.Sprintf("%s in %s", megabytes(a.snapshots), count(len(a.stamps), "backup"))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, snapshots, megabytes(a.live), megabytes(a.snapshots+a.live))
	}
	w.Flush()
	fmt.Fprintf(&out, "Total: %s in %s", megabytes(total), count(len(objects), "object"))
	if other > 0 {
		fmt.Fprintf(&out, ". %s of it is not from Chasen", megabytes(other))
	}
	out.WriteString("\n")

	if oldest.IsZero() {
		return trimLines(out.String())
	}
	out.WriteString("\nThe live replica is files of changes. What it holds now:\n")
	w = tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	for _, l := range replicaLevels {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", l.what, count(levelFiles[l.level], "file"), megabytes(levelSize[l.level]), l.kept)
	}
	w.Flush()
	fmt.Fprintf(&out, "Its oldest file is %s old. After each daily full copy, the files from before the oldest full copy are deleted: the replica holds about two days of changes, not more.\n", age(now.Sub(oldest)))
	return trimLines(out.String())
}

// trimLines takes the spaces off the end of each line: a table pads its last column.
func trimLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// megabytes says a size for a person: "250 KB", "134.8 MB", "2.3 GB".
func megabytes(bytes int64) string {
	switch mb := float64(bytes) / (1 << 20); {
	case bytes == 0:
		return "0"
	case mb < 1:
		return fmt.Sprintf("%.0f KB", float64(bytes)/(1<<10))
	case mb < 1024:
		return fmt.Sprintf("%.1f MB", mb)
	default:
		return fmt.Sprintf("%.1f GB", mb/1024)
	}
}

// count says a number of things: "1 file", "20 files".
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// age says a duration for a person: "18 minutes", "5 hours", "2 days".
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return count(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return count(int(d.Hours()), "hour")
	}
	return count(int(d.Hours()/24), "day")
}
