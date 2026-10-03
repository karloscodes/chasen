package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScreenColors(t *testing.T) {
	keep := func(t *testing.T) {
		accent, dim, bad, read := colorAccent, colorDim, colorBad, themeRead
		t.Cleanup(func() { colorAccent, colorDim, colorBad, themeRead = accent, dim, bad, read })
	}
	omarchy := func(t *testing.T, colors string) string {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("HOME", t.TempDir())
		path := filepath.Join(state, "omarchy", "current", "theme", "colors.toml")
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte(colors), 0644)
		return path
	}

	t.Run("take the accent, the dim text, and the red of the Omarchy theme", func(t *testing.T) {
		keep(t)
		omarchy(t, "mode = \"dark\"\naccent = \"#7aa2f7\"\nmuted = \"#414868\"\ndark_foreground = \"#565f89\"\nred = \"#f7768e\"\n")

		followTheme()

		if colorAccent != "38;2;122;162;247" || colorDim != "38;2;86;95;137" || colorBad != "38;2;247;118;142" {
			t.Errorf("colors = %q, %q, %q", colorAccent, colorDim, colorBad)
		}
	})

	t.Run("follow the theme when Omarchy switches it", func(t *testing.T) {
		keep(t)
		path := omarchy(t, "accent = \"#7aa2f7\"\n")
		followTheme()

		os.WriteFile(path, []byte("accent = \"#205EA6\"\n"), 0644)
		followTheme()

		if colorAccent != "38;2;32;94;166" {
			t.Errorf("accent = %q, want the one of the new theme", colorAccent)
		}
	})

	t.Run("keep the Chasen colors with no Omarchy theme", func(t *testing.T) {
		keep(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		before := colorAccent

		followTheme()

		if colorAccent != before {
			t.Errorf("accent = %q, want the Chasen amber %q", colorAccent, before)
		}
	})

	t.Run("CHASEN_THEME=ansi uses the colors of the terminal", func(t *testing.T) {
		keep(t)
		t.Setenv("CHASEN_THEME", "ansi")

		followTheme()

		if colorAccent != "33" || colorDim != "90" || colorBad != "31" {
			t.Errorf("colors = %q, %q, %q", colorAccent, colorDim, colorBad)
		}
	})
}
