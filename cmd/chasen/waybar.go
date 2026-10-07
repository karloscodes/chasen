package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/karloscodes/chasen/protocol"
)

// chasen alerts --waybar is the module of Chasen for Waybar, the top bar of
// Omarchy: the alerts of every server you are logged in to, in one line of
// JSON. The text is the count, the tooltip lists them, and the class is
// error, warning, or nothing, so the style of the bar can color it. With no
// alert the text is empty, and Waybar hides the module.

type waybarLine struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
}

func waybarAlerts(w io.Writer) error {
	saved := loadLogins()
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	type answer struct {
		alerts []alertRow
		err    error
	}
	answers := make([]answer, len(addresses))
	var wg sync.WaitGroup
	for i, address := range addresses {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var out strings.Builder
			code, err := protocol.Client(credentials{URL: address, Token: saved.Tokens[address]}).Run(ctx, "alerts", nil, nil, &out)
			if err == nil && code != 0 {
				err = fmt.Errorf("it has no alerts yet: chasen-server update")
			}
			alerts, _ := parseAlerts(out.String())
			answers[i] = answer{alerts, err}
		})
	}
	wg.Wait()

	errors, warnings := 0, 0
	var tooltip []string
	for i, address := range addresses {
		name := barName(address)
		switch a := answers[i]; {
		case a.err != nil:
			warnings++
			tooltip = append(tooltip, name+": cannot ask it. "+cleanText(a.err.Error()))
		case len(a.alerts) == 0:
			tooltip = append(tooltip, name+": no alerts")
		default:
			tooltip = append(tooltip, name+":")
			for _, alert := range a.alerts {
				mark := "warning"
				if alert.error {
					mark, errors = "error  ", errors+1
				} else {
					warnings++
				}
				tooltip = append(tooltip, "  "+mark+"  "+alert.what)
			}
		}
	}
	// Waybar reads the tooltip as Pango markup: text from a server is escaped.
	line := waybarLine{Tooltip: html.EscapeString(strings.Join(tooltip, "\n"))}
	switch {
	case errors > 0:
		line.Text, line.Class = fmt.Sprintf("● %d", errors+warnings), "error"
	case warnings > 0:
		line.Text, line.Class = fmt.Sprintf("● %d", warnings), "warning"
	}
	if len(addresses) == 0 {
		line.Tooltip = "chasen: not logged in to a server"
	}
	return json.NewEncoder(w).Encode(line)
}

// barName is the short name of a server for the bar: the host of an SSH
// address, the base domain of an address on the web, or cloud.
func barName(address string) string {
	if protocol.IsSSH(address) {
		if u, err := url.Parse(address); err == nil {
			return cmp.Or(u.Hostname(), address)
		}
	}
	return serverName(address)
}
