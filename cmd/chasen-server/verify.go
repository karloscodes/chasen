package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// replicaMayBeBehind is how long the live replica may be behind a database
// before the hourly run calls it a failure. The replica is about a second
// behind when it works: ten minutes is a replica that stopped.
const replicaMayBeBehind = 10 * time.Minute

// replicaPosition is how far a database and its replica are, in the count
// that Litestream keeps: the number of the last transaction it wrote down
// here, and of the last one that is in the bucket.
type replicaPosition struct{ Local, Replica uint64 }

// saveReplicaState writes down the position of every replica. The replica
// daemon calls it every few seconds. A replica that has every transaction of
// its database is in sync. For one that has not, the state keeps the moment
// it was first seen behind, so a short delay and a replica that stopped are
// two different things.
func saveReplicaState(positions map[string]replicaPosition, now time.Time) error {
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	behind := map[string]string{}
	rows, err := tx.Query("SELECT db, behind_since FROM replica_state WHERE behind_since IS NOT NULL")
	if err != nil {
		return err
	}
	for rows.Next() {
		var path, since string
		if err := rows.Scan(&path, &since); err != nil {
			return err
		}
		behind[path] = since
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM replica_state"); err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339)
	for path, pos := range positions {
		var since any
		if pos.Replica < pos.Local {
			since = cmp.Or(behind[path], stamp)
		}
		rel, _ := filepath.Rel(root()+"/var/matcha", path)
		app, _, _ := strings.Cut(rel, string(filepath.Separator))
		if _, err := tx.Exec("INSERT INTO replica_state (db, app, local_txid, replica_txid, behind_since, checked_at) VALUES (?, ?, ?, ?, ?, ?)",
			path, app, pos.Local, pos.Replica, since, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// replicasBehind returns one line for each database whose replica has been
// behind for longer than replicaMayBeBehind. With an app, only its databases.
func replicasBehind(app string, now time.Time) ([]string, error) {
	db, err := openServerDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT db, app, local_txid, replica_txid, behind_since FROM replica_state WHERE behind_since IS NOT NULL AND (app = ? OR ? = '') ORDER BY db", app, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var behind []string
	for rows.Next() {
		var path, name, since string
		var local, replica uint64
		if err := rows.Scan(&path, &name, &local, &replica, &since); err != nil {
			return nil, err
		}
		from, err := time.Parse(time.RFC3339, since)
		if err != nil || now.Sub(from) <= replicaMayBeBehind {
			continue
		}
		rel, _ := filepath.Rel(appDir(name), path)
		behind = append(behind, fmt.Sprintf("the live replica of %s (%s) is behind since %s UTC: %d transactions are not in the bucket", name, rel, from.UTC().Format("15:04"), local-replica))
	}
	return behind, rows.Err()
}
