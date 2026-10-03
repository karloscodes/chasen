package main

import (
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The themes of the screen:
//
//   - chasen: the amber of the Chasen mark. The default.
//   - monochrome: no color, only bold and dim.
//   - omarchy: the colors of the current Omarchy theme. Omarchy keeps them in
//     one file, and the screen follows it when the theme changes.
//   - terminal: the 16 colors of the terminal, so any terminal theme applies.
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

// themes are the themes this computer can show, in the order of t.
func themes() []string {
	if omarchyColorsFile() != "" {
		return []string{"chasen", "monochrome", "omarchy", "terminal"}
	}
	return []string{"chasen", "monochrome", "terminal"}
}

// currentTheme is the theme that the screen shows now.
func currentTheme() string {
	chosen := cmp.Or(os.Getenv("CHASEN_THEME"), themeChoice)
	if chosen == "ansi" {
		chosen = "terminal" // the first name of this theme
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
	themeRead = "" // the next look reads the colors again
	followTheme()
	return themeChoice
}

// omarchyColorsFile is the colors file of the current Omarchy theme, or "".
// Older versions of Omarchy keep the theme in ~/.config.
func omarchyColorsFile() string {
	home, _ := os.UserHomeDir()
	state := cmp.Or(os.Getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
	for _, path := range []string{
		filepath.Join(state, "omarchy", "current", "theme", "colors.toml"),
		filepath.Join(home, ".config", "omarchy", "current", "theme", "colors.toml"),
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

var (
	themeColor = regexp.MustCompile(`(?m)^\s*([a-z_]+)\s*=\s*"#([0-9a-fA-F]{6})"`)
	themeRead  string // the content of the Omarchy colors file at the last look
)

// followTheme sets the colors of the current theme. The screen calls it once
// a second, so it follows a switch of the Omarchy theme.
func followTheme() {
	switch currentTheme() {
	case "terminal":
		colorAccent, colorDim, colorBad = "33", "90", "31"
		themeRead = ""
	case "chasen":
		colorAccent, colorDim, colorBad = chasenColors[0], chasenColors[1], chasenColors[2]
		themeRead = ""
	case "monochrome":
		colorAccent, colorDim, colorBad = "1", "2", "1;4" // bold, dim, and bold underlined
		themeRead = ""
	case "omarchy":
		data, err := os.ReadFile(omarchyColorsFile())
		if err != nil || string(data) == themeRead {
			return
		}
		themeRead = string(data)
		colors := map[string]string{}
		for _, m := range themeColor.FindAllStringSubmatch(themeRead, -1) {
			colors[m[1]] = m[2]
		}
		for _, use := range []struct {
			color *string
			names []string
		}{
			{&colorAccent, []string{"accent"}},
			{&colorDim, []string{"dark_foreground", "muted"}},
			{&colorBad, []string{"red"}},
		} {
			for _, name := range use.names {
				if hex, ok := colors[name]; ok {
					*use.color = truecolor(hex)
					break
				}
			}
		}
	}
}

// truecolor is the escape code of a foreground color in 24 bits.
func truecolor(hex string) string {
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return "38;2;" + strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(b))
}
