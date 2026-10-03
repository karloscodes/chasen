package main

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The themes of the screen:
//
//   - chasen: the amber of the Chasen mark. The default.
//   - monochrome: no color, only bold and dim.
//   - terminal: the 16 colors of the terminal, so the theme of the terminal
//     applies. On Omarchy, each theme sets those colors, so the screen
//     follows a switch of theme by itself.
//
// t in the screen goes to the next one and remembers it. CHASEN_THEME wins
// over the choice.

// The colors of the chasen theme, as the terminal can show them.
var chasenColors = [3]string{colorAccent, colorDim, colorBad}

// themeChoice is the theme that t chose, or "" for the default.
var themeChoice = loadThemeChoice()

func themePath() string { return filepath.Join(filepath.Dir(credentialsPath()), "theme") }

func loadThemeChoice() string {
	data, _ := os.ReadFile(themePath())
	return strings.TrimSpace(string(data))
}

// themes are the themes of the screen, in the order of t.
func themes() []string { return []string{"chasen", "monochrome", "terminal"} }

// currentTheme is the theme that the screen shows now.
func currentTheme() string {
	chosen := cmp.Or(os.Getenv("CHASEN_THEME"), themeChoice)
	switch chosen {
	case "ansi", "omarchy":
		chosen = "terminal" // the names of this theme before
	}
	if slices.Contains(themes(), chosen) {
		return chosen
	}
	return themes()[0]
}

// nextTheme goes to the next theme, remembers it, and returns its name.
func nextTheme() string {
	list := themes()
	themeChoice = list[(slices.Index(list, currentTheme())+1)%len(list)]
	os.MkdirAll(filepath.Dir(themePath()), 0700)
	os.WriteFile(themePath(), []byte(themeChoice+"\n"), 0600)
	followTheme()
	return themeChoice
}

// followTheme sets the colors of the current theme.
func followTheme() {
	switch currentTheme() {
	case "terminal":
		colorAccent, colorDim, colorBad = "33", "90", "31"
	case "monochrome":
		colorAccent, colorDim, colorBad = "1", "2", "1;4" // bold, dim, and bold underlined
	default:
		colorAccent, colorDim, colorBad = chasenColors[0], chasenColors[1], chasenColors[2]
	}
}
