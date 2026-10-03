package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// The screen takes the colors of the Omarchy theme when the computer has one,
// and follows it when the theme changes: Omarchy keeps the colors of the
// current theme in one file. CHASEN_THEME=ansi uses the 16 colors of the
// terminal instead, so the screen follows any other terminal theme.

// omarchyColors is the colors file of the current Omarchy theme. Older
// versions of Omarchy keep the theme in ~/.config.
func omarchyColors() []string {
	home, _ := os.UserHomeDir()
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return []string{
		filepath.Join(state, "omarchy", "current", "theme", "colors.toml"),
		filepath.Join(home, ".config", "omarchy", "current", "theme", "colors.toml"),
	}
}

var (
	themeColor = regexp.MustCompile(`(?m)^\s*([a-z_]+)\s*=\s*"#([0-9a-fA-F]{6})"`)
	themeRead  string // the content of the colors file at the last look
)

// followTheme sets the colors from the theme, when the theme changed since the
// last call. The screen calls it once a second.
func followTheme() {
	if os.Getenv("CHASEN_THEME") == "ansi" {
		colorAccent, colorDim, colorBad = "33", "90", "31"
		return
	}
	for _, path := range omarchyColors() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if string(data) == themeRead {
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
		return
	}
}

// truecolor is the escape code of a foreground color in 24 bits.
func truecolor(hex string) string {
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return "38;2;" + strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(b))
}
