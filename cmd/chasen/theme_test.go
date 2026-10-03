package main

import (
	"testing"
)

func TestScreenColors(t *testing.T) {
	// Each case gets a computer of its own, and the colors come back after it.
	computer := func(t *testing.T) {
		accent, dim, bad, choice := colorAccent, colorDim, colorBad, themeChoice
		t.Cleanup(func() { colorAccent, colorDim, colorBad, themeChoice = accent, dim, bad, choice })
		themeChoice = ""
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("CHASEN_THEME", "")
	}

	t.Run("the Chasen colors are the default", func(t *testing.T) {
		computer(t)

		followTheme()

		if colorAccent != chasenColors[0] {
			t.Errorf("accent = %q, want the Chasen amber %q", colorAccent, chasenColors[0])
		}
	})

	t.Run("t goes through the themes and remembers the choice", func(t *testing.T) {
		computer(t)

		first, second, third := nextTheme(), nextTheme(), nextTheme()

		if first != "monochrome" || second != "terminal" || third != "chasen" {
			t.Errorf("t gave %s, %s, %s, want monochrome, terminal, chasen", first, second, third)
		}
		nextTheme()
		if loadThemeChoice() != "monochrome" || colorAccent != "1" || colorDim != "2" {
			t.Errorf("the saved choice = %q, accent %q, want monochrome: bold and dim", loadThemeChoice(), colorAccent)
		}
	})

	t.Run("terminal uses the 16 colors of the terminal, also under its names of before", func(t *testing.T) {
		for _, name := range []string{"terminal", "ansi", "omarchy"} {
			computer(t)
			t.Setenv("CHASEN_THEME", name)

			followTheme()

			if colorAccent != "33" || colorDim != "90" || colorBad != "31" {
				t.Errorf("CHASEN_THEME=%s: colors = %q, %q, %q", name, colorAccent, colorDim, colorBad)
			}
		}
	})
}
