package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/karloscodes/chasen/protocol"
	"gopkg.in/yaml.v3"
)

// appFile is chasen.yml in the app directory. Every field is optional.
type appFile struct {
	Name string `yaml:"name"`
	// Server names the server of this app, when you have more than one:
	// a domain like example.com, or "cloud".
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
options:
	for i := 0; i < len(args); i++ {
		hasValue := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
		switch {
		case args[i] == "run":
			// The words after run are a command for the container, with its
			// own options: chasen run ls -la.
			break options
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
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown option %s, or it has no value\n\n%s", args[i], usage)
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
	case "secrets":
		return secretsCommand(args[1:])
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
	case "list", "load":
		return remote(creds, nil, os.Stdout, cmd)
	case "deploy", "check":
		app, err := loadAppFile()
		if err != nil {
			return err
		}
		return deploy(creds, app, cmd)
	case "enable":
		if len(args) < 2 {
			return errors.New("usage: chasen enable fusionaly|formlander|lognorth [domain]")
		}
		creds, err := placed(creds, args[1])
		if err != nil {
			return err
		}
		return remote(creds, nil, os.Stdout, args...)
	case "run":
		if len(args) < 2 {
			return errors.New("usage: chasen run <command> [arguments]. For example: chasen run bin/rails db:migrate")
		}
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
	switch args[0] {
	case "ssh", "download":
		app, err := loadAppFile()
		if err != nil {
			return err
		}
		if args[0] == "ssh" && len(args) == 1 {
			return shell(creds, app.Name)
		}
		if args[0] == "download" && len(args) <= 2 {
			return downloadBackup(creds, app.Name, strings.Join(args[1:], ""))
		}
		return errors.New("usage: chasen ssh, or chasen download [backup]")
	}
	// Every other command of the protocol runs on the server, for the app of this directory.
	if !slices.Contains(protocol.Commands, args[0]) {
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
		return app, appFileError(err)
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

// unknownKey matches what the YAML decoder says for a key that appFile does not have.
var unknownKey = regexp.MustCompile(`line (\d+): field (\S+) not found in type \S+`)

// appFileError says what is wrong in chasen.yml, in the words of the file,
// with the keys it can have and the page that explains them.
func appFileError(err error) error {
	message := unknownKey.ReplaceAllString(err.Error(), "line $1: unknown key `$2`")
	message = strings.TrimPrefix(strings.ReplaceAll(message, "yaml: unmarshal errors:\n  ", ""), "yaml: ")
	return fmt.Errorf("chasen.yml: %s\n  The keys of chasen.yml: name, server, image, registry, env, secrets, secrets_command, port, health, health_timeout, volumes.\n  %s%s", message, docsURL, docsAppFile)
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
