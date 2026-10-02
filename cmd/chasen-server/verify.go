package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A copy that nobody restored is a hope, not a backup. These are the two
// checks that turn the copies of the bucket into something to trust.

// serverVerify proves that the copies of an app restore. It restores the live
// replica of each database to a file next to the real one, runs the
// integrity check of SQLite on it, and removes it. Then it does the same
// with the newest snapshot. It changes nothing: the app keeps running on its
// own databases.
func serverVerify(name string) error {
	if _, err := loadApp(name); err != nil {
		return err
	}
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	if !backedUp(name) {
		fmt.Printf("%s has no backups: chasen.yml says backup: false\n", name)
		return nil
	}
	fmt.Printf("Checking the copies of %s. Nothing changes.\n", name)
	failed := 0
	report := func(what string, staged map[string]string, err error) {
		defer discard(staged)
		if err != nil {
			fmt.Printf("  FAIL  %s: %v\n", what, err)
			failed++
			return
		}
		for db, restored := range staged {
			rel, _ := filepath.Rel(appDir(name), db)
			size := int64(0)
			if info, err := os.Stat(restored); err == nil {
				size = info.Size()
			}
			fmt.Printf("  ok    %s restores %s (%s), and it passes the integrity check\n", what, rel, megabytes(size))
		}
	}

	if cfg.Backup.S3 != nil {
		staged, err := stageLive(name, cfg.Backup.S3)
		if err == nil && len(staged) == 0 {
			err = errors.New("the bucket has no live replica of this app")
		}
		report("the live replica", staged, err)
	} else {
		fmt.Println("  skip  the live replica (this server has no bucket)")
	}

	stamp, err := fetchBackup(name, "", cfg)
	if errors.Is(err, errNoBackups) {
		fmt.Println("  skip  the newest snapshot (the app has none yet)")
	} else if err != nil {
		report("the newest snapshot", nil, err)
	} else {
		staged, err := stageBackup(name, stamp)
		report("the snapshot "+stamp, staged, err)
	}

	if failed > 0 {
		return fmt.Errorf("%d check(s) failed. The live app did not change", failed)
	}
	fmt.Println("The copies restore.")
	return nil
}

// replicaMayBeBehind is how far the live replica may be behind a database
// before the hourly run calls it a failure. The replica is about a second
// behind when it works: ten minutes is a replica that stopped.
const replicaMayBeBehind = 10 * time.Minute

// replicaGap returns how far the live replica of a database is behind: the
// time between the last change of the database and the newest file of its
// replica. A database that did not change since the replica got its last
// file has no gap.
func replicaGap(changed time.Time, replica []s3Object) time.Duration {
	var newest time.Time
	for _, object := range replica {
		if object.LastModified.After(newest) {
			newest = object.LastModified
		}
	}
	if changed.Before(newest) {
		return 0
	}
	return changed.Sub(newest)
}

// replicaBehind returns the databases of an app whose live replica is behind
// by more than replicaMayBeBehind, each with a line that says how far.
func replicaBehind(name string, s3 *s3Config) ([]string, error) {
	dbs, err := findDatabases(appDir(name))
	if err != nil {
		return nil, err
	}
	var behind []string
	for _, rel := range dbs {
		// A change is in the -wal file first, and in the database after a checkpoint.
		var changed time.Time
		for _, file := range []string{rel, rel + "-wal"} {
			if info, err := os.Stat(filepath.Join(appDir(name), file)); err == nil && info.ModTime().After(changed) {
				changed = info.ModTime()
			}
		}
		replica, err := s3.objects(name + "/live/" + rel + "/")
		if err != nil {
			return nil, err
		}
		if gap := replicaGap(changed, replica); gap > replicaMayBeBehind {
			behind = append(behind, fmt.Sprintf("%s of %s is %s behind", rel, name, age(gap)))
		}
	}
	return behind, nil
}
