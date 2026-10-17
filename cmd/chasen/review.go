package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// The review of a deploy. Before chasen builds, pushes, or changes anything
// on the server, it reads chasen.yml, and after the build it reads the image.
// It says what is wrong, what to write instead, and where the docs are.
//
// An error is something no server accepts, or that cannot work: the deploy
// stops. A warning is something that often ends in a failed deploy or in
// lost data, and that can also be right: the deploy goes on.

// finding is one thing the review found.
type finding struct {
	Error bool   // an error stops the deploy. A warning does not
	What  string // what is wrong
	Fix   string // what to do
	Docs  string // the page that explains it, under docsURL
}

const docsURL = "https://chasenhq.com/docs/"

// The pages of the docs that the review points to.
const (
	docsAppFile    = "deploy/#chasenyml"
	docsImage      = "deploy/#the-image"
	docsDockerfile = "standard/#the-dockerfile-is-the-contract"
	docsEnv        = "standard/#4-environment"
	docsSecrets    = "secrets/"
)

var (
	// A name that says its value is a secret.
	secretName = regexp.MustCompile(`SECRET|PASSWORD|PASSWD|TOKEN|PRIVATE_KEY|API_KEY|ACCESS_KEY`)
	// How the tokens of the common registries start.
	tokenValue = regexp.MustCompile(`^(ghp_|gho_|ghs_|github_pat_|dckr_pat_|glpat-)`)
	// The name of an image in a registry, with no tag: ghcr.io/you/app.
	imageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[0-9]+)?(/[a-z0-9._/-]+)*$`)
)

// reviewAppFile reviews chasen.yml and the name of the app. dockerfile says
// that this directory has a Dockerfile, and tag is the value of --tag.
func reviewAppFile(app appFile, dockerfile bool, tag string) []finding {
	var found []finding
	if err := protocol.CheckAppName(app.Name); err != nil {
		found = append(found, finding{true, firstSentence(err), "The name is a part of the address of the app. Without `name:` in chasen.yml, it is the name of this directory.", docsAppFile})
	}

	// The same rules that the server applies when the deploy arrives.
	env := maps.Clone(app.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, name := range app.Secrets {
		env[name] = ""
	}
	settings := protocol.Settings{Env: env, Port: app.Port, Health: app.Health, HealthTimeout: app.HealthTimeout, Volumes: app.Volumes, Memory: app.Memory, Cron: app.Cron, Jobs: app.Jobs}
	for _, err := range unjoin(settings.Check()) {
		f := finding{true, "chasen.yml: " + err.Error(), "", docsAppFile}
		if strings.HasPrefix(err.Error(), "chasen sets ") {
			f.Fix, f.Docs = "Chasen gives this variable to every app, with its own value. The app reads it; chasen.yml cannot change it.", docsEnv
		}
		found = append(found, f)
	}
	image, imageTag := app.Image, ""
	if at := strings.LastIndexAny(image, ":@"); at > strings.LastIndex(image, "/") {
		image, imageTag = image[:at], image[at+1:]
	}
	if image != "" && !imageName.MatchString(image) {
		found = append(found, finding{true, fmt.Sprintf("chasen.yml: invalid image %q", app.Image), "Write the image like ghcr.io/you/app, in lowercase, without https://.", docsImage})
	}

	switch registry := app.Registry; {
	case tokenValue.MatchString(registry.Password):
		found = append(found, finding{true, "chasen.yml: registry.password holds a token, and chasen.yml goes into git",
			"Write the name of a secret there (password: GHCR_TOKEN) and give its value with secrets_command or the environment. Make a new token: this one is not secret any more.", docsSecrets})
	case registry.Username != "" && registry.Password == "":
		found = append(found, finding{true, "chasen.yml: registry has a username and no password", "Add the name of the secret that holds the token: password: GHCR_TOKEN.", docsImage})
	case registry.Username == "" && registry.Password != "":
		found = append(found, finding{true, "chasen.yml: registry has a password and no username", "Add the user of the registry: username: <you>.", docsImage})
	}

	for _, name := range slices.Sorted(maps.Keys(app.Env)) {
		switch {
		case slices.Contains(app.Secrets, name):
			found = append(found, finding{false, fmt.Sprintf("chasen.yml: %s is in env and in secrets", name), "The secret wins. Remove the name from env.", docsSecrets})
		case secretName.MatchString(strings.ToUpper(name)) && !strings.Contains(strings.ToUpper(name), "PUBLIC") && app.Env[name] != "":
			found = append(found, finding{false, fmt.Sprintf("chasen.yml: the value of %s is in env, as plain text, and chasen.yml goes into git", name),
				"If it is a secret, add the name to `secrets:` and take the value out of the file.", docsSecrets})
		}
	}

	if dockerfile && tag == "" && imageTag != "" {
		found = append(found, finding{false, fmt.Sprintf("chasen.yml: image names the tag %s, so chasen does not build the Dockerfile of this directory", imageTag),
			"To build and deploy the current commit, write the image without a tag.", docsImage})
	}
	return found
}

// imageFacts is what an image says about itself: the part of
// `docker image inspect` that the standard reads.
type imageFacts struct {
	Ports   []int    // the TCP ports of EXPOSE, sorted
	Volumes []string // the paths of VOLUME, sorted
	Command bool     // the image has a CMD or an ENTRYPOINT
}

// inspectImage reads the facts of an image that this Docker has.
func inspectImage(image string) (imageFacts, error) {
	out, err := exec.Command("docker", "image", "inspect", "-f", "{{json .Config}}", image).Output()
	if err != nil {
		return imageFacts{}, fmt.Errorf("cannot read the image %s: %w", image, err)
	}
	var config struct {
		ExposedPorts map[string]struct{}
		Volumes      map[string]struct{}
		Cmd          []string
		Entrypoint   []string
	}
	if err := json.Unmarshal(out, &config); err != nil {
		return imageFacts{}, fmt.Errorf("cannot read the image %s: %w", image, err)
	}
	facts := imageFacts{Volumes: slices.Sorted(maps.Keys(config.Volumes)), Command: len(config.Cmd)+len(config.Entrypoint) > 0}
	for key := range config.ExposedPorts {
		number, proto, _ := strings.Cut(key, "/")
		if n, err := strconv.Atoi(number); err == nil && (proto == "tcp" || proto == "") {
			facts.Ports = append(facts.Ports, n)
		}
	}
	slices.Sort(facts.Ports)
	return facts, nil
}

// reviewImage reviews the image that the build made, against what the server
// does with it. An image that says nothing about its port or its storage gets
// no word: the defaults apply, and a deploy that fails says what the app did.
func reviewImage(app appFile, image imageFacts) []finding {
	var found []finding
	settings := protocol.Settings{Port: app.Port, Volumes: app.Volumes}
	if _, err := protocol.ShapeOf(settings, image.Ports, image.Volumes); err != nil {
		found = append(found, finding{true, firstSentence(err), strings.TrimSpace(strings.TrimPrefix(err.Error(), firstSentence(err)+".")), docsDockerfile})
	}
	if !image.Command {
		found = append(found, finding{true, "the image has no command to start the app", `Add the command to the end of the Dockerfile: CMD ["/app/server"].`, docsDockerfile})
	}
	return found
}

// printReview prints the findings, the errors first. With an error, it
// returns one, and the deploy stops.
func printReview(w io.Writer, found []finding) error {
	slices.SortStableFunc(found, func(a, b finding) int {
		switch {
		case a.Error && !b.Error:
			return -1
		case b.Error && !a.Error:
			return 1
		}
		return 0
	})
	errorCount := 0
	for _, f := range found {
		level := "Warning"
		if f.Error {
			level = "Error"
			errorCount++
		}
		fmt.Fprintf(w, "%s: %s\n", level, f.What)
		if f.Fix != "" {
			fmt.Fprintf(w, "  %s\n", f.Fix)
		}
		fmt.Fprintf(w, "  %s%s\n\n", docsURL, f.Docs)
	}
	if errorCount > 0 {
		return fmt.Errorf("the review found %s. Nothing was pushed and nothing changed on the server", plural(errorCount, "error"))
	}
	if len(found) > 0 {
		fmt.Fprintf(w, "%s. A warning does not stop the deploy.\n\n", plural(len(found), "warning"))
	}
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// unjoin returns the errors that errors.Join put together.
func unjoin(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	if err != nil {
		return []error{err}
	}
	return nil
}

// firstSentence returns the message of an error up to its first period.
func firstSentence(err error) string {
	message := err.Error()
	if before, _, ok := strings.Cut(message, ". "); ok {
		return before
	}
	return message
}

var errNoDocker = errors.New(`Docker is not on this computer, or not in the PATH. Chasen builds the image where it runs, and the server does not build.
  Install Docker (or Colima, or OrbStack), or deploy from CI.
  ` + docsURL + "github-actions/")
