package main

import (
	"strings"
	"testing"
	"time"
)

func TestChooseRollback(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	kept := []keptVersion{
		{"727846a1b2c3d4e5", now.Add(-time.Hour)},
		{"6cff7df0a1b2c3d4", now.Add(-2 * time.Hour)},
		{"6cffaaa0a1b2c3d4", now.Add(-3 * time.Hour)},
	}
	const current = "3f9a2c1d5e8b7a6f"

	t.Run("with no version, it is the newest one the server keeps", func(t *testing.T) {
		got, err := chooseRollback(kept, current, "")

		if got != "727846a1b2c3d4e5" || err != nil {
			t.Errorf("chooseRollback = %q, %v", got, err)
		}
	})

	t.Run("a version is its full name, or its first 7 characters", func(t *testing.T) {
		for _, want := range []string{"6cff7df0a1b2c3d4", "6cff7df"} {
			if got, err := chooseRollback(kept, current, want); got != "6cff7df0a1b2c3d4" || err != nil {
				t.Errorf("chooseRollback(%q) = %q, %v", want, got, err)
			}
		}
	})

	t.Run("a version the server does not keep is refused, with what it keeps", func(t *testing.T) {
		_, err := chooseRollback(kept, current, "abcdef0")

		if err == nil || !strings.Contains(err.Error(), "keeps no version abcdef0") || !strings.Contains(err.Error(), "727846a1b2c3d4e5, 6cff7df0a1b2c3d4") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a short name that fits two versions asks for more of it", func(t *testing.T) {
		_, err := chooseRollback(kept, current, "6cff")

		if err == nil || !strings.Contains(err.Error(), "keeps no version 6cff") {
			t.Errorf("err = %v, want 4 characters to be too few to match", err)
		}
		if _, err := chooseRollback(append(kept, keptVersion{"6cff7df9", now}), current, "6cff7df"); err == nil || !strings.Contains(err.Error(), "names 2 versions") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("the version that runs is not a rollback", func(t *testing.T) {
		_, err := chooseRollback(kept, current, "3f9a2c1")

		if err == nil || !strings.Contains(err.Error(), "runs now") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("with nothing kept, it says how to get an older version", func(t *testing.T) {
		_, err := chooseRollback(nil, current, "")

		if err == nil || !strings.Contains(err.Error(), "chasen deploy --tag") {
			t.Errorf("err = %v", err)
		}
	})
}
