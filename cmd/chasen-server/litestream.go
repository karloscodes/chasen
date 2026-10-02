package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/benbjohnson/litestream"
	lss3 "github.com/benbjohnson/litestream/s3"
)

// The live replica. Litestream is inside this binary. `chasen-server replicate`
// is a daemon, started by `serve`, that streams every change of every app database to the S3
// bucket, about one second behind. The hourly backups are the checked copies
// with a long history. The live replica is the copy that loses almost nothing
// when the server dies.

func replicaLockPath() string { return root() + "/etc/chasen/replica.lock" }

// replicaLock stays open for the life of the process. A closed file drops its lock.
var replicaLock *os.File

func openReplicaLock() error {
	if err := os.MkdirAll(filepath.Dir(replicaLockPath()), 0755); err != nil {
		return err
	}
	var err error
	replicaLock, err = os.OpenFile(replicaLockPath(), os.O_CREATE|os.O_RDWR, 0600)
	return err
}

// replicaPID returns the process id of the running daemon, or 0.
func replicaPID() int {
	if replicaLock == nil && openReplicaLock() != nil {
		return 0
	}
	lk := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(replicaLock.Fd(), syscall.F_GETLK, &lk); err != nil || lk.Type == syscall.F_UNLCK {
		return 0
	}
	return int(lk.Pid)
}

// pauseReplica stops the daemon and keeps it stopped until this process exits.
// The daemon makes a last sync before it stops. `serve` starts it again, and
// it waits for the lock.
func pauseReplica() error {
	if pid := replicaPID(); pid != 0 {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	return waitForReplicaLock()
}

func waitForReplicaLock() error {
	if replicaLock == nil {
		if err := openReplicaLock(); err != nil {
			return err
		}
	}
	return syscall.FcntlFlock(replicaLock.Fd(), syscall.F_SETLKW, &syscall.Flock_t{Type: syscall.F_WRLCK})
}

func replicaClient(s3 *s3Config, name, rel string) *lss3.ReplicaClient {
	c := lss3.NewReplicaClient()
	c.AccessKeyID, c.SecretAccessKey = s3.AccessKeyID, s3.SecretAccessKey
	c.Region, c.Bucket, c.Endpoint = s3.Region, s3.Bucket, s3.Endpoint
	c.Path = name + "/live/" + rel
	c.ForcePathStyle = true
	return c
}

// serverReplicate is the daemon. It replicates each database of each deployed
// app. Every few seconds it looks for databases that are new, gone, or replaced.
func serverReplicate() error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	if cfg.Backup.S3 == nil {
		return errors.New("no live replica: this server has no bucket")
	}
	// One daemon at a time. A restore holds this lock while it replaces the databases.
	if err := waitForReplicaLock(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	store := litestream.NewStore(nil, litestream.DefaultCompactionLevels)
	if err := store.Open(ctx); err != nil {
		return err
	}
	registered := map[string]os.FileInfo{}
	for {
		current := map[string]os.FileInfo{}
		clients := map[string]*lss3.ReplicaClient{}
		apps, err := listApps()
		if err != nil {
			slog.Error("cannot read the apps", "error", err)
		}
		for name := range apps {
			dbs, err := findDatabases(appDir(name))
			if err != nil {
				slog.Error("cannot read the data directory", "app", name, "error", err)
			}
			for _, rel := range dbs {
				path := filepath.Join(appDir(name), rel)
				if info, err := os.Stat(path); err == nil {
					current[path], clients[path] = info, replicaClient(cfg.Backup.S3, name, rel)
				}
			}
		}

		// A restore replaces the file of a database. The replica must start again on the new file.
		for path, info := range registered {
			if now, ok := current[path]; !ok || !os.SameFile(info, now) {
				if err := store.UnregisterDB(ctx, path); err != nil {
					slog.Error("cannot stop the replica", "db", path, "error", err)
				}
				delete(registered, path)
			}
		}
		for path, info := range current {
			if _, ok := registered[path]; ok {
				continue
			}
			db := litestream.NewDB(path)
			db.Replica = litestream.NewReplicaWithClient(db, clients[path])
			if err := store.RegisterDB(db); err != nil {
				slog.Error("cannot start the replica", "db", path, "error", err)
				continue
			}
			registered[path] = info
		}

		select {
		case <-ctx.Done():
			return store.Close(context.Background())
		case <-time.After(10 * time.Second):
		}
	}
}

// liveDatabases returns the databases of the app that have a live replica.
// A replica object has the key <app>/live/<database>/<level>/<file>.ltx
func liveDatabases(name string, s3 *s3Config) ([]string, error) {
	keys, err := s3.list(name + "/live/")
	if err != nil {
		return nil, err
	}
	var dbs []string
	for _, key := range keys {
		rel := filepath.Dir(filepath.Dir(strings.TrimPrefix(key, name+"/live/")))
		if !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("unsafe object key %q", key)
		}
		if !slices.Contains(dbs, rel) {
			dbs = append(dbs, rel)
		}
	}
	return dbs, nil
}

// stageLive restores the newest state of every database of the app from the
// live replica, checked and ready for swap. It returns nothing when the
// replica has no database for the app.
func stageLive(name string, s3 *s3Config) (staged map[string]string, err error) {
	dbs, err := liveDatabases(name, s3)
	if err != nil || len(dbs) == 0 {
		return nil, err
	}
	slog.SetLogLoggerLevel(slog.LevelWarn) // Litestream logs each step at the info level

	staged = map[string]string{}
	defer func() {
		if err != nil {
			discard(staged)
		}
	}()
	for _, rel := range dbs {
		db := filepath.Join(appDir(name), rel)
		if err := os.MkdirAll(filepath.Dir(db), 0755); err != nil {
			return nil, err
		}
		tmp, err := stagingFile(name, db)
		if err != nil {
			return nil, err
		}
		staged[db] = tmp
		replica := litestream.NewReplicaWithClient(litestream.NewDB(db), replicaClient(s3, name, rel))
		if err := replica.Restore(context.Background(), litestream.RestoreOptions{OutputPath: tmp}); err != nil {
			return nil, fmt.Errorf("live restore of %s failed: %w", rel, err)
		}
		// Fold the -wal file into the database, so the swap moves one complete file.
		if err := sqliteExec(tmp, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return nil, err
		}
		os.Remove(tmp + "-wal")
		os.Remove(tmp + "-shm")
		if err := checkIntegrity(tmp); err != nil {
			return nil, err
		}
	}
	return staged, nil
}

// stageOffsite gets the data of an app that has no data on this server: the
// live replica first, then the newest backup. This is how an app comes back on
// a new server. It returns nothing when there is no copy anywhere.
func stageOffsite(name string, cfg serverConfig) (map[string]string, string, error) {
	if cfg.Backup.S3 != nil {
		staged, err := stageLive(name, cfg.Backup.S3)
		if len(staged) > 0 {
			return staged, "the live replica", nil
		}
		if err != nil {
			fmt.Printf("Warning: %v\nTrying the newest backup.\n", err)
		}
	}
	stamp, err := fetchBackup(name, "", cfg)
	if errors.Is(err, errNoBackups) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	staged, err := stageBackup(name, stamp)
	return staged, "backup " + stamp, err
}
