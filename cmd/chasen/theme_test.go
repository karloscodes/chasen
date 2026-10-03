package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScreenColors(t *testing.T) {
	// Each case gets a computer of its own, and the colors come back after it.
	computer := func(t *testing.T, omarchy string) string {
		accent, dim, bad, read, choice := colorAccent, colorDim, colorBad, themeRead, themeChoice
		t.Cleanup(func() { colorAccent, colorDim, colorBad, themeRead, themeChoice = accent, dim, bad, read, choice })
		themeChoice, themeRead = "", ""
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("CHASEN_THEME", "")
		path := filepath.Join(state, "omarchy", "current", "theme", "colors.toml")
		if omarchy != "" {
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte(omarchy), 0644)
		}
		return path
	}
	const tokyoNight = "mode = \"dark\"\naccent = \"#7aa2f7\"\nmuted = \"#414868\"\ndark_foreground = \"#565f89\"\nred = \"#f7768e\"\n"

	t.Run("on Omarchy, take the accent, the dim text, and the red of its theme", func(t *testing.T) {
		computer(t, tokyoNight)

		followTheme()

		if colorAccent != "38;2;122;162;247" || colorDim != "38;2;86;95;137" || colorBad != "38;2;247;118;142" {
			t.Errorf("colors = %q, %q, %q", colorAccent, colorDim, colorBad)
		}
	})

	t.Run("follow the theme when Omarchy switches it", func(t *testing.T) {
		path := computer(t, tokyoNight)
		followTheme()

		os.WriteFile(path, []byte("accent = \"#205EA6\"\n"), 0644)
		followTheme()

		if colorAccent != "38;2;32;94;166" {
			t.Errorf("accent = %q, want the one of the new theme", colorAccent)
		}
	})

	t.Run("keep the Chasen colors with no Omarchy theme", func(t *testing.T) {
		computer(t, "")

		followTheme()

		if colorAccent != chasenColors[0] {
			t.Errorf("accent = %q, want the Chasen amber %q", colorAccent, chasenColors[0])
		}
	})

	t.Run("t goes through the themes and remembers the choice", func(t *testing.T) {
		computer(t, tokyoNight)

		first, second, third := nextTheme(), nextTheme(), nextTheme()

		if first != "chasen" || second != "terminal" || third != "omarchy" {
			t.Errorf("t gave %s, %s, %s, want chasen, terminal, omarchy", first, second, third)
		}
		nextTheme()
		if loadThemeChoice() != "chasen" || colorAccent != chasenColors[0] {
			t.Errorf("the saved choice = %q, accent %q, want chasen", loadThemeChoice(), colorAccent)
		}
	})

	t.Run("CHASEN_THEME=ansi uses the colors of the terminal", func(t *testing.T) {
		computer(t, tokyoNight)
		t.Setenv("CHASEN_THEME", "ansi")

		followTheme()

		if colorAccent != "33" || colorDim != "90" || colorBad != "31" {
			t.Errorf("colors = %q, %q, %q", colorAccent, colorDim, colorBad)
		}
	})
}
