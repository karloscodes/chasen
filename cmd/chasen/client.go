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
	"golang.org/x/term"
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
	// Backup is false for an app whose data needs no backup: a demo that
	// makes its data again at each start. Without the key, the app is backed up.
	Backup *bool `yaml:"backup"`

	// Cron is the commands that the server runs in the container on a
	// schedule, in UTC.
	Cron []protocol.CronJob `yaml:"cron"`
	// Jobs is the command of a second container for the jobs of the app,
	// like the job role of Kamal: bin/jobs.
	Jobs string `yaml:"jobs"`

	Port          int      `yaml:"port"`
	Memory        string   `yaml:"memory"`
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

// domainFlag is the domain from `deploy --domain <domain>`: the domain of an
// app at its first deploy.
var domainFlag string

// jsonFlag is `overview --json`: every server with its apps, as JSON.
var jsonFlag bool

// watchFlag is `overview --watch`: the JSON of --json again and again, one
// line each time, on connections that stay open.
var watchFlag bool

// waybarFlag is `alerts --waybar`: the alerts of every server, as the JSON
// of a Waybar module.
var waybarFlag bool

// serverFlag is the cloud server from `--on <id>`, `--new`, or `--new=<type>@<location>`, in the
// form of protocol.ServerHeader.
var serverFlag string

func runClient(args []string) error {
options:
	for i := 0; i < len(args); i++ {
		hasValue := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
		switch {
		case args[i] == "run" || args[i] == "bucket":
			// The words after run are a command for the container, with its
			// own options: chasen run ls -la. The options of bucket are for
			// the server.
			break options
		case (args[i] == "-a" || args[i] == "--app") && hasValue:
			appFlag = args[i+1]
		case args[i] == "--on" && hasValue:
			serverFlag = args[i+1]
		case args[i] == "--tag" && hasValue:
			tagFlag = args[i+1]
		case args[i] == "--domain" && hasValue:
			domainFlag = strings.ToLower(args[i+1])
		case args[i] == "--json" && len(args) > 0 && args[0] == "overview":
			jsonFlag = true
			args = slices.Delete(args, i, i+1)
			i--
			continue
		case args[i] == "--watch" && len(args) > 0 && args[0] == "overview":
			watchFlag = true
			args = slices.Delete(args, i, i+1)
			i--
			continue
		case args[i] == "--waybar":
			waybarFlag = true
			args = slices.Delete(args, i, i+1)
			i--
			continue
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
	case "alerts":
		// The module of the top bar asks every server, with no current one.
		if waybarFlag {
			return waybarAlerts(os.Stdout)
		}
	case "overview":
		if watchFlag {
			return watchOverview(os.Stdin, os.Stdout)
		}
		if jsonFlag {
			return overviewAll(os.Stdout)
		}
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
	// `chasen deploy root@203.0.113.5` names the server of the deploy. The
	// first time, that is all it takes: the server gets Chasen, this computer
	// gets its login, and the app goes live.
	login := func() (credentials, error) { return loadCredentials(app.Server) }
	if args[0] == "deploy" && len(args) > 1 {
		if len(args) > 2 {
			return errors.New("usage: chasen deploy [<user>@<host>] [--domain <domain>] [--tag <tag>]")
		}
		server := args[1]
		args = args[:1]
		login = func() (credentials, error) { return serverLogin(server) }
	}
	creds, err := login()
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
		if creds, err = login(); err != nil {
			return err
		}
		creds.Server = serverFlag
		return runCommand(creds, args)
	}
	return err
}

func runCommand(creds credentials, args []string) error {
	switch cmd := args[0]; cmd {
	case "list", "load", "alerts", "overview":
		return remote(creds, nil, os.Stdout, cmd)
	case "bucket":
		return bucket(creds, args[1:])
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
	if err := remote(creds, nil, os.Stdout, append([]string{args[0], app.Name}, args[1:]...)...); err != nil {
		return err
	}
	// A new domain: say whether it reaches the app yet.
	if len(args) == 3 && args[0] == "domains" && args[1] == "add" {
		checkAddresses(os.Stdout, appURLs("  https://"+strings.ToLower(args[2])+"\n"))
	}
	return nil
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
	return fmt.Errorf("chasen.yml: %s\n  The keys of chasen.yml: name, server, image, registry, env, secrets, secrets_command, port, health, health_timeout, volumes, memory, backup, cron, jobs.\n  %s%s", message, docsURL, docsAppFile)
}

// bucket shows where the backups of the server go, or sets the bucket: the
// server tests the bucket before it saves it. The secret access key does not
// go on the command line: it comes from S3_SECRET_ACCESS_KEY, or the person
// types it, and it travels in the request, like every secret.
func bucket(creds credentials, args []string) error {
	if len(args) == 0 {
		return remote(creds, nil, os.Stdout, "bucket")
	}
	secret := os.Getenv("S3_SECRET_ACCESS_KEY")
	if secret == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("the secret access key is missing: set S3_SECRET_ACCESS_KEY, or run the command at a terminal")
		}
		fmt.Fprint(os.Stderr, "Secret access key: ")
		typed, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(string(typed))
	}
	return remote(creds, strings.NewReader(secret+"\n"), os.Stdout, append([]string{"bucket"}, args...)...)
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
