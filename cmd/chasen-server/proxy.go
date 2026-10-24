package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

// The proxy is kamal-proxy, in the container matcha-proxy, on ports 80 and
// 443, in front of every app. A release of Chasen names the version it runs,
// so every server runs the same proxy, and only a release changes it. Bump
// proxyImage by hand after reading the changes of kamal-proxy, then run the
// end-to-end test.
//
// The nightly update replaces a proxy of another version, at the quiet hour
// of the server. The swap takes a few seconds. During it, a new connection
// gets no answer at all, not a refusal, so the system of the visitor sends it
// again a second later and reaches the new proxy: a pause, not an error. A
// request that runs during the swap, like an upload or a websocket, is cut.
// The routes and the certificates stay: they are on the disk of the server.

const (
	proxyImage     = "basecamp/kamal-proxy:v0.10.2"
	proxyContainer = "matcha-proxy"
)

// serverProxy says which proxy runs, or replaces it with `proxy update`.
func serverProxy(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "update":
		if err := lock(); err != nil {
			return err
		}
		return updateProxy()
	case len(args) > 0:
		return errors.New("usage: chasen-server proxy [update]")
	}
	running, err := docker("inspect", "-f", "{{.Config.Image}}", proxyContainer)
	if err != nil {
		return errors.New("the proxy does not run. chasen-server setup starts it")
	}
	fmt.Println("The proxy runs " + running + ".")
	if running != proxyImage && !runsImage(proxyImage) {
		fmt.Println("This release of Chasen runs " + proxyImage + ". chasen-server proxy update starts it: new visitors wait a few seconds.")
	}
	return nil
}

// updateProxy starts the proxy of this release when another one runs. A
// proxy from before the version was pinned runs basecamp/kamal-proxy:latest,
// and that can be the same image: then nothing changes.
func updateProxy() error {
	running, err := docker("inspect", "-f", "{{.Config.Image}}", proxyContainer)
	if err != nil {
		return errors.New("the proxy does not run. chasen-server setup starts it")
	}
	if running == proxyImage {
		fmt.Println("The proxy runs " + proxyImage + ", the version of this release.")
		return nil
	}
	if out, err := docker("pull", proxyImage); err != nil {
		return fmt.Errorf("cannot pull %s, the proxy stays as it is: %s", proxyImage, lastLine(out))
	}
	if runsImage(proxyImage) {
		fmt.Println("The proxy runs the image of " + proxyImage + ", by the name " + running + ".")
		return nil
	}
	return replaceProxy(running, proxyImage)
}

// runsImage reports that the proxy container runs the image of a name, by
// its id.
func runsImage(name string) bool {
	var container struct{ Image string }
	var image struct{ Id string }
	return dockerAPI("GET", "/containers/"+proxyContainer+"/json", nil, &container) == nil &&
		dockerAPI("GET", "/images/"+name+"/json", nil, &image) == nil && container.Image == image.Id
}

// replaceProxy swaps the proxy for one of another image. It pulls the image
// first, while the old proxy still serves. When the new one does not take
// every route back, the old one starts again.
func replaceProxy(old, image string) error {
	fmt.Println("Pulling " + image)
	if out, err := docker("pull", image); err != nil {
		return fmt.Errorf("cannot pull %s, the proxy stays as it is: %s", image, lastLine(out))
	}
	routes, _ := proxyRoutes()
	release := holdNewConnections()
	defer release()

	started := time.Now()
	fmt.Println("Starting the proxy " + image + " in place of " + old)
	docker("stop", "-t", "10", proxyContainer) // the requests that run get 10 seconds to end
	docker("rm", "-f", proxyContainer)
	err := startProxy(image)
	if err == nil {
		err = waitForRoutes(routes)
	}
	if err != nil {
		docker("rm", "-f", proxyContainer)
		if back := startProxy(old); back != nil {
			return fmt.Errorf("the new proxy did not start (%v), and the proxy of before did not start again (%v). Run: chasen-server setup", err, back)
		}
		waitForRoutes(routes)
		return fmt.Errorf("the new proxy did not start: %v. The proxy of before runs again", err)
	}
	fmt.Printf("The proxy runs %s. New visitors waited %s.\n", image, time.Since(started).Round(100*time.Millisecond))
	return nil
}

// startProxy runs the proxy container with the flags that matcha gives the
// first one (deployProxy), and the same directory for its routes and
// certificates.
func startProxy(image string) error {
	out, err := docker("run", "-d", "--name", proxyContainer, "--network", "matcha-network",
		"-p", "80:80", "-p", "443:443", "-p", "443:443/udp",
		"-v", root()+"/var/matcha/proxy:/home/kamal-proxy/.config/kamal-proxy",
		"--memory=128m", "--restart", "unless-stopped",
		"--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		image)
	if err != nil {
		return errors.New(lastLine(out))
	}
	return nil
}

// proxyRoutes returns the services that the proxy routes to, by name.
func proxyRoutes() ([]string, error) {
	out, err := docker("exec", proxyContainer, "kamal-proxy", "list")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n")[1:] { // the first line is the header
		if fields := strings.Fields(line); len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names, nil
}

// waitForRoutes waits until the proxy answers with every route it had.
// kamal-proxy reads them back from its directory when it starts.
func waitForRoutes(want []string) error {
	for range 100 {
		if got, err := proxyRoutes(); err == nil && !slices.ContainsFunc(want, func(name string) bool { return !slices.Contains(got, name) }) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("it did not answer with all its routes in 20 seconds")
}

// holdRules drop the first packet of each new connection to the proxy, in
// the raw table, before Docker sends it on: from other machines
// (PREROUTING), and from this one (OUTPUT). Only connections to this server:
// its own connections to port 443 elsewhere keep going, and so do the
// connections that are open. The comment names the rules, so a later run
// can find them.
var holdRules = func() (rules [][]string) {
	mine := []string{"-m", "addrtype", "--dst-type", "LOCAL", "-m", "comment", "--comment", "chasen proxy swap", "-j", "DROP"}
	for _, chain := range []string{"PREROUTING", "OUTPUT"} {
		rules = append(rules,
			append([]string{chain, "-p", "tcp", "--syn", "-m", "multiport", "--dports", "80,443"}, mine...),
			append([]string{chain, "-p", "udp", "--dport", "443"}, mine...))
	}
	return rules
}()

// holdNewConnections puts the hold rules in place and returns the function
// that takes them away. It takes them away on a signal too: rules left in
// place would keep every visitor out. Without iptables the swap still works,
// and a visitor in those seconds gets an error.
func holdNewConnections() (release func()) {
	releaseHold() // the rules of a run that did not end
	held := false
	for _, tool := range []string{"iptables", "ip6tables"} {
		for _, rule := range holdRules {
			if exec.Command(tool, append([]string{"-w", "-t", "raw", "-I"}, rule...)...).Run() == nil {
				held = true
			}
		}
	}
	if !held {
		fmt.Println("This server has no iptables: a visitor who connects in the next seconds gets an error.")
		return func() {}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-stop:
			releaseHold()
			os.Exit(1)
		case <-done:
		}
	}()
	return func() {
		releaseHold()
		signal.Stop(stop)
		close(done)
	}
}

// releaseHold takes every hold rule away, as many times as it is there.
func releaseHold() {
	for _, tool := range []string{"iptables", "ip6tables"} {
		for _, rule := range holdRules {
			for range 10 {
				if exec.Command(tool, append([]string{"-w", "-t", "raw", "-D"}, rule...)...).Run() != nil {
					break
				}
			}
		}
	}
}

// proxyAlerts reports a proxy that does not run while apps need it, and a
// proxy of another version than this release.
func proxyAlerts() []alert {
	var proxy struct {
		Config struct{ Image string }
		State  struct{ Running bool }
	}
	if err := dockerAPI("GET", "/containers/"+proxyContainer+"/json", nil, &proxy); err != nil || !proxy.State.Running {
		public := 0 // a private app does not need the proxy
		apps, _ := listApps()
		for _, app := range apps {
			if app.Listen == "" {
				public++
			}
		}
		if public == 0 {
			return nil
		}
		return []alert{{"proxy", true, "the proxy does not run: no app answers on the web",
			"chasen-server setup starts it again. docker logs matcha-proxy says why it stopped."}}
	}
	if proxy.Config.Image != proxyImage && !runsImage(proxyImage) {
		return []alert{{"proxy", false, "the proxy runs " + proxy.Config.Image + ", not " + proxyImage + ", the version of this release",
			"The nightly update replaces it at the quiet hour of the server. Now: chasen-server proxy update. New visitors wait a few seconds."}}
	}
	return nil
}
