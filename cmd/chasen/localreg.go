package main

import (
	"bufio"
	"bytes"
	"encoding/json"
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
// The registry keeps the images of the last deploys of each app, so the next
// deploy sends only the layers that changed, and `--tag` can send an older
// version again. After each deploy, it and the Docker of this computer forget
// the older ones (cleanLocalImages).

// localRegistry is where the registry of this computer listens.
const (
	localRegistry          = "127.0.0.1:5555"
	localRegistryContainer = "chasen-registry"
)

// isLocalImage reports an image of the local registry.
func isLocalImage(image string) bool { return strings.HasPrefix(image, localRegistry+"/") }

// deleteEnabled lets the registry delete an image, for the cleanup.
const deleteEnabled = "REGISTRY_STORAGE_DELETE_ENABLED=true"

// startLocalRegistry starts the registry of this computer, when it does not
// run yet. A registry from before the cleanup cannot delete: it is made
// again, and its volume keeps the images.
func startLocalRegistry() error {
	env, _ := exec.Command("docker", "inspect", "-f", "{{.Config.Env}}", localRegistryContainer).Output()
	if len(env) > 0 && !bytes.Contains(env, []byte(deleteEnabled)) {
		exec.Command("docker", "rm", "-f", localRegistryContainer).Run()
	}
	if out, _ := exec.Command("docker", "ps", "-q", "--filter", "name=^"+localRegistryContainer+"$").Output(); len(bytes.TrimSpace(out)) > 0 {
		return nil
	}
	if exec.Command("docker", "start", localRegistryContainer).Run() != nil {
		fmt.Println("Starting the registry of chasen on this computer. The image goes from here to the server, through SSH.")
		out, err := exec.Command("docker", "run", "-d", "--name", localRegistryContainer, "--restart", "unless-stopped",
			"-e", deleteEnabled, "-p", localRegistry+":5000", "-v", localRegistryContainer+":/var/lib/registry", "registry:2").CombinedOutput()
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

// keptLocalImages is how many images of an app this computer keeps, like the
// server.
const keptLocalImages = 5

// cleanLocalImages forgets the older images of an app after a deploy: in the
// Docker of this computer, then in its registry, and then the registry frees
// the layers that no image uses. It keeps the image of this deploy, the
// newest others, and an image that a kept one shares. A failure changes
// nothing that a deploy needs, so it says nothing.
func cleanLocalImages(repo, deployed string) {
	out, err := exec.Command("docker", "images", repo, "--format", "{{.Tag}} {{.ID}}").Output()
	if err != nil {
		return
	}
	type image struct{ tag, id string }
	var images []image
	keep, keptIDs := map[string]bool{deployed: true}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tag, id, _ := strings.Cut(line, " "); tag != "" && tag != "<none>" {
			images = append(images, image{tag, id})
			if tag == deployed {
				keptIDs[id] = true
			}
		}
	}
	// Docker lists the newest first: an image built from the cache keeps the
	// age of the first build, so the image of this deploy counts apart.
	for _, im := range images {
		if keptIDs[im.id] || (len(keptIDs) < keptLocalImages && !keep[im.tag]) {
			keep[im.tag], keptIDs[im.id] = true, true
		}
	}
	for _, im := range images {
		if !keep[im.tag] {
			exec.Command("docker", "rmi", repo+":"+im.tag).Run()
		}
	}

	name := strings.TrimPrefix(repo, localRegistry+"/")
	tags, err := registryTags(name)
	if err != nil {
		return
	}
	keptDigests := map[string]bool{}
	for tag := range keep {
		if digest, err := registryDigest(name, tag); err == nil {
			keptDigests[digest] = true
		}
	}
	deleted := false
	for _, tag := range tags {
		if keep[tag] {
			continue
		}
		digest, err := registryDigest(name, tag)
		if err != nil || keptDigests[digest] {
			continue
		}
		req, _ := http.NewRequest("DELETE", "http://"+localRegistry+"/v2/"+name+"/manifests/"+digest, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			deleted = deleted || resp.StatusCode == http.StatusAccepted
		}
	}
	// ponytail: the collection runs while the registry runs. A push from
	// another deploy of this computer at the same moment could lose a layer;
	// that deploy then fails, and runs again.
	if deleted {
		exec.Command("docker", "exec", localRegistryContainer, "registry", "garbage-collect", "--delete-untagged", "/etc/docker/registry/config.yml").Run()
	}
}

// registryTags lists the tags of an image in the local registry.
func registryTags(name string) ([]string, error) {
	resp, err := http.Get("http://" + localRegistry + "/v2/" + name + "/tags/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Tags []string `json:"tags"`
	}
	return list.Tags, json.NewDecoder(resp.Body).Decode(&list)
}

// registryDigest returns the digest of the manifest of a tag, which a delete needs.
func registryDigest(name, tag string) (string, error) {
	req, _ := http.NewRequest("HEAD", "http://"+localRegistry+"/v2/"+name+"/manifests/"+tag, nil)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	digest := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode != http.StatusOK || digest == "" {
		return "", fmt.Errorf("%s:%s: %s", name, tag, resp.Status)
	}
	return digest, nil
}
