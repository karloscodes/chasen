package main

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// originImage returns the image on ghcr.io for a git origin on GitHub, or "".
// GitHub gives every repository a place in its registry under the same name.
var githubOrigin = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

func originImage(origin string) string {
	m := githubOrigin.FindStringSubmatch(strings.TrimSpace(origin))
	if m == nil {
		return ""
	}
	return strings.ToLower("ghcr.io/" + m[1] + "/" + m[2])
}

// registryLogin returns the login of the registry of the app, or nil for an
// image that needs none. The first one that exists wins:
//
//  1. `registry:` in chasen.yml.
//  2. For ghcr.io: GHCR_TOKEN or GITHUB_TOKEN. This is the way of CI.
//  3. The login of Docker for this registry (`docker login`).
//  4. For ghcr.io: the login of the gh CLI.
func registryLogin(app appFile) (*protocol.Registry, error) {
	if app.Registry.Password != "" {
		secrets, err := secretValues(app, []string{app.Registry.Password})
		if err != nil {
			return nil, err
		}
		return &protocol.Registry{Username: app.Registry.Username, Password: secrets[app.Registry.Password]}, nil
	}
	owner, github := strings.CutPrefix(app.Image, "ghcr.io/")
	owner, _, _ = strings.Cut(owner, "/")
	owner = cmp.Or(app.Registry.Username, owner)
	if token := cmp.Or(os.Getenv("GHCR_TOKEN"), os.Getenv("GITHUB_TOKEN")); github && token != "" {
		return &protocol.Registry{Username: owner, Password: token}, nil
	}
	if login := dockerLogin(protocol.RegistryHost(app.Image)); login != nil {
		return login, nil
	}
	if !github {
		return nil, nil
	}
	if out, _ := exec.Command("gh", "auth", "token").Output(); len(bytes.TrimSpace(out)) > 0 {
		return &protocol.Registry{Username: owner, Password: string(bytes.TrimSpace(out))}, nil
	}
	return nil, errors.New(`no login for ghcr.io. Do one of these:
  docker login ghcr.io      with your GitHub name and a token that has the write:packages scope
  gh auth login -s write:packages
  set GHCR_TOKEN            in CI: the token of the job`)
}

// dockerLogin returns what `docker login` saved for a registry, or nil. Docker
// keeps a login in its config file, or in the credential helper that the
// config file names (the keychain on a Mac).
func dockerLogin(host string) *protocol.Registry {
	if host == "" {
		host = "https://index.docker.io/v1/" // the name Docker uses for Docker Hub
	}
	dir := os.Getenv("DOCKER_CONFIG")
	if home, err := os.UserHomeDir(); dir == "" && err == nil {
		dir = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil
	}
	if helper := cmp.Or(config.CredHelpers[host], config.CredsStore); helper != "" {
		get := exec.Command("docker-credential-"+helper, "get")
		get.Stdin = strings.NewReader(host)
		var saved struct{ Username, Secret string }
		if out, err := get.Output(); err == nil && json.Unmarshal(out, &saved) == nil && saved.Secret != "" {
			return &protocol.Registry{Username: saved.Username, Password: saved.Secret}
		}
	}
	plain, err := base64.StdEncoding.DecodeString(config.Auths[host].Auth)
	if username, password, found := strings.Cut(string(plain), ":"); err == nil && found {
		return &protocol.Registry{Username: username, Password: password}
	}
	return nil
}
