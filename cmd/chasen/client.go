package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/karloscodes/chasen/oauth"
	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// appFile is chasen.yml in the app directory. Every field is optional.
type appFile struct {
	Name string `yaml:"name"`
	// Server names the server of this app, when you have more than one:
	// a domain like apps.example.com, or "cloud".
	Server string `yaml:"server"`
	// Image is the image of the app in a registry, without a tag:
	// ghcr.io/you/shop. The tag is the git commit, or --tag.
	Image string `yaml:"image"`
	// Registry is the login for a private image. Password is the name of a
	// secret, not its value.
	Registry struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"registry"`
	Env            map[string]string `yaml:"env"`
	Secrets        []string          `yaml:"secrets"`
	SecretsCommand string            `yaml:"secrets_command"`

	// Overrides of the standard. Without them, the port and the volumes come
	// from the image, and the health path is /up.
	Port          int      `yaml:"port"`
	Health        string   `yaml:"health"`
	HealthTimeout int      `yaml:"health_timeout"`
	Volumes       []string `yaml:"volumes"`
}

// credentials is the saved login: the API of the cloud or of a server, and the token.
type credentials struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Server is the cloud server a command goes to, when the user chose one:
	// an id, or "new", or "new:<type>". It is not saved.
	Server string `json:"-"`
}

func credentialsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "chasen", "credentials.json")
}

// logins is the file of saved logins. You can be logged in to several
// servers and to the cloud. Commands go to the current one, unless chasen.yml
// names a server.
type logins struct {
	Current string            `json:"current"`
	Tokens  map[string]string `json:"logins"` // by API address
}

func loadLogins() logins {
	saved := logins{Tokens: map[string]string{}}
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		return saved
	}
	// The first format of the file had one login: {"url": ..., "token": ...}.
	var one credentials
	if json.Unmarshal(data, &one) == nil && one.URL != "" {
		return logins{Current: one.URL, Tokens: map[string]string{one.URL: one.Token}}
	}
	json.Unmarshal(data, &saved)
	if saved.Tokens == nil {
		saved.Tokens = map[string]string{}
	}
	return saved
}

func (l logins) save() error {
	data, _ := json.MarshalIndent(l, "", "  ")
	if err := os.MkdirAll(filepath.Dir(credentialsPath()), 0700); err != nil {
		return err
	}
	return os.WriteFile(credentialsPath(), data, 0600)
}

// apiAddress turns what a person types into the address of an API: "cloud"
// is the Chasen cloud, apps.example.com is https://api.apps.example.com, and
// a full address stays as it is.
func apiAddress(name string) string {
	switch {
	case name == "cloud":
		return cmp.Or(os.Getenv("CHASEN_CLOUD"), cloudURL)
	case strings.Contains(name, "://"):
		return strings.TrimRight(name, "/")
	}
	return "https://api." + name
}

// loadCredentials picks where a command goes: CHASEN_URL and CHASEN_TOKEN
// (for CI), then the server that chasen.yml names, then the current login.
func loadCredentials(server string) (credentials, error) {
	creds := credentials{URL: os.Getenv("CHASEN_URL"), Token: os.Getenv("CHASEN_TOKEN")}
	if creds.URL != "" && creds.Token != "" {
		return creds, nil
	}
	saved := loadLogins()
	creds.URL = saved.Current
	if server != "" {
		creds.URL = apiAddress(server)
	}
	token, ok := saved.Tokens[creds.URL]
	switch {
	case ok:
		creds.Token = token
		return creds, nil
	case server != "":
		return creds, fmt.Errorf("chasen.yml names the server %s, and you are not logged in to it. Run: chasen add server %s", server, server)
	}
	return creds, errors.New("not logged in. " + howToLogin)
}

// listServers shows the saved logins. The star marks the current one.
func listServers() error {
	saved := loadLogins()
	if len(saved.Tokens) == 0 {
		return errors.New("not logged in. " + howToLogin)
	}
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	for _, address := range addresses {
		mark := " "
		if address == saved.Current {
			mark = "*"
		}
		fmt.Println(mark, address)
	}
	return nil
}

// useServer makes another saved login the current one.
func useServer(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: chasen use <server>. chasen servers lists them")
	}
	saved := loadLogins()
	address := apiAddress(args[0])
	if _, ok := saved.Tokens[address]; !ok {
		return fmt.Errorf("you are not logged in to %s. Run: chasen servers", address)
	}
	saved.Current = address
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Println("Commands now go to", address)
	return nil
}

// cloudURL is the Chasen cloud that `chasen login` uses. CHASEN_CLOUD sets
// another one.
var cloudURL = "https://cloud.chasenhq.com"

const howToLogin = "Run: chasen login. For your own server, run: chasen add server <domain>"

// login logs in to the Chasen cloud.
func login(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: chasen login. For your own server: chasen add server <domain>")
	}
	return connect(apiAddress("cloud"))
}

// addServer logs in to a server you host yourself. The server apps.example.com
// has its API on https://api.apps.example.com.
func addServer(args []string) error {
	if len(args) != 2 || args[0] != "server" {
		return errors.New("usage: chasen add server <domain>")
	}
	return connect(apiAddress(args[1]))
}

// connect logs in to the API at target, saves the login, and makes it the
// current one.
func connect(target string) error {
	server, err := url.Parse(strings.TrimRight(target, "/"))
	if err != nil || server.Host == "" {
		return fmt.Errorf("invalid address %q", target)
	}
	// The token goes in each request. Plain http is only for a server on this machine or in a test.
	host := server.Hostname()
	local := host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil
	if server.Scheme != "https" && !(server.Scheme == "http" && local) {
		return errors.New("the address must start with https://")
	}

	// CI sets CHASEN_TOKEN. A person logs in with a browser.
	creds := credentials{URL: server.String(), Token: os.Getenv("CHASEN_TOKEN")}
	if creds.Token == "" {
		if creds.Token, err = deviceLogin(creds.URL); err != nil {
			return err
		}
	}
	if err := remote(creds, nil, io.Discard, "list"); err != nil {
		return err
	}

	saved := loadLogins()
	saved.Tokens[creds.URL] = creds.Token
	saved.Current = creds.URL
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Println("Logged in to", creds.URL)
	return nil
}

// deviceLogin is the OAuth 2.0 device flow: show a page and a code to the
// user, then wait until they approve the login in a browser.
func deviceLogin(server string) (string, error) {
	var device struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		Interval   int    `json:"interval"`
	}
	resp, err := http.PostForm(server+"/oauth/device_authorization", url.Values{"client_id": {"chasen"}})
	if err != nil {
		return "", err
	}
	err = json.NewDecoder(resp.Body).Decode(&device)
	resp.Body.Close()
	if err != nil || device.DeviceCode == "" {
		return "", fmt.Errorf("%s does not answer like a Chasen server (%s)", server, resp.Status)
	}

	// The page is always on the server the user named, not on an address from the answer.
	page := server + "/oauth/device?user_code=" + device.UserCode
	fmt.Printf("Open this page to log in:\n  %s\nCode: %s\n", page, device.UserCode)
	for _, opener := range []string{"xdg-open", "open"} {
		if exec.Command(opener, page).Start() == nil {
			break
		}
	}

	for {
		time.Sleep(time.Duration(max(device.Interval, 1)) * time.Second)
		resp, err := http.PostForm(server+"/oauth/token", url.Values{
			"grant_type": {oauth.GrantType}, "device_code": {device.DeviceCode}, "client_id": {"chasen"},
		})
		if err != nil {
			return "", err
		}
		var answer struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&answer)
		resp.Body.Close()
		switch {
		case err != nil:
			return "", err
		case answer.AccessToken != "":
			return answer.AccessToken, nil
		case answer.Error != "authorization_pending":
			return "", fmt.Errorf("the login did not complete (%s). Try again", answer.Error)
		}
	}
}

// errUnauthorized means the server refused the token.
var errUnauthorized = errors.New("the server does not accept the token. " + howToLogin)

// appFlag is the app from `-a <app>`. It replaces the app of the directory,
// for an addon or for an app whose directory you are not in.
var appFlag string

// tagFlag is the image tag from `--tag <tag>`. It replaces the git commit.
var tagFlag string

// serverFlag is the cloud server from `--on <id>`, `--new`, or `--new=<type>@<location>`, in the
// form of protocol.ServerHeader.
var serverFlag string

func runClient(args []string) error {
	for i := 0; i < len(args); i++ {
		hasValue := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
		switch {
		case (args[i] == "-a" || args[i] == "--app") && hasValue:
			appFlag = args[i+1]
		case args[i] == "--on" && hasValue:
			serverFlag = args[i+1]
		case args[i] == "--tag" && hasValue:
			tagFlag = args[i+1]
		case args[i] == "--new" || strings.HasPrefix(args[i], "--new="):
			serverFlag = strings.Replace(strings.TrimPrefix(args[i], "--"), "=", ":", 1)
			args = slices.Delete(args, i, i+1)
			i--
			continue
		default:
			continue
		}
		args = slices.Delete(args, i, i+2)
		i--
	}
	if len(args) == 0 {
		return errors.New(usage)
	}

	switch args[0] {
	case "login":
		return login(args[1:])
	case "add":
		return addServer(args[1:])
	case "servers":
		return listServers()
	case "use":
		return useServer(args[1:])
	}
	// chasen.yml can name the server of the app.
	app, err := loadAppFile()
	if err != nil {
		return err
	}
	creds, err := loadCredentials(app.Server)
	if err != nil {
		return err
	}
	creds.Server = serverFlag
	err = runCommand(creds, args)
	// The saved login is not valid any more: log in again, then run the command again.
	if errors.Is(err, errUnauthorized) && os.Getenv("CHASEN_TOKEN") == "" {
		if err := connect(creds.URL); err != nil {
			return err
		}
		if creds, err = loadCredentials(app.Server); err != nil {
			return err
		}
		creds.Server = serverFlag
		return runCommand(creds, args)
	}
	return err
}

func runCommand(creds credentials, args []string) error {
	switch cmd := args[0]; cmd {
	case "list":
		return remote(creds, nil, os.Stdout, "list")
	case "deploy", "check":
		app, err := loadAppFile()
		if err != nil {
			return err
		}
		return sendCommit(creds, app, cmd)
	case "enable":
		if len(args) < 2 {
			return errors.New("usage: chasen enable fusionaly|formlander|lognorth [domain]")
		}
		creds, err := placed(creds, args[1])
		if err != nil {
			return err
		}
		return remote(creds, nil, os.Stdout, args...)
	case "restart":
		// Start the app again with the env and the secrets of chasen.yml, from the image it has.
		app, err := loadAppFile()
		if err != nil {
			return err
		}
		return restart(creds, app)
	case "logout":
		// Tell the server to forget this login, then forget it here.
		if err := remote(creds, nil, io.Discard, protocol.Logout); err != nil && !errors.Is(err, errUnauthorized) {
			return err
		}
		saved := loadLogins()
		delete(saved.Tokens, creds.URL)
		// Another login becomes the current one: the first by name.
		saved.Current = ""
		if rest := slices.Sorted(maps.Keys(saved.Tokens)); len(rest) > 0 {
			saved.Current = rest[0]
		}
		fmt.Println("Logged out of", creds.URL)
		return saved.save()
	}
	// Every other command of the protocol runs on the server, for the app of this directory.
	if !slices.Contains(protocol.Commands, args[0]) || args[0] == "env" {
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
	app, err := loadAppFile()
	if err != nil {
		return err
	}
	return remote(creds, nil, os.Stdout, append([]string{args[0], app.Name}, args[1:]...)...)
}

func loadAppFile() (appFile, error) {
	var app appFile
	data, err := os.ReadFile("chasen.yml")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return app, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&app); err != nil && err != io.EOF {
		return app, fmt.Errorf("chasen.yml: %w", err)
	}
	if appFlag != "" {
		app.Name = appFlag
	}
	if app.Name == "" {
		wd, err := os.Getwd()
		if err != nil {
			return app, err
		}
		app.Name = strings.ToLower(filepath.Base(wd))
	}
	return app, nil
}

// A static website follows the standard like an app: Caddy listens on $PORT
// and answers /up.
const (
	websiteDockerfile = "FROM caddy:2-alpine\nCOPY Caddyfile /etc/caddy/Caddyfile\nCOPY . /srv\n"
	websiteCaddyfile  = `{
	admin off
	auto_https off
}
:{$PORT} {
	root * /srv
	respond /up 200
	file_server {
		hide Dockerfile Caddyfile chasen.yml
	}
}
`
)

// sendCommit deploys or checks the current git commit. An app is an image in
// a registry: chasen builds the image of the commit, pushes it, and the
// server pulls it. With a tag (--tag, or in chasen.yml) the image is already
// in the registry, and nothing is built. A website is an index.html with no
// Dockerfile: its files go to the server as a tar archive.
func sendCommit(creds credentials, app appFile, command string) error {
	_, noDockerfile := os.Stat("Dockerfile")
	// An app on GitHub needs no chasen.yml: its image is ghcr.io/<owner>/<repository>.
	if app.Image == "" && noDockerfile == nil {
		origin, _ := exec.Command("git", "remote", "get-url", "origin").Output()
		if app.Image = originImage(string(origin)); app.Image != "" {
			fmt.Printf("No image in chasen.yml: chasen uses %s, from the git origin.\n", app.Image)
		}
	}
	website := app.Image == "" && noDockerfile != nil
	if website {
		if _, err := os.Stat("index.html"); err != nil {
			return errors.New("nothing to deploy here: no image in chasen.yml, and no index.html for a website")
		}
		fmt.Println("No Dockerfile: chasen treats this directory as a static website.")
	} else if app.Image == "" {
		return errors.New(noImage)
	}

	// The version is the git commit. With --tag, or a tag in chasen.yml, the
	// directory does not have to be a git repository.
	tag := tagFlag
	if at := strings.LastIndexAny(app.Image, ":@"); tag == "" && at > strings.LastIndex(app.Image, "/") {
		app.Image, tag = app.Image[:at], app.Image[at+1:]
	}
	version := tag
	// A tag that is a full commit hash shows as the short hash, like a deploy of that commit.
	if ok, _ := regexp.MatchString("^[0-9a-f]{40}$", tag); ok {
		version = tag[:7]
	}
	build := tag == "" && !website
	if tag == "" {
		sha, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return errors.New("chasen deploys the current git commit. This directory has no commit. Or name an image tag: chasen deploy --tag <tag>")
		}
		tag = strings.TrimSpace(string(sha))
		version = tag[:7]
		if dirty, _ := exec.Command("git", "status", "--porcelain").Output(); len(dirty) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: chasen uses commit %s. It does not include your uncommitted changes.\n", version)
		}
	}
	if !website {
		app.Image += ":" + tag
		if build {
			if err := buildAndPush(app, version); err != nil {
				return err
			}
		}
	}

	// A check keeps its settings apart, so it changes nothing of the live app.
	slot := []string{"env", app.Name}
	if command == "check" {
		slot = append(slot, "check")
	}
	var err error
	if command == "deploy" {
		if creds, err = placed(creds, app.Name); err != nil {
			return err
		}
	}
	if err := sendSettings(creds, app, slot); err != nil {
		return err
	}
	if command == "deploy" {
		creds.Server = "" // the settings put the app on its server. The cloud knows it now
	}
	if !website {
		return remote(creds, nil, os.Stdout, command, app.Name, version)
	}

	archive := exec.Command("git", "archive", "--format=tar.gz",
		"--add-virtual-file=Dockerfile:"+websiteDockerfile, "--add-virtual-file=Caddyfile:"+websiteCaddyfile, "HEAD")
	archive.Stderr = os.Stderr
	files, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	if err := archive.Start(); err != nil {
		return err
	}
	if err := remote(creds, files, os.Stdout, command, app.Name, version); err != nil {
		return err
	}
	return archive.Wait()
}

// buildAndPush builds the image of the commit, where chasen runs (your
// computer, or CI), and pushes it to the registry with the login of
// chasen.yml. The build gets the files of the commit, not the working
// directory, so the image is what its tag says. The login stays in a
// directory that is gone after the push.
func buildAndPush(app appFile, version string) error {
	build := []string{"build", "--build-arg", "APP_VERSION=" + version, "-t", app.Image}
	// The image must run on the server. Most servers are amd64, and a Mac is not.
	if os.Getenv("DOCKER_DEFAULT_PLATFORM") == "" {
		build = append(build, "--platform", "linux/amd64")
	}
	fmt.Println("Building", app.Image)
	archive := exec.Command("git", "archive", "--format=tar", "HEAD")
	archive.Stderr = os.Stderr
	files, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	if err := archive.Start(); err != nil {
		return err
	}
	if err := runLocal(files, "docker", append(build, "-")...); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	if err := archive.Wait(); err != nil {
		return err
	}

	config, err := os.MkdirTemp("", "chasen-registry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(config)
	// The directory of the login has no Docker context. Name the Docker that
	// made the build (Colima, a remote context), so the push goes to the same one.
	docker := []string{"--config", config}
	if host, err := exec.Command("docker", "context", "inspect", "-f", "{{.Endpoints.docker.Host}}").Output(); err == nil && len(bytes.TrimSpace(host)) > 0 {
		docker = append(docker, "-H", string(bytes.TrimSpace(host)))
	}
	push := []string{"push", "-q", app.Image}
	registry, err := registryLogin(app)
	if err != nil {
		return err
	}
	if registry != nil {
		login := append(slices.Clone(docker), "login", "-u", registry.Username, "--password-stdin")
		if host := protocol.RegistryHost(app.Image); host != "" {
			login = append(login, host)
		}
		if err := runLocal(strings.NewReader(registry.Password), "docker", login...); err != nil {
			return fmt.Errorf("the registry refused the login of %s", registry.Username)
		}
		push = append(slices.Clone(docker), push...)
	}
	fmt.Println("Pushing", app.Image)
	if err := runLocal(nil, "docker", push...); err != nil {
		return fmt.Errorf("docker push failed: %w", err)
	}
	return nil
}

// runLocal runs a command on this computer and shows its errors.
func runLocal(stdin io.Reader, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stderr = stdin, os.Stderr
	return cmd.Run()
}

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

// sendSettings sends the settings of chasen.yml: the image, the env, the
// secrets, and the overrides of the standard.
func sendSettings(creds credentials, app appFile, slot []string) error {
	secrets, err := secretValues(app, app.Secrets)
	if err != nil {
		return err
	}
	env := maps.Clone(app.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, name := range app.Secrets {
		env[name] = secrets[name]
	}
	settings := protocol.Settings{
		Image: app.Image, Env: env, Port: app.Port, Health: app.Health, HealthTimeout: app.HealthTimeout, Volumes: app.Volumes,
	}
	if app.Image != "" {
		if settings.Registry, err = registryLogin(app); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(settings)
	return remote(creds, bytes.NewReader(body), os.Stdout, slot...)
}

// restart sends the settings of chasen.yml to the server and starts the app
// again with them, from the image it already has: a change of configuration
// or a new secret needs no build.
func restart(creds credentials, app appFile) error {
	// The app starts again from the image it has. No pull, so no image and no registry login.
	app.Image, app.Registry.Password = "", ""
	if err := sendSettings(creds, app, []string{"env", app.Name}); err != nil {
		return err
	}
	return remote(creds, nil, os.Stdout, "restart", app.Name)
}

// secretValues returns the values of the named secrets. A secret comes from
// the output of secrets_command, or from the environment.
func secretValues(app appFile, names []string) (map[string]string, error) {
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
			missing = append(missing, name)
		}
		values[name] = value
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing secrets: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

// parseDotenv reads KEY=VALUE lines. It accepts an `export ` prefix and quotes.
// ponytail: one line per value. Multi-line values need a real dotenv parser.
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
			value = value[1 : len(value)-1]
		}
		env[strings.TrimSpace(key)] = value
	}
	return env
}

// placed returns the credentials with the cloud server for a new app. The
// cloud says which servers the account has and what a new one costs, and the
// user picks. A plain server, an app that already has its server, and a run
// without a terminal (CI) need no question: there the cloud decides.
func placed(creds credentials, app string) (credentials, error) {
	if creds.Server != "" {
		return creds, nil
	}
	p, ok, err := protocol.Client(creds).Placement(context.Background(), app)
	if errors.Is(err, protocol.ErrUnauthorized) {
		return creds, errUnauthorized
	}
	if err != nil || !ok || p.Server != "" {
		return creds, err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return creds, nil
	}
	creds.Server, err = choosePlacement(app, p, os.Stdin, os.Stdout)
	return creds, err
}

func euros(cents int64) string { return fmt.Sprintf("€%d.%02d", cents/100, cents%100) }

// choosePlacement asks where a new app goes: one of the servers of the
// account, or a new server of one of the types. The first option is the
// default, and it is the cheapest one: a server you already pay for, or the
// cheapest type. The answer has the form of protocol.ServerHeader.
func choosePlacement(app string, p protocol.Placement, in io.Reader, out io.Writer) (string, error) {
	var choices []string
	table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	for _, s := range p.Servers {
		choices = append(choices, s.ID)
		apps := "no apps yet"
		if len(s.Apps) > 0 {
			apps = "runs " + strings.Join(s.Apps, ", ")
		}
		fmt.Fprintf(table, "  %d\tyour server %s\t%s\t%s\t%s\tno extra cost\n", len(choices), s.ID, s.Region, cmp.Or(s.Type, "your own"), apps)
	}
	if p.CanCreate {
		// The list is cheapest first. Show the four cheapest of each region:
		// --new=<type>@<location> reaches the larger ones.
		shown := map[string]int{}
		for _, t := range p.Types {
			if shown[t.Region]++; shown[t.Region] > 4 {
				continue
			}
			choices = append(choices, "new:"+t.Name+"@"+t.Location)
			fmt.Fprintf(table, "  %d\ta new server\t%s\t%s\t%d vCPU, %d GB memory, %d GB disk\t%s a month at Hetzner\n", len(choices),
				t.Region, t.Name, t.Cores, t.MemoryGB, t.DiskGB, euros(t.MonthlyCents))
		}
	}
	if len(choices) < 2 {
		return "", nil // nothing to choose. The cloud uses the one server, or says why it cannot
	}

	if len(p.Servers) == 0 {
		fmt.Fprintf(out, "%s is your first app. Chasen creates a server for it. Which one?\n\n", app)
	} else {
		fmt.Fprintf(out, "%s is a new app. Where does it go?\n\n", app)
	}
	table.Flush()
	if p.CanCreate {
		fmt.Fprintln(out, "\nPick the region of your customers. A server costs what Hetzner charges at this moment, without VAT.")
		fmt.Fprintf(out, "Chasen is %s a month for your account, with any number of servers.\n", euros(p.FeeCents))
	} else if p.Reason != "" {
		fmt.Fprintf(out, "\nA new server is not possible now: %s\n", p.Reason)
	}

	lines := bufio.NewReader(in)
	for {
		fmt.Fprintf(out, "Choice [1]: ")
		line, err := lines.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" && err == nil {
			return choices[0], nil
		}
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(choices) {
			return choices[n-1], nil
		}
		if err != nil {
			return "", errors.New("no choice. Nothing is deployed")
		}
		fmt.Fprintf(out, "Type a number from 1 to %d.\n", len(choices))
	}
}

// remote runs one command through the API of a server, or of the cloud. It
// sends stdin as the request body and writes the output as it arrives.
func remote(creds credentials, stdin io.Reader, out io.Writer, args ...string) error {
	code, err := protocol.Client(creds).Run(context.Background(), args[0], args[1:], stdin, out)
	if errors.Is(err, protocol.ErrUnauthorized) {
		return errUnauthorized
	}
	if code != 0 {
		// The server already printed the reason.
		os.Exit(code)
	}
	return err
}
