package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/engine"
	"github.com/karloscodes/chasen/protocol"
)

func TestOverviewMap(t *testing.T) {
	t.Run("gives each app its domains, its jobs, and where its data goes", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("shop", engine.AppConfig{Image: "chasen.invalid/shop:abc1234", Domain: "shop.com,www.shop.com"})
		saveSettings("shop", protocol.Settings{Jobs: "bin/jobs"})
		newest := filepath.Join(backupsDir("shop"), "20261020T120000Z", "data")
		os.MkdirAll(newest, 0755)
		os.WriteFile(filepath.Join(newest, "db.sqlite3.gz"), []byte("x"), 0644)
		os.WriteFile(filepath.Join(newest, "queue.sqlite3.gz"), []byte("x"), 0644)
		os.MkdirAll(filepath.Join(backupsDir("shop"), "20261020T110000Z"), 0755)
		saveApp("blog", engine.AppConfig{Image: "chasen.invalid/blog:1", Domain: "blog.example.com"})
		saveSettings("blog", protocol.Settings{NoBackup: true})

		rows, err := overview()

		if err != nil || len(rows) != 2 {
			t.Fatalf("overview = %+v, %v", rows, err)
		}
		blog, shop := rows[0], rows[1]
		if !reflect.DeepEqual(shop.Domains, []string{"shop.com", "www.shop.com"}) || shop.Jobs != "not running" {
			t.Errorf("shop = %+v, want its two domains, and its jobs container, not running", shop)
		}
		if shop.Backup != "20261020T120000Z" || shop.Databases != 2 || shop.Replica != "off" {
			t.Errorf("shop = %+v, want the newest backup with its 2 databases, and no replica: no bucket", shop)
		}
		if blog.Backup != "off" || blog.Replica != "off" || blog.Jobs != "" {
			t.Errorf("blog = %+v, want its backups off, and no jobs", blog)
		}
	})

	t.Run("a replica that has not copied for ten minutes is behind", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		db, err := openServerDB()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		_, err = db.Exec("INSERT INTO replica_state (db, app, local_txid, replica_txid, behind_since, checked_at) VALUES (?, 'shop', 9, 4, ?, ?), (?, 'blog', 9, 8, ?, ?)",
			"/var/matcha/shop/data/db.sqlite3", now.Add(-11*time.Minute).Format(time.RFC3339), now.Format(time.RFC3339),
			"/var/matcha/blog/data/db.sqlite3", now.Add(-time.Minute).Format(time.RFC3339), now.Format(time.RFC3339))
		if err != nil {
			t.Fatal(err)
		}

		behind := behindReplicas(db, now)

		if !reflect.DeepEqual(behind, map[string]bool{"shop": true}) {
			t.Errorf("behind = %v, want shop only: blog is one minute behind", behind)
		}
	})
}
