package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
)

// Auto-update: an app deployed with --auto-update gets the newest image of
// its tag once a night, `latest` or a channel like `:2`. The API looks at
// autoUpdateHour:autoUpdateMinute UTC, after the window of the nightly update
// of chasen-server, from the minute loop of the cron jobs, so a restart of
// the API skips no night. It pulls the tag. A new image deploys like
// `chasen deploy`: a backup first, and the old version keeps the traffic when
// the new one does not get healthy. The history keeps each such deploy, and
// each pull that fails, as "auto-update". Once a night, like Watchtower: a
// bad release has a day to be pulled before it reaches every server.
const (
	autoUpdateHour   = 5
	autoUpdateMinute = 30
)

// autoUpdateDue reports that the minutes from..to hold the nightly auto-update.
func autoUpdateDue(from, to time.Time) bool {
	for minute := from.UTC(); !minute.After(to.UTC()); minute = minute.Add(time.Minute) {
		if minute.Hour() == autoUpdateHour && minute.Minute() == autoUpdateMinute {
			return true
		}
	}
	return false
}

// autoUpdates reports an app that auto-updates: it asked for it, and its
// image is in a registry that the server can pull at night. An image of a
// computer comes through an SSH tunnel that is open only during a deploy.
func autoUpdates(settings protocol.Settings) bool {
	return settings.AutoUpdate != nil && *settings.AutoUpdate &&
		settings.Image != "" && !strings.HasPrefix(settings.Image, "127.0.0.1:")
}

// mergeSettings returns the settings a deploy keeps. An image alone
// (KeepSettings) changes the image and keeps the rest of what the app has. A
// deploy that says nothing about auto-update keeps what the app has too.
func mergeSettings(saved, sent protocol.Settings) protocol.Settings {
	merged := sent
	if sent.KeepSettings {
		merged = saved
		merged.Image, merged.Registry, merged.Domain = sent.Image, sent.Registry, sent.Domain
	}
	merged.KeepSettings = false
	merged.AutoUpdate = sent.AutoUpdate
	if sent.AutoUpdate == nil {
		merged.AutoUpdate = saved.AutoUpdate
	}
	if !autoUpdates(merged) {
		merged.AutoUpdate = nil
	}
	return merged
}

// serverAutoUpdate runs the auto-update of every app that has it, or of one
// app, now. The nightly run of the API calls it; by hand it gets a fix out
// at once.
func serverAutoUpdate(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: chasen-server auto-update [app]")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	apps, err := listApps()
	if err != nil {
		return err
	}
	names := matcha.ListAppsSorted(apps)
	if len(args) == 1 {
		if err := checkAppName(args[0]); err != nil {
			return err
		}
		names = args
	}
	for _, name := range names {
		settings, err := loadSettings(name)
		if err != nil || !autoUpdates(settings) {
			if len(args) == 1 {
				return fmt.Errorf("%s does not auto-update. Turn it on: chasen deploy <image> --auto-update", name)
			}
			continue
		}
		autoUpdateApp(self, name, settings.Image)
	}
	return nil
}

// autoUpdateApp deploys the newest image of the tag when it is not the one
// that runs.
func autoUpdateApp(self, name, image string) {
	newer, err := isNewerImage(name, image)
	if err == nil && !newer {
		fmt.Printf("%s: the newest %s runs already.\n", name, image)
		return
	}
	db, dbErr := openServerDB()
	if dbErr != nil {
		log.Print("auto-update: ", dbErr)
		return
	}
	defer db.Close()
	id, dbErr := startActivity(db, name, "auto-update")
	if dbErr != nil {
		log.Print("auto-update: ", dbErr)
		return
	}
	forgetOldRuns(db, name, "auto-update")
	out := &cronOutput{save: func(output string) { saveActivity(db, id, output) }}
	w := io.MultiWriter(os.Stdout, out)
	if err != nil {
		fmt.Fprintf(w, "Cannot pull %s: %v\nThe server keeps no registry login, so only a public image auto-updates.\n", image, err)
		finishActivity(db, id, false, out.buf.String())
		return
	}
	fmt.Fprintf(w, "A new %s: deploying it.\n", image)
	deploy := exec.Command(self, "deploy", name, "latest")
	deploy.Stdin = protocol.Settings{Image: image, KeepSettings: true}.Body(nil)
	deploy.Stdout, deploy.Stderr = w, w
	err = deploy.Run()
	finishActivity(db, id, err == nil, out.buf.String())
}

// isNewerImage pulls the tag and reports that its image is not the one the
// app runs. It leaves the name of the tag only when it was there before:
// another tool can run its apps from it.
func isNewerImage(name, image string) (bool, error) {
	app, err := loadApp(name)
	if err != nil {
		return false, err
	}
	_, missing := docker("image", "inspect", image)
	if out, err := docker("pull", "-q", image); err != nil {
		return false, fmt.Errorf("%s", lastLine(out))
	}
	pulled, err := docker("image", "inspect", "-f", "{{.Id}}", image)
	if err != nil {
		return false, err
	}
	running, _ := docker("image", "inspect", "-f", "{{.Id}}", app.Image)
	if pulled == running && missing != nil {
		docker("rmi", image) // only the name: the app's own tag keeps the image
	}
	return pulled != running, nil
}
