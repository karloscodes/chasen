package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWaybarModule(t *testing.T) {
	t.Run("counts the alerts of every server, and lists them in the tooltip", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		s := newTestServer(t)
		if err := (logins{Current: s.URL, Tokens: map[string]string{s.URL: "token"}}).save(); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder

		err := waybarAlerts(&out)

		var line waybarLine
		if err != nil || json.Unmarshal([]byte(out.String()), &line) != nil {
			t.Fatalf("waybar = %q, %v", out.String(), err)
		}
		if line.Text != "● 2" || line.Class != "error" || !strings.Contains(line.Tooltip, "error    blog is down") || !strings.Contains(line.Tooltip, "warning  SSH accepts passwords") {
			t.Errorf("waybar = %+v", line)
		}
	})

	t.Run("nothing to say hides the module", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		var out strings.Builder

		waybarAlerts(&out)

		if !strings.Contains(out.String(), `"text":""`) {
			t.Errorf("waybar = %q, want no text", out.String())
		}
	})
}
