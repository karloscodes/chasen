package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// deploy deploys or checks the current git commit of the app in this
// directory. An app is an image in a registry: chasen builds the image of the
// commit, pushes it, and the server pulls it. A website is an index.html with
// no Dockerfile: its files go to the server.
func deploy(creds credentials, app appFile, command string) error {
	// The review comes first: before a secret is read, and before anything is
	// built or sent.
	_, noDockerfile := os.Stat("Dockerfile")
	if err := printReview(os.Stderr, reviewAppFile(app, noDockerfile == nil, tagFlag)); err != nil {
		return err
	}
	if command == "deploy" {
		// A new app gets its secret key now, in the secrets of this directory.
		if err := keepSecretKey(func() bool { return isDeployed(creds, app.Name) }); err != nil {
			return err
		}
	}
	settings, err := appSettings(app)
	if err != nil {
		return err
	}
	settings.Domain = domainFlag
	settings.AutoUpdate = autoUpdateFlag
	// A directory with a Dockerfile and no tag builds its image: there is
	// nothing for the server to pull at night.
	if _, tag := splitImage(app.Image); autoUpdates(settings) && noDockerfile == nil && cmp.Or(tagFlag, tag) == "" {
		return errors.New(noAutoUpdate)
	}
	// The values of the secrets are here now: check the ones that have a rule.
	if err := settings.Check(); err != nil {
		return fmt.Errorf("the settings of the deploy: %w", err)
	}

	// An app with a Dockerfile and no image: to a server through SSH, the
	// image goes from this computer, with no registry on the internet. To a
	// server on the web (CI with a token), it goes to ghcr.io of
	// the GitHub origin of the app.
	if app.Image == "" && noDockerfile == nil {
		if protocol.IsSSH(creds.URL) {
			app.Image = localRegistry + "/" + app.Name
		} else {
			origin, _ := exec.Command("git", "remote", "get-url", "origin").Output()
			if app.Image = originImage(string(origin)); app.Image == "" {
				return errors.New(noImage)
			}
			fmt.Printf("No image in chasen.yml: chasen uses %s, from the git origin.\n", app.Image)
		}
	}
	if app.Image == "" {
		return deployWebsite(creds, app, settings, command)
	}
	return deployImage(creds, app, settings, command)
}

// appSettings returns the settings of chasen.yml with the values of the
// secrets. It runs secrets_command, so call it one time for each command.
func appSettings(app appFile) (protocol.Settings, error) {
	secrets, err := secretValues(app, app.Secrets)
	if err != nil {
		return protocol.Settings{}, err
	}
	env := maps.Clone(app.Env)
	if env == nil {
		env = map[string]string{}
	}
	// Every secret of chasen.secrets.enc is a secret of the app: no list to keep.
	stored, err := storedSecrets()
	if err != nil {
		return protocol.Settings{}, err
	}
	maps.Copy(env, stored)
	for _, name := range app.Secrets {
		env[name] = secrets[name]
	}
	if err := railsMasterKey(env); err != nil {
		return protocol.Settings{}, err
	}
	return protocol.Settings{Env: env, Port: app.Port, Health: app.Health, HealthTimeout: app.HealthTimeout, Volumes: app.Volumes, Memory: app.Memory,
		NoBackup: app.Backup != nil && !*app.Backup, Cron: app.Cron, Jobs: app.Jobs}, nil
}

// railsMasterKey gives a Rails app the key of its credentials. The key on
// this computer travels like a secret, so the app needs no setup for it: the
// key of the production credentials when the app has them, else
// config/master.key. A key in the secrets or in env: wins. With credentials
// and no key, the app fails at start, so the review warns.
func railsMasterKey(env map[string]string) error {
	if _, ok := env["RAILS_MASTER_KEY"]; ok {
		return nil
	}
	for _, path := range []string{"config/credentials/production.key", "config/master.key"} {
		if key, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(key)) > 0 {
			env["RAILS_MASTER_KEY"] = string(bytes.TrimSpace(key))
			return nil
		}
	}
	for _, path := range []string{"config/credentials/production.yml.enc", "config/credentials.yml.enc"} {
		if _, err := os.Stat(path); err == nil {
			return printReview(os.Stderr, []finding{{false, "the app has Rails credentials (" + path + ") and no RAILS_MASTER_KEY",
				"Rails cannot read them without the key, and an app that needs them fails at start. Run chasen secrets edit and add RAILS_MASTER_KEY=<the key of config/master.key>.", docsSecrets}})
		}
	}
	return nil
}

// commitHash matches the full hash of a git commit.
var commitHash = regexp.MustCompile("^[0-9a-f]{40}$")

// headCommit returns the hash of the current git commit, and warns when the
// working directory has changes that the commit does not have.
func headCommit() (string, error) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", errors.New("chasen deploys the current git commit. This directory has no commit")
	}
	commit := strings.TrimSpace(string(out))
	if dirty, _ := exec.Command("git", "status", "--porcelain").Output(); len(dirty) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: chasen uses commit %s. It does not include your uncommitted changes.\n", commit[:7])
	}
	return commit, nil
}

// deployImage builds the image of the commit, pushes it, and tells the server
// to pull it. With a tag (--tag, or in chasen.yml) the image is already in
// the registry: nothing is built, and the directory needs no git commit. The
// same is true for a directory with no Dockerfile: its image is the newest
// one in the registry.
func deployImage(creds credentials, app appFile, settings protocol.Settings, command string) error {
	image, tag := splitImage(app.Image)
	app.Image, tag = image, cmp.Or(tagFlag, tag)
	build := tag == ""
	if _, err := os.Stat("Dockerfile"); build && err != nil {
		// Nothing to build here: the image comes from the release of another
		// repository. Deploy the newest one. The server names the version.
		fmt.Printf("No Dockerfile here: chasen deploys the newest %s. For one version: chasen deploy --tag <version>\n", app.Image)
		tag, build = "latest", false
	}
	// The server pulls the newest image at night. An image that this deploy
	// builds, or that comes from this computer, is in no registry it can reach.
	if autoUpdates(settings) && (build || isLocalImage(app.Image)) {
		return errors.New(noAutoUpdate)
	}
	if build {
		var err error
		if tag, err = headCommit(); err != nil {
			return err
		}
	}
	// A commit shows as its short hash.
	version := tag
	if commitHash.MatchString(tag) {
		version = tag[:7]
	}

	settings.Image = app.Image + ":" + tag
	registry, from, err := registryLogin(app)
	// An image that is not built here can be public: the server pulls it with no login.
	if err != nil && build {
		return err
	}
	settings.Registry = registry
	local := isLocalImage(app.Image)
	if local && build {
		if err := startLocalRegistry(); err != nil {
			return err
		}
	}
	if build {
		if err := buildImage(settings.Image, version, serverPlatform(creds)); err != nil {
			return err
		}
		// The image is the contract: review what it says before it leaves this computer.
		facts, err := inspectImage(settings.Image)
		if err != nil {
			return err
		}
		if err := printReview(os.Stderr, reviewImage(app, facts)); err != nil {
			return err
		}
		if err := pushImage(settings.Image, registry, from); err != nil {
			return err
		}
	}
	// The server pulls an image of this computer through a port of its own
	// loopback, which leads back here for as long as the deploy runs.
	if local {
		// By digest, not by tag: the registry of this computer has no login,
		// so another local user could put another image under the tag before
		// the server pulls it. The digest names the image that was reviewed.
		digest, err := imageDigest(app.Image, tag)
		if err != nil {
			return err
		}
		port, closeTunnel, err := registryTunnel(creds.URL)
		if err != nil {
			return err
		}
		defer closeTunnel()
		settings.Image = fmt.Sprintf("127.0.0.1:%d/%s@%s", port, app.Image[len(localRegistry)+1:], digest)
	}
	var printed strings.Builder
	if err := remote(creds, settings.Body(nil), io.MultiWriter(os.Stdout, &printed), command, app.Name, version); err != nil {
		return err
	}
	if command == "deploy" {
		checkAddresses(os.Stdout, appURLs(printed.String()))
		if local && build {
			cleanLocalImages(app.Image, tag)
		}
	}
	return nil
}

// splitImage returns the image without its tag or digest, and the tag or
// digest: ghcr.io/you/app:2.1 is ghcr.io/you/app and 2.1.
func splitImage(ref string) (image, tag string) {
	if at := strings.LastIndexAny(ref, ":@"); at > strings.LastIndex(ref, "/") {
		return ref[:at], ref[at+1:]
	}
	return ref, ""
}

// deployRelease deploys an image that another repository releases, with the
// line of its README: chasen deploy ghcr.io/acme/chat --domain
// chat.example.com. Nothing of this directory counts: no chasen.yml, no
// Dockerfile, no secrets. The app is named after the image, or -a. The same
// line again updates the app to the newest image and keeps its settings.
func deployRelease(creds credentials, ref string) error {
	image, tag := splitImage(ref)
	app := appFile{Name: appFlag, Image: image + ":" + cmp.Or(tagFlag, tag, "latest")}
	if app.Name == "" {
		app.Name = strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(path.Base(image)))
	}
	if err := printReview(os.Stderr, reviewAppFile(app, false, "")); err != nil {
		return err
	}
	settings := protocol.Settings{Env: map[string]string{}, Domain: domainFlag, KeepSettings: true, AutoUpdate: autoUpdateFlag}
	return deployImage(creds, app, settings, "deploy")
}

// isDeployed reports that the server runs the app. A server that does not
// answer counts as no.
func isDeployed(creds credentials, name string) bool {
	code, err := protocol.Client(creds).Run(context.Background(), "status", []string{name}, nil, io.Discard)
	return err == nil && code == 0
}

// deployWebsite sends the files of the current commit. The server puts them
// in a Caddy image.
func deployWebsite(creds credentials, app appFile, settings protocol.Settings, command string) error {
	if autoUpdates(settings) {
		return errors.New(noAutoUpdate)
	}
	if _, err := os.Stat("index.html"); err != nil {
		return errors.New("nothing to deploy here: no Dockerfile for an app, and no index.html for a website")
	}
	fmt.Println("No Dockerfile: chasen treats this directory as a static website.")
	commit, err := headCommit()
	if err != nil {
		return err
	}
	archive := exec.Command("git", "archive", "--format=tar.gz", "HEAD")
	archive.Stderr = os.Stderr
	files, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	if err := archive.Start(); err != nil {
		return err
	}
	var printed strings.Builder
	if err := remote(creds, settings.Body(files), io.MultiWriter(os.Stdout, &printed), command, app.Name, commit[:7]); err != nil {
		return err
	}
	if command == "deploy" {
		checkAddresses(os.Stdout, appURLs(printed.String()))
	}
	return archive.Wait()
}

// serverPlatform is the platform of the server, for the build: the server
// says its CPU type in chasen load. A server that does not say, or a server
// that does not exist yet, is amd64, like most servers.
func serverPlatform(creds credentials) string {
	var out strings.Builder
	if code, err := protocol.Client(creds).Run(context.Background(), "load", nil, nil, &out); err == nil && code == 0 {
		if m := cpuType.FindStringSubmatch(out.String()); m != nil {
			return "linux/" + m[1]
		}
	}
	return "linux/amd64"
}

var cpuType = regexp.MustCompile(`cores, (amd64|arm64)\)`)

// buildImage builds the image of the commit, where chasen runs (your
// computer, or CI). The build gets the files of the commit, not the working
// directory, so the image is what its tag says.
func buildImage(image, version, platform string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errNoDocker
	}
	build := []string{"build", "--build-arg", "APP_VERSION=" + version, "-t", image}
	// GitHub links the package of an image to the repository that the label
	// names, so the package and the repository share their access.
	if origin, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
		if m := githubOrigin.FindStringSubmatch(strings.TrimSpace(string(origin))); m != nil {
			build = append(build, "--label", "org.opencontainers.image.source=https://github.com/"+m[1]+"/"+m[2])
		}
	}
	// The image must run on the server, which can have another CPU than this
	// computer: an ARM server, or a Mac and an amd64 server.
	if os.Getenv("DOCKER_DEFAULT_PLATFORM") == "" {
		build = append(build, "--platform", platform)
		if platform != "linux/"+runtime.GOARCH {
			fmt.Printf("The server is %s and this computer is %s: the build runs under emulation, and takes longer.\n", strings.TrimPrefix(platform, "linux/"), runtime.GOARCH)
		}
	}
	// An image of this computer is named by its app and version: its address
	// in the local registry says nothing to a person.
	if isLocalImage(image) {
		fmt.Println("Building", strings.TrimPrefix(image[:strings.LastIndex(image, ":")], localRegistry+"/"), version)
	} else {
		fmt.Println("Building", image)
	}
	archive := exec.Command("git", "archive", "--format=tar", "HEAD")
	archive.Stderr = os.Stderr
	files, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	if err := archive.Start(); err != nil {
		return err
	}
	docker := exec.Command("docker", append(build, "-")...)
	docker.Stdin, docker.Stderr = files, os.Stderr
	if err := docker.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return archive.Wait()
}

// pushImage pushes the image to the registry. The login stays in a directory
// that is gone after the push. from says where the login came from.
func pushImage(image string, registry *protocol.Registry, from string) error {
	config, err := os.MkdirTemp("", "chasen-registry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(config)
	push := []string{"push", "-q", image}
	if registry != nil {
		// The directory of the login has no Docker context. Name the Docker that
		// made the build (Colima, a remote context), so the push goes to the same one.
		with := []string{"--config", config}
		if host, err := exec.Command("docker", "context", "inspect", "-f", "{{.Endpoints.docker.Host}}").Output(); err == nil && len(bytes.TrimSpace(host)) > 0 {
			with = append(with, "-H", string(bytes.TrimSpace(host)))
		}
		login := exec.Command("docker", append(slices.Clone(with), "login", "-u", registry.Username, "--password-stdin", protocol.RegistryHost(image))...)
		if protocol.RegistryHost(image) == "" {
			login.Args = login.Args[:len(login.Args)-1] // Docker Hub has no host
		}
		login.Stdin = strings.NewReader(registry.Password)
		// Docker warns that the login is saved unencrypted. It is gone in a moment: show its output only when it fails.
		if out, err := login.CombinedOutput(); err != nil {
			return fmt.Errorf("the registry refused the login of %s: %s", registry.Username, strings.TrimSpace(string(out)))
		}
		push = append(with, push...)
	}
	if !isLocalImage(image) {
		fmt.Println("Pushing", image)
	}
	var said strings.Builder
	pushing := exec.Command("docker", push...)
	pushing.Stderr = io.MultiWriter(os.Stderr, &said)
	if err := pushing.Run(); err != nil {
		if refused := strings.ToLower(said.String()); registry != nil && (strings.Contains(refused, "denied") || strings.Contains(refused, "unauthorized")) {
			return fmt.Errorf("the registry refused the push of %s, with the login of %s (%s). The login needs the right to write packages. For ghcr.io: gh auth refresh -s write:packages, or docker login ghcr.io with a classic token that has write:packages", image, registry.Username, from)
		}
		return fmt.Errorf("docker push failed: %w", err)
	}
	return nil
}

// restart sends the settings of chasen.yml to the server and starts the app
// again with them, from the image it already has: a change of configuration
// or a new secret needs no build.
func restart(creds credentials, app appFile) error {
	if err := printReview(os.Stderr, reviewAppFile(app, false, "")); err != nil {
		return err
	}
	settings, err := appSettings(app)
	if err != nil {
		return err
	}
	return remote(creds, settings.Body(nil), os.Stdout, "restart", app.Name)
}

// autoUpdates reports a deploy that turns auto-update on.
func autoUpdates(settings protocol.Settings) bool {
	return settings.AutoUpdate != nil && *settings.AutoUpdate
}

// noAutoUpdate is what a deploy says when it cannot auto-update.
const noAutoUpdate = `--auto-update needs an image in a registry: the server pulls the newest one each night. This deploy builds its image here, or sends it from this computer, so there is nothing for the server to pull.
Deploy an image that another repository releases instead: chasen deploy ghcr.io/acme/chat chat.example.com --auto-update`

// noImage is what a deploy says for an app with a Dockerfile and no image.
const noImage = `Chasen does not know where the image of this app goes. It pushes the image to a registry, and the server pulls it. It does not build on the server.

A repository with a git origin on GitHub needs no setting: the image goes to ghcr.io/<owner>/<repository>. For every other case:

1. Add to chasen.yml:

  image: ghcr.io/<you>/<app>
  registry:              # only for a private image
    username: <you>
    password: GHCR_TOKEN # the name of a secret

2. Run: chasen deploy
   It builds the image of the commit here, pushes it, and deploys it.`

// secretValues returns the values of the named secrets. A secret comes from
// the output of secrets_command, then from the environment, then from the
// encrypted secrets of this directory.
func secretValues(app appFile, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	stored, err := storedSecrets()
	if err != nil {
		return nil, err
	}
	var fetched map[string]string
	if app.SecretsCommand != "" && len(names) > 0 {
		cmd := exec.Command("sh", "-c", app.SecretsCommand)
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("secrets_command failed: %w", err)
		}
		fetched = parseDotenv(string(out))
	}

	values := map[string]string{}
	var missing []string
	for _, name := range names {
		value, ok := fetched[name]
		if !ok {
			value, ok = os.LookupEnv(name)
		}
		if !ok {
			value, ok = stored[name]
		}
		if !ok {
			missing = append(missing, name)
		}
		values[name] = value
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing secrets: %s. Add them with: chasen secrets edit", strings.Join(missing, ", "))
	}
	return values, nil
}

// parseDotenv reads KEY=VALUE lines. It accepts an `export ` prefix and quotes.
// ponytail: one line per value. Multi-line values need a real dotenv parser.
var dotenvEscapes = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n")

func parseDotenv(s string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			double := value[0] == '"'
			value = value[1 : len(value)-1]
			// A tool that prints "..." escapes a quote and a backslash inside it.
			if double {
				value = dotenvEscapes.Replace(value)
			}
		}
		env[strings.TrimSpace(key)] = value
	}
	return env
}
