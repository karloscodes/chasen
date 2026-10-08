package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLogFiles(t *testing.T) {
	t.Run("finds log files and their rotated copies, by their path in the storage", func(t *testing.T) {
		storage := filepath.Join(t.TempDir(), "storage")
		for _, file := range []string{"app.db", "logs/app.log", "logs/app.log.1", "logs/app-2026-10-08.log.gz", "uploads/report.pdf", "catalog.json"} {
			os.MkdirAll(filepath.Dir(filepath.Join(storage, file)), 0755)
			os.WriteFile(filepath.Join(storage, file), []byte("x"), 0644)
		}

		files := logFiles([]string{storage})

		want := []string{"storage/logs/app-2026-10-08.log.gz", "storage/logs/app.log", "storage/logs/app.log.1"}
		slices.Sort(files)
		if !slices.Equal(files, want) {
			t.Errorf("logFiles = %v, want %v", files, want)
		}
	})

	t.Run("finds nothing in a storage of data only", func(t *testing.T) {
		storage := t.TempDir()
		os.WriteFile(filepath.Join(storage, "app.db"), []byte("x"), 0644)

		if files := logFiles([]string{storage, filepath.Join(storage, "missing")}); len(files) != 0 {
			t.Errorf("logFiles = %v, want none", files)
		}
	})
}
