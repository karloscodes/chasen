package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
)

// The asset folder on a real Docker. It runs with CHASEN_TEST_DOCKER=1.
func TestAssetFolder(t *testing.T) {
	if os.Getenv("CHASEN_TEST_DOCKER") == "" {
		t.Skip("set CHASEN_TEST_DOCKER=1: this test builds images")
	}
	const assets = "/app/public/assets"
	// image builds an image from scratch with these files under the asset
	// path. A file whose content starts with "->" is a link.
	image := func(t *testing.T, tag string, files map[string]string) string {
		dir := t.TempDir()
		for name, content := range files {
			path := filepath.Join(dir, "assets", name)
			os.MkdirAll(filepath.Dir(path), 0755)
			if target, ok := strings.CutPrefix(content, "->"); ok {
				os.Symlink(target, path)
			} else {
				os.WriteFile(path, []byte(content), 0644)
			}
		}
		os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY assets "+assets+"\n"), 0644)
		name := "chasen.invalid/assets-test:" + tag
		if out, err := exec.Command("docker", "build", "-q", "-t", name, dir).CombinedOutput(); err != nil {
			t.Fatalf("docker build: %s", out)
		}
		t.Cleanup(func() { exec.Command("docker", "rmi", name).Run() })
		return name
	}
	files := func(t *testing.T) []string {
		var names []string
		dir := filepath.Join(appDir("shop"), "assets")
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && path != dir {
				rel, _ := filepath.Rel(dir, path)
				names = append(names, rel)
			}
			return nil
		})
		slices.Sort(names)
		return names
	}
	v1 := image(t, "1", map[string]string{"app-1.js": "one", "old/logo-1.svg": "logo", ".manifest.json": "1"})
	v2 := image(t, "2", map[string]string{"app-2.js": "two", ".manifest.json": "2", "link.js": "->/etc/passwd"})
	v3 := image(t, "3", map[string]string{"app-3.js": "three", ".manifest.json": "3"})

	t.Run("after a deploy, the folder has the files of the new version and of the version before", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		syncAssets("shop", v1, assets)

		_, err := syncAssets("shop", v2, assets)
		pruneAssets("shop", assets, v2, v1)

		if err != nil {
			t.Fatal(err)
		}
		want := []string{".manifest.json", "app-1.js", "app-2.js", "old", "old/logo-1.svg"}
		if got := files(t); !slices.Equal(got, want) {
			t.Errorf("the folder has %v, want %v", got, want)
		}
		if manifest, _ := os.ReadFile(filepath.Join(appDir("shop"), "assets", ".manifest.json")); string(manifest) != "2" {
			t.Errorf("the manifest is the one of version %q, want 2", manifest)
		}
	})

	t.Run("the next deploy removes the files of the version before the version before", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		syncAssets("shop", v1, assets)
		syncAssets("shop", v2, assets)
		pruneAssets("shop", assets, v2, v1)

		syncAssets("shop", v3, assets)
		pruneAssets("shop", assets, v3, v2)

		want := []string{".manifest.json", "app-2.js", "app-3.js"}
		if got := files(t); !slices.Equal(got, want) {
			t.Errorf("the folder has %v, want %v", got, want)
		}
	})

	t.Run("a link in the image is not an asset", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		syncAssets("shop", v2, assets)

		if _, err := os.Lstat(filepath.Join(appDir("shop"), "assets", "link.js")); err == nil {
			t.Error("the link of the image is in the folder")
		}
	})

	t.Run("root follows no link that the app put in the folder", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		outside := t.TempDir()
		os.MkdirAll(filepath.Join(appDir("shop"), "assets"), 0755)
		os.Symlink(outside, filepath.Join(appDir("shop"), "assets", "old"))

		syncAssets("shop", v1, assets)

		if entries, _ := os.ReadDir(outside); len(entries) > 0 {
			t.Errorf("root wrote %v outside the folder, through the link", entries)
		}
	})

	t.Run("an image without the folder stops the deploy, and the error names it", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		_, err := syncAssets("shop", v1, "/rails/public/assets")

		if err == nil || !strings.Contains(err.Error(), "/rails/public/assets") {
			t.Errorf("syncAssets() = %v, want an error that names the asset path", err)
		}
	})
}

// The live replica and the backups look for databases in the folders of an
// app, and open each file to read its header. The asset folder holds the
// files of an image, never a database.
func TestDatabasesAreNotLookedForInTheAssets(t *testing.T) {
	t.Setenv("CHASEN_ROOT", t.TempDir())
	if err := saveSettings("shop", protocol.Settings{AssetPath: "/rails/public/assets"}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(dataDir("shop"), 0755)
	os.MkdirAll(filepath.Join(appDir("shop"), "assets"), 0755)
	query(t, filepath.Join(dataDir("shop"), "production.sqlite3"), "CREATE TABLE orders (name); SELECT 1")
	query(t, filepath.Join(appDir("shop"), "assets", "demo.sqlite3"), "CREATE TABLE orders (name); SELECT 1")

	dbs, err := findDatabases(appDir("shop"), nil, assetDir("shop"))

	if err != nil || !slices.Equal(dbs, []string{"storage/production.sqlite3"}) {
		t.Errorf("found %v, %v; want only storage/production.sqlite3", dbs, err)
	}
}
