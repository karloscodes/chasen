package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/karloscodes/chasen/protocol"
)

// The jobs container of an app: `jobs:` in chasen.yml, a command like
// bin/jobs. It is the job role of Kamal: the image of the app with another
// command, the same env, volumes, and memory, and no proxy.
//
// The web container and the jobs container deploy as one unit. The web swap
// comes first, as in Kamal, so the new version runs its migrations before its
// jobs start. The old jobs container keeps working meanwhile. Then the new
// jobs container starts, and it must run for jobsStartup. When it does not,
// the old version of the web comes back (apply), and the old jobs container
// was never stopped: both are old again. When it does, the old jobs container
// stops: SIGTERM, then 30 seconds to finish its job.

const jobsStartup = 10 * time.Second

// jobsName is the name of the jobs containers of an app. An app name has no
// "_", so no app has these names.
func jobsName(app string) string { return app + "_jobs" }

// runningJobs returns the jobs container of an app that runs now, or "".
func runningJobs(app string) string {
	all, _ := containers()
	for _, name := range []string{jobsName(app), jobsName(app) + "-next"} {
		if all[name].State == "running" {
			return name
		}
	}
	return ""
}

// swapJobs gives an app the jobs container of the version that its web
// container runs now, or stops it when the app has no jobs any more.
func swapJobs(app, command string) error {
	old := runningJobs(app)
	if command == "" {
		if old != "" {
			fmt.Println("Stopping the jobs of", app)
			stopContainer(old)
		}
		return nil
	}
	words, err := protocol.CommandWords(command)
	if err != nil {
		return err
	}
	web, err := activeContainer(app)
	if err != nil {
		return err
	}
	next := jobsName(app)
	if old == next {
		next += "-next"
	}
	fmt.Printf("Starting the jobs of %s: %s\n", app, command)
	if err := startJobs(next, web, words); err != nil {
		stopContainer(next)
		return err
	}
	if old != "" {
		stopContainer(old)
	}
	return nil
}

// startJobs starts the container name like the container web, with the
// command words, and waits until it has run for jobsStartup.
func startJobs(name, web string, words []string) error {
	var like struct {
		Config struct {
			Image string
			Env   []string
		}
		HostConfig struct {
			Binds       []string
			Memory      int64
			NetworkMode string
			LogConfig   map[string]any
		}
	}
	if err := dockerAPI("GET", "/containers/"+web+"/json", nil, &like); err != nil {
		return err
	}
	// A container of a deploy that failed can have the name still.
	dockerAPI("DELETE", "/containers/"+name+"?force=true", nil, nil)
	create := map[string]any{
		"Image":       like.Config.Image,
		"Env":         like.Config.Env,
		"Cmd":         words,
		"StopTimeout": 30,
		"HostConfig": map[string]any{
			"Binds":         like.HostConfig.Binds,
			"Memory":        like.HostConfig.Memory,
			"NetworkMode":   like.HostConfig.NetworkMode,
			"LogConfig":     like.HostConfig.LogConfig,
			"RestartPolicy": map[string]string{"Name": "unless-stopped"},
			"CapDrop":       []string{"NET_RAW"}, // like the web container: no spoofing on the shared network
		},
	}
	if err := dockerAPI("POST", "/containers/create?name="+name, create, nil); err != nil {
		return err
	}
	if err := dockerAPI("POST", "/containers/"+name+"/start", nil, nil); err != nil {
		return err
	}
	time.Sleep(jobsStartup)
	var now struct {
		State struct {
			Running   bool
			ExitCode  int
			OOMKilled bool
		}
		RestartCount int
	}
	if err := dockerAPI("GET", "/containers/"+name+"/json", nil, &now); err != nil {
		return err
	}
	if now.State.Running && now.RestartCount == 0 {
		return nil
	}
	logs, _ := docker("logs", "--tail", "20", name)
	why := fmt.Sprintf("it stopped with exit code %d", now.State.ExitCode)
	if now.State.OOMKilled {
		why = "it used more than its memory"
	}
	return fmt.Errorf("the jobs container did not keep running for %s: %s. Its last lines:\n%s", jobsStartup, why, indentLines(logs))
}

// stopContainer stops a container with its own stop timeout, and removes it.
func stopContainer(name string) {
	dockerAPI("POST", "/containers/"+name+"/stop", nil, nil)
	dockerAPI("DELETE", "/containers/"+name+"?force=true", nil, nil)
}

func indentLines(text string) string {
	if strings.TrimSpace(text) == "" {
		return "  (no output)"
	}
	return "  " + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n  ")
}
