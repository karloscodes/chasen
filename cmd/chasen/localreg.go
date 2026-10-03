package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// The default way of an image to a server you reach through SSH: no registry
// on the internet, and no token. A registry runs on this computer, in Docker,
// and listens on its loopback only. The deploy pushes the image there, opens
// a port on the loopback of the server that leads back to it through SSH, and
// the server pulls the image through that port. The server keeps nothing of
// it: the port closes when the deploy ends.
//
// The registry keeps the images it got, so the next deploy sends only the
// layers that changed.

// localRegistry is where the registry of this computer listens.
const (
	localRegistry          = "127.0.0.1:5555"
	localRegistryContainer = "chasen-registry"
)

// isLocalImage reports an image of the local registry.
func isLocalImage(image string) bool { return strings.HasPrefix(image, localRegistry+"/") }

// startLocalRegistry starts the registry of this computer, when it does not
// run yet.
func startLocalRegistry() error {
	if out, _ := exec.Command("docker", "ps", "-q", "--filter", "name=^"+localRegistryContainer+"$").Output(); len(bytes.TrimSpace(out)) > 0 {
		return nil
	}
	if exec.Command("docker", "start", localRegistryContainer).Run() != nil {
		fmt.Println("Starting the registry of chasen on this computer. The image goes from here to the server, through SSH.")
		out, err := exec.Command("docker", "run", "-d", "--name", localRegistryContainer, "--restart", "unless-stopped",
			"-p", localRegistry+":5000", "-v", localRegistryContainer+":/var/lib/registry", "registry:2").CombinedOutput()
		if err != nil {
			return fmt.Errorf("cannot start the registry of chasen on this computer: %s", lastLineOf(string(out)))
		}
	}
	// It answers in a moment. With Docker on another machine it never
	// answers here, and the push says so.
	for range 50 {
		if resp, err := http.Get("http://" + localRegistry + "/v2/"); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// registryTunnel opens a port on the loopback of the server that leads to
// the local registry, and returns the port and the way to close it. A port
// that is taken on the server gets another try.
func registryTunnel(address string) (int, func(), error) {
	server, err := url.Parse(address)
	if err != nil || server.Hostname() == "" {
		return 0, nil, fmt.Errorf("invalid SSH address %q", address)
	}
	target := server.Hostname()
	if user := server.User.Username(); user != "" {
		target = user + "@" + target
	}
	for range 5 {
		port := 20000 + rand.IntN(20000)
		args := []string{"-T", "-o", "ExitOnForwardFailure=yes", "-R", fmt.Sprintf("127.0.0.1:%d:%s", port, localRegistry)}
		if p := server.Port(); p != "" {
			args = append(args, "-p", p)
		}
		// The command runs once the port is open: its first line says so.
		ssh := exec.Command("ssh", append(args, target, "sh -c 'echo open; exec cat'")...)
		input, _ := ssh.StdinPipe()
		output, _ := ssh.StdoutPipe()
		var said bytes.Buffer
		ssh.Stderr = &said
		if err := ssh.Start(); err != nil {
			return 0, nil, fmt.Errorf("cannot run ssh: %w", err)
		}
		if line, _ := bufio.NewReader(output).ReadString('\n'); strings.TrimSpace(line) == "open" {
			return port, func() {
				input.Close()
				ssh.Process.Kill()
				ssh.Wait()
			}, nil
		}
		ssh.Wait()
		if !strings.Contains(said.String(), "port forwarding failed") {
			return 0, nil, errors.New("cannot open the way from the server to the image on this computer: " + lastLineOf(said.String()) +
				". The SSH server must allow it (AllowTcpForwarding yes, the default), or name a registry: image: in chasen.yml")
		}
	}
	return 0, nil, errors.New("cannot open a port on the server for the image: five ports were taken")
}
