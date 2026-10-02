package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/karloscodes/matcha"
)

// engineEnv is the env that the engine adds to every container at a deploy,
// with the name of the app in front: SHOP_PRIVATE_KEY, SHOP_DOMAIN,
// SHOP_APP_PORT, and SHOP_ENV. An app made for matcha reads them. The check
// gives them too: an app must not pass a deploy and fail the check.
func engineEnv(name, domain string, port int, privateKey string) map[string]string {
	prefix := strings.ToUpper(name)
	return map[string]string{
		prefix + "_PRIVATE_KEY": privateKey,
		prefix + "_DOMAIN":      domain,
		prefix + "_APP_PORT":    strconv.Itoa(port),
		prefix + "_ENV":         "production",
	}
}

// serverCheck tests one commit against the standard. It gets the image and
// runs it next to the live app, with no traffic and an empty storage, and
// reports each rule. It changes nothing of the live app.
func serverCheck(name, version string) error {
	if !versionRe.MatchString(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	// The settings come with the check. Nothing of them is kept.
	in := bufio.NewReader(os.Stdin)
	settings, _, err := readSettings(in)
	if err != nil {
		return err
	}

	image := imageRepo(name) + ":check-" + version
	if err := fetchImage(settings, in, image); err != nil {
		return err
	}
	defer docker("rmi", "-f", image)

	fmt.Printf("Checking %s %s against the standard\n", name, version)
	failed := 0
	report := func(ok bool, format string, args ...any) bool {
		mark := "  ok    "
		if !ok {
			mark = "  FAIL  "
			failed++
		}
		fmt.Println(mark + fmt.Sprintf(format, args...))
		return ok
	}
	verdict := func() error {
		if failed > 0 {
			return fmt.Errorf("%d check(s) failed. The live app did not change", failed)
		}
		fmt.Println("All checks passed.")
		return nil
	}

	sh, err := appShape(image, settings)
	if !report(err == nil, "the port is %d (%s)%s", sh.Port, sh.PortFrom, errText(err)) {
		return verdict()
	}

	// An empty storage that belongs to the user of the image, like on a first deploy.
	scratch := ".check-" + name
	container := "chasen-check-" + name
	docker("rm", "-f", container)
	os.RemoveAll(appDir(scratch))
	if err := prepareVolumes(scratch, image, sh.Volumes); err != nil {
		return err
	}
	defer os.RemoveAll(appDir(scratch))
	defer docker("rm", "-f", container)

	privateKey, err := matcha.GeneratePrivateKey()
	if err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", container, "--network", "matcha-network", "--memory=512m"}
	for _, v := range sh.Volumes {
		args = append(args, "-v", filepath.Join(appDir(scratch), path.Base(v))+":"+v)
	}
	// `-e NAME` takes the value from the environment of the docker command, so
	// a secret is not in its arguments, where every user of the server sees it.
	var env []string
	checked := appEnv(settings, []string{name + ".check"}, version, privateKey, sh)
	for k, v := range engineEnv(name, name+".check", sh.Port, privateKey) {
		checked[k] = v
	}
	for k, v := range checked {
		args = append(args, "-e", k)
		env = append(env, k+"="+v)
	}
	run := exec.Command("docker", append(args, image)...)
	run.Env = append(os.Environ(), env...)
	if out, err := run.CombinedOutput(); err != nil {
		report(false, "the container starts: %s", strings.TrimSpace(string(out)))
		return verdict()
	}

	// Rule 3 and 4: the health path answers 200, with no redirect, in time.
	elapsed, problem := waitHealthy(container, sh)
	if problem == "" {
		report(true, "GET %s returned 200 after %s", sh.Health, elapsed.Round(time.Second))
	} else {
		report(false, "GET %s did not return 200: %s", sh.Health, problem)
		logs, _ := docker("logs", "--tail", "15", container)
		fmt.Println("  The last lines of the app:\n    " + strings.ReplaceAll(logs, "\n", "\n    "))
		return verdict()
	}

	// Rule 5: the user of the image can write to the storage.
	probe := "for d in " + strings.Join(sh.Volumes, " ") + `; do touch "$d/.chasen-check" && rm "$d/.chasen-check" || exit 1; done`
	if out, err := docker("exec", container, "sh", "-c", probe); err != nil && strings.Contains(out, "executable file not found") {
		fmt.Println("  skip  the storage is writable (the image has no shell to test it)")
	} else {
		report(err == nil, "the storage %s is writable%s", sh.Volumes[0], suffix(out))
	}

	// Rule 6: a database outside the storage is lost at the next deploy.
	// `docker diff` lists what the container wrote outside its volumes.
	diff, _ := docker("diff", container)
	var stray []string
	for _, line := range strings.Split(diff, "\n") {
		file := strings.TrimSpace(strings.TrimLeft(line, "ACD"))
		for _, ext := range []string{".sqlite3", ".sqlite", ".db", ".sqlite3-wal", ".db-wal"} {
			if strings.HasSuffix(file, ext) && !strings.HasPrefix(line, "D") {
				stray = append(stray, file)
			}
		}
	}
	report(len(stray) == 0, "no database outside the storage%s", suffix(strings.Join(stray, ", ")))

	// Rule 7: a restart runs the migrations again. They must not fail the second time.
	docker("restart", "-t", "10", container)
	if elapsed, problem = waitHealthy(container, sh); problem == "" {
		report(true, "a restart is safe: healthy again after %s", elapsed.Round(time.Second))
	} else {
		report(false, "a restart is not safe: %s", problem)
	}

	// Rule 10: the app stops on SIGTERM. Docker kills it after 10 seconds.
	start := time.Now()
	docker("stop", "-t", "10", container)
	stopped := time.Since(start)
	report(stopped < 10*time.Second, "the app stops on SIGTERM (%s)", stopped.Round(100*time.Millisecond))

	return verdict()
}

// waitHealthy asks the health path once a second until it returns 200, the
// container stops, or the timeout of the app passes. It returns the problem,
// or "" for a healthy app.
func waitHealthy(container string, sh shape) (time.Duration, string) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		// A health path must not redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	last := "no answer"
	for time.Since(start) < time.Duration(sh.HealthTimeout)*time.Second {
		state, _ := docker("inspect", "-f", "{{.State.Running}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container)
		running, ip, _ := strings.Cut(state, " ")
		if running != "true" {
			return time.Since(start), "the container stopped"
		}
		if resp, err := client.Get(fmt.Sprintf("http://%s:%d%s", ip, sh.Port, sh.Health)); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(start), ""
			}
			last = "it answered " + resp.Status
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				last += ": a health path must not redirect"
			}
		}
		time.Sleep(time.Second)
	}
	return time.Since(start), fmt.Sprintf("%s on port %d in %d seconds", last, sh.Port, sh.HealthTimeout)
}

func suffix(problem string) string {
	if problem = strings.TrimSpace(problem); problem == "" {
		return ""
	}
	return ": " + problem
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
