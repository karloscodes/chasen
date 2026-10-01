package main

import (
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/karloscodes/matcha"
	_ "modernc.org/sqlite"
)

// A backup is one directory named by its UTC time. It holds one gzip file for
// each SQLite database in the volumes of the app:
//
//	/var/matcha/<app>/backups/20261001T120000Z/data/db.sqlite3.gz
//
// The offsite copy has the same layout: <app>/snapshots/20261001T120000Z/data/db.sqlite3.gz
// The live replica of Litestream is next to it, in <app>/live/.
const stampLayout = "20060102T150405Z"

func backupsDir(name string) string { return appDir(name) + "/backups" }

// localBackups returns the stamps of the complete backups, newest first.
func localBackups(name string) []string {
	entries, _ := os.ReadDir(backupsDir(name))
	var stamps []string
	for _, e := range entries {
		if _, err := time.Parse(stampLayout, e.Name()); err == nil && e.IsDir() {
			stamps = append(stamps, e.Name())
		}
	}
	slices.Sort(stamps)
	slices.Reverse(stamps)
	return stamps
}

// findDatabases returns the SQLite files in the volumes of an app, relative to dir.
// It reads the file header, so the file name does not matter.
func findDatabases(dir string) ([]string, error) {
	var dbs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		// Skip what is not live data: the backups, the databases a restore moved
		// aside, and the local state of the live replica.
		if err == nil && d.IsDir() && (d.Name() == "backups" || strings.HasPrefix(d.Name(), "pre-restore-") || strings.HasSuffix(d.Name(), "-litestream")) {
			return fs.SkipDir
		}
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		header := make([]byte, 16)
		if _, err := io.ReadFull(f, header); err == nil && string(header) == "SQLite format 3\x00" {
			rel, _ := filepath.Rel(dir, path)
			dbs = append(dbs, rel)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return dbs, err
}

// sqliteDB opens a database with the SQLite that is inside this binary.
func sqliteDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // the timeout below is a setting of the connection
	if _, err := db.Exec("PRAGMA busy_timeout = 30000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

func sqliteExec(path, statement string, args ...any) error {
	db, err := sqliteDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(statement, args...); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func checkIntegrity(path string) error {
	db, err := sqliteDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if result != "ok" {
		return fmt.Errorf("%s fails the integrity check: %s", path, result)
	}
	return nil
}

// snapshot writes a consistent, checked, compressed copy of a live database.
// VACUUM INTO reads one snapshot of the database, so the app can keep writing.
func snapshot(db, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	raw := strings.TrimSuffix(dst, ".gz")
	defer os.Remove(raw)
	if err := sqliteExec(db, "VACUUM INTO ?", raw); err != nil {
		return err
	}
	if err := checkIntegrity(raw); err != nil {
		return err
	}
	return gzipFile(raw, dst)
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Sync()
}

func gunzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, zr); err != nil {
		return err
	}
	return out.Sync()
}

// backupFiles returns the files of one local backup, relative to its directory.
func backupFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, rel)
		}
		return err
	})
	return files, err
}

// backupApp backs up every SQLite database of the app. It returns the stamp
// of the backup, or "" when the app has no database yet.
func backupApp(name string, cfg serverConfig) (string, error) {
	dbs, err := findDatabases(appDir(name))
	if err != nil || len(dbs) == 0 {
		return "", err
	}

	stamp := time.Now().UTC().Format(stampLayout)
	dir := filepath.Join(backupsDir(name), stamp)
	// Build the backup in a .tmp directory. A backup that stops halfway never shows as complete.
	tmp := dir + ".tmp"
	defer os.RemoveAll(tmp)
	for _, db := range dbs {
		if err := snapshot(filepath.Join(appDir(name), db), filepath.Join(tmp, db+".gz")); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}

	if s3 := cfg.Backup.S3; s3 != nil {
		for _, db := range dbs {
			if err := s3.putFile(name+"/snapshots/"+stamp+"/"+db+".gz", filepath.Join(dir, db+".gz")); err != nil {
				return stamp, fmt.Errorf("the backup %s is on the server, but the offsite copy failed: %w", stamp, err)
			}
		}
	}
	return stamp, prune(name, cfg)
}

// printBackup runs one backup and tells the user about it.
func printBackup(name string, cfg serverConfig) error {
	stamp, err := backupApp(name, cfg)
	if err != nil {
		return err
	}
	if stamp == "" {
		fmt.Printf("%s: no SQLite database in %s yet\n", name, appDir(name))
		return nil
	}
	where := "on the server only"
	if cfg.Backup.S3 != nil {
		where = "on the server and offsite"
	}
	fmt.Printf("%s: backup %s (%s)\n", name, stamp, where)
	return nil
}

// backupAll backs up every app. `serve` runs it each hour. It calls the heartbeat
// URL only when every backup worked, so a monitor can alert when backups stop.
func backupAll() error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	apps, err := matcha.ListAppsFrom(appsPath())
	if err != nil {
		return err
	}

	var failed []string
	for _, name := range matcha.ListAppsSorted(apps) {
		if err := printBackup(name, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "%s: backup failed: %v\n", name, err)
			failed = append(failed, name)
		}
	}
	// A stopped replica must not stay silent: this makes the run fail, and the heartbeat stops.
	if cfg.Backup.S3 != nil && replicaPID() == 0 {
		fmt.Fprintln(os.Stderr, "the live replica does not run. Check: docker logs chasen-server")
		failed = append(failed, "live replica")
	}
	if len(failed) > 0 {
		return fmt.Errorf("backup failed for: %s", strings.Join(failed, ", "))
	}
	if cfg.Backup.HeartbeatURL != "" {
		resp, err := http.Get(cfg.Backup.HeartbeatURL)
		if err != nil {
			return fmt.Errorf("heartbeat: %w", err)
		}
		resp.Body.Close()
	}
	return nil
}

// expired returns the stamps that the retention rules do not keep.
// The rules keep the newest backup of each of the last 24 hours, 7 days,
// 4 weeks, and 6 months that have a backup.
func expired(stamps []string) []string {
	rules := []struct {
		bucket func(time.Time) string
		count  int
	}{
		{func(t time.Time) string { return t.Format("2006010215") }, 24},
		{func(t time.Time) string { return t.Format("20060102") }, 7},
		{func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprint(y, w) }, 4},
		{func(t time.Time) string { return t.Format("200601") }, 6},
	}

	sorted := slices.Clone(stamps)
	slices.Sort(sorted)
	slices.Reverse(sorted)

	keep := map[string]bool{}
	for _, rule := range rules {
		seen := map[string]bool{}
		for _, stamp := range sorted {
			t, err := time.Parse(stampLayout, stamp)
			if err != nil {
				keep[stamp] = true // not ours: never delete it
				continue
			}
			if b := rule.bucket(t); !seen[b] && len(seen) < rule.count {
				seen[b] = true
				keep[stamp] = true
			}
		}
	}

	var drop []string
	for _, stamp := range sorted {
		if !keep[stamp] {
			drop = append(drop, stamp)
		}
	}
	return drop
}

// prune applies the retention rules on the server and offsite. Each side uses
// its own list, so an empty server never empties the offsite copy.
func prune(name string, cfg serverConfig) error {
	for _, stamp := range expired(localBackups(name)) {
		if err := os.RemoveAll(filepath.Join(backupsDir(name), stamp)); err != nil {
			return err
		}
	}

	s3 := cfg.Backup.S3
	if s3 == nil {
		return nil
	}
	remote, err := offsiteBackups(name, s3)
	if err != nil {
		return err
	}
	for _, stamp := range expired(slices.Collect(maps.Keys(remote))) {
		for _, key := range remote[stamp] {
			if err := s3.delete(key); err != nil {
				return err
			}
		}
	}
	return nil
}

// offsiteBackups returns the object keys of each offsite backup, by stamp.
func offsiteBackups(name string, s3 *s3Config) (map[string][]string, error) {
	keys, err := s3.list(name + "/snapshots/")
	if err != nil {
		return nil, err
	}
	backups := map[string][]string{}
	for _, key := range keys {
		stamp, _, _ := strings.Cut(strings.TrimPrefix(key, name+"/snapshots/"), "/")
		backups[stamp] = append(backups[stamp], key)
	}
	return backups, nil
}

func serverBackups(name string) error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	local := localBackups(name)
	offsite := map[string][]string{}
	if cfg.Backup.S3 != nil {
		if offsite, err = offsiteBackups(name, cfg.Backup.S3); err != nil {
			return err
		}
	}

	all := slices.Clone(local)
	for stamp := range offsite {
		if !slices.Contains(all, stamp) {
			all = append(all, stamp)
		}
	}
	slices.Sort(all)
	slices.Reverse(all)
	for _, stamp := range all {
		var where []string
		if slices.Contains(local, stamp) {
			where = append(where, "server")
		}
		if _, ok := offsite[stamp]; ok {
			where = append(where, "offsite")
		}
		fmt.Printf("%s  %s\n", stamp, strings.Join(where, " + "))
	}
	if len(all) == 0 {
		fmt.Println("No backups.")
	}
	if cfg.Backup.S3 != nil {
		if dbs, err := liveDatabases(name, cfg.Backup.S3); err == nil && len(dbs) > 0 {
			fmt.Println("live              offsite, continuous")
		}
	}
	return nil
}

var errNoBackups = errors.New("no backups")

// serverRestore replaces the databases of the app with a backup. The source is
// a backup stamp, "live" for the live replica, or "" for the newest backup.
func serverRestore(name, source string) error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	app, err := loadApp(name)
	if err != nil {
		return err
	}

	// Unpack and check the backup while the app still runs. The downtime is only the swap.
	var staged map[string]string
	if source == "live" {
		if cfg.Backup.S3 == nil {
			return errors.New("no live replica: backup.s3 is not set on the server")
		}
		if staged, err = stageLive(name, cfg.Backup.S3); err == nil && len(staged) == 0 {
			err = errors.New("the live replica has no database for this app")
		}
	} else {
		if source, err = fetchBackup(name, source, cfg); err == nil {
			staged, err = stageBackup(name, source)
		}
	}
	if err != nil {
		return err
	}

	m := engine(name, app)
	if err := pauseReplica(); err != nil {
		return err
	}
	m.StopApp()
	aside, swapErr := swap(name, staged)
	if err := m.Deploy(); err != nil {
		return fmt.Errorf("the app did not start after the restore: %w", err)
	}
	if swapErr != nil {
		return swapErr
	}
	fmt.Printf("Restored %s from %s. The previous databases are in %s\n", name, source, aside)
	return nil
}

// fetchBackup makes sure the backup is on the server and returns its stamp.
// An empty stamp means the newest backup, on the server or offsite.
func fetchBackup(name, stamp string, cfg serverConfig) (string, error) {
	local := localBackups(name)
	offsite := map[string][]string{}
	if cfg.Backup.S3 != nil {
		var err error
		if offsite, err = offsiteBackups(name, cfg.Backup.S3); err != nil {
			return "", err
		}
	}

	if stamp == "" {
		all := append(slices.Collect(maps.Keys(offsite)), local...)
		if len(all) == 0 {
			return "", errNoBackups
		}
		stamp = slices.Max(all)
	}
	if _, err := time.Parse(stampLayout, stamp); err != nil {
		return "", fmt.Errorf("invalid backup %q. Run `chasen backups` to see the list", stamp)
	}
	if slices.Contains(local, stamp) {
		return stamp, nil
	}
	keys, ok := offsite[stamp]
	if !ok {
		return "", fmt.Errorf("no backup %s. Run `chasen backups` to see the list", stamp)
	}

	dir := filepath.Join(backupsDir(name), stamp)
	tmp := dir + ".tmp"
	defer os.RemoveAll(tmp)
	for _, key := range keys {
		rel := strings.TrimPrefix(key, name+"/snapshots/"+stamp+"/")
		if !filepath.IsLocal(rel) {
			return "", fmt.Errorf("unsafe object key %q", key)
		}
		if err := cfg.Backup.S3.getFile(key, filepath.Join(tmp, rel)); err != nil {
			return "", err
		}
	}
	return stamp, os.Rename(tmp, dir)
}

// stageBackup unpacks and checks every database of a local backup. A staged
// restore maps each database path to a checked copy next to it, ready for swap.
func stageBackup(name, stamp string) (staged map[string]string, err error) {
	dir := filepath.Join(backupsDir(name), stamp)
	files, err := backupFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("backup %s is empty", stamp)
	}

	staged = map[string]string{}
	defer func() {
		if err != nil {
			discard(staged)
		}
	}()
	for _, file := range files {
		db := filepath.Join(appDir(name), strings.TrimSuffix(file, ".gz"))
		if err := os.MkdirAll(filepath.Dir(db), 0755); err != nil {
			return nil, err
		}
		staged[db] = db + ".restore"
		if err := gunzipFile(filepath.Join(dir, file), staged[db]); err != nil {
			return nil, err
		}
		if err := checkIntegrity(staged[db]); err != nil {
			return nil, err
		}
	}
	return staged, nil
}

func discard(staged map[string]string) {
	for _, tmp := range staged {
		os.Remove(tmp)
	}
}

// swap puts the staged databases in place. The app must be stopped. It moves
// the current databases to a pre-restore directory and returns that directory.
// It deletes nothing.
func swap(name string, staged map[string]string) (string, error) {
	defer discard(staged)
	aside := filepath.Join(appDir(name), "pre-restore-"+time.Now().UTC().Format(stampLayout))
	for db, tmp := range staged {
		// The restored file gets the owner and the mode of the file it replaces.
		owner, err := os.Stat(db)
		if err != nil {
			if owner, err = os.Stat(filepath.Dir(db)); err != nil {
				return "", err
			}
		} else if err := os.Chmod(tmp, owner.Mode().Perm()); err != nil {
			return "", err
		}
		if st, ok := owner.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(tmp, int(st.Uid), int(st.Gid)); err != nil {
				return "", err
			}
		}

		rel, _ := filepath.Rel(appDir(name), db)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(aside, rel)), 0700); err != nil {
			return "", err
		}
		// A leftover -wal file would replay old writes into the restored database.
		// The state of the live replica belongs to the old database too.
		state := filepath.Join(filepath.Dir(db), "."+filepath.Base(db)+"-litestream")
		moves := map[string]string{state: filepath.Join(filepath.Dir(filepath.Join(aside, rel)), filepath.Base(state))}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			moves[db+suffix] = filepath.Join(aside, rel) + suffix
		}
		for from, to := range moves {
			if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
		if err := os.Rename(tmp, db); err != nil {
			return "", err
		}
	}
	return aside, nil
}
