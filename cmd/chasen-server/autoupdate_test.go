package main

import (
	"testing"
	"time"

	"github.com/karloscodes/chasen/protocol"
)

func TestMergeSettings(t *testing.T) {
	on, off := true, false
	saved := protocol.Settings{Image: "ghcr.io/acme/chat:latest", Env: map[string]string{"GREETING": "hola"}, Health: "/_health", AutoUpdate: &on}

	t.Run("an image alone keeps the settings and the auto-update of the app", func(t *testing.T) {
		merged := mergeSettings(saved, protocol.Settings{Image: "ghcr.io/acme/chat:2", KeepSettings: true})

		if merged.Image != "ghcr.io/acme/chat:2" || merged.Env["GREETING"] != "hola" || merged.Health != "/_health" || merged.AutoUpdate == nil || !*merged.AutoUpdate || merged.KeepSettings {
			t.Errorf("merged = %+v, want the new image, the saved env and health, auto-update on, and no keep_settings", merged)
		}
	})

	t.Run("a deploy from a folder replaces the settings and keeps the auto-update", func(t *testing.T) {
		merged := mergeSettings(saved, protocol.Settings{Image: "ghcr.io/acme/chat:latest", Env: map[string]string{}})

		if len(merged.Env) != 0 || merged.Health != "" || merged.AutoUpdate == nil || !*merged.AutoUpdate {
			t.Errorf("merged = %+v, want the settings of the deploy, with auto-update on", merged)
		}
	})

	t.Run("--no-auto-update turns it off", func(t *testing.T) {
		merged := mergeSettings(saved, protocol.Settings{Image: "ghcr.io/acme/chat:latest", KeepSettings: true, AutoUpdate: &off})

		if merged.AutoUpdate != nil {
			t.Errorf("auto-update = %v, want off", *merged.AutoUpdate)
		}
	})

	t.Run("a website or an image of a computer cannot auto-update", func(t *testing.T) {
		for _, image := range []string{"", "127.0.0.1:41234/shop@sha256:abc"} {
			merged := mergeSettings(saved, protocol.Settings{Image: image, AutoUpdate: &on})

			if merged.AutoUpdate != nil {
				t.Errorf("image %q: auto-update on, want off: the server cannot pull it at night", image)
			}
		}
	})
}

func TestAutoUpdateDue(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	t.Run("in the minute of the night", func(t *testing.T) {
		if !autoUpdateDue(at("2026-10-08 05:30"), at("2026-10-08 05:30")) {
			t.Error("05:30 UTC is not due")
		}
	})

	t.Run("after a restart that spans the minute", func(t *testing.T) {
		if !autoUpdateDue(at("2026-10-08 05:25"), at("2026-10-08 05:34")) {
			t.Error("the minutes 05:25 to 05:34 do not hold 05:30")
		}
	})

	t.Run("not in any other minute", func(t *testing.T) {
		if autoUpdateDue(at("2026-10-08 05:31"), at("2026-10-08 06:00")) || autoUpdateDue(at("2026-10-08 05:29"), at("2026-10-08 05:29")) {
			t.Error("a minute other than 05:30 is due")
		}
	})

	t.Run("not when the loop looked at no minute", func(t *testing.T) {
		if autoUpdateDue(at("2026-10-08 05:30"), time.Time{}) {
			t.Error("an empty window is due")
		}
	})
}
