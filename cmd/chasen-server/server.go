package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
	"gopkg.in/yaml.v3"
)

// serverConfig is the settings of the server. They are rows of the settings
// table of the database, one for each value. The yaml names are those of
// /etc/chasen/config.yml, the file that held them before.
type serverConfig struct {
	// Domain is the base domain. Each app gets <name>.<Domain>.
	Domain string `yaml:"domain"`
	// Token is the secret that a client sends with each API request.
	Token  string `yaml:"token"`
	Backup struct {
		// S3 is the bucket for the offsite copies. A server without one is
		// complete: its backups stay on its disk.
		S3           *s3Config `yaml:"s3,omitempty"`
		HeartbeatURL string    `yaml:"heartbeat_url,omitempty"`
	} `yaml:"backup,omitempty"`
	// AutoUpdate turns the nightly update off when it is false. Without the
	// setting, the server updates itself.
	AutoUpdate *bool `yaml:"auto_update,omitempty"`
}

// CHASEN_ROOT moves all server state under one directory. Tests use it.
func root() string            { return os.Getenv("CHASEN_ROOT") }
func configPath() string      { return root() + "/etc/chasen/config.yml" }
func appsPath() string        { return root() + "/etc/chasen/apps.yml" }
func envPath(n string) string { return root() + "/etc/chasen/env/" + n + ".json" }
func appDir(n string) string  { return root() + "/var/matcha/" + n }
func dataDir(n string) string { return appDir(n) + "/storage" }

func runServer(args []string) error {
	if os.Geteuid() != 0 && root() == "" {
		return errors.New("chasen-server must run as root")
	}

	cmd, args := args[0], args[1:]
	switch cmd {
	case "setup":
		return serverSetup(args)
	case "check":
		// With no arguments: the security check of the server. With an app: the standard.
		if len(args) == 0 {
			return matcha.Check()
		}
	case "bucket":
		return serverBucket(args)
	case "settings":
		return serverSettings(args)
	case "token":
		cfg, err := loadServerConfig()
		if err != nil {
			return err
		}
		fmt.Println(cfg.Token)
		return nil
	case "update":
		return serverUpdate()
	case "replicate":
		return serverReplicate()
	case "serve":
		return serverServe()
	case "list":
		return serverList()
	case "load":
		return serverLoad()
	case "quiet-hour":
		return serverQuietHour()
	case "backup":
		if len(args) == 0 {
			return backupAll()
		}
	}

	if len(args) == 0 {
		return fmt.Errorf("usage: chasen-server %s <app>", cmd)
	}
	name, args := args[0], args[1:]
	if err := checkAppName(name); err != nil {
		return err
	}

	switch cmd {
	case "deploy", "enable", "restart", "domains", "restore", "remove":
		if err := lock(); err != nil {
			return err
		}
	}

	switch cmd {
	case "deploy":
		if len(args) != 1 {
			return errors.New("usage: chasen-server deploy <app> <version>")
		}
		return serverDeploy(name, args[0])
	case "check":
		if len(args) != 1 {
			return errors.New("usage: chasen-server check <app> <version>")
		}
		return serverCheck(name, args[0])
	case "enable":
		return serverEnable(name, args)
	case "restart":
		return serverRestart(name)
	case "domains":
		return serverDomains(name, args)
	case "status":
		return serverStatus(name)
	case "run":
		return serverRun(name, args)
	case "logs":
		app, err := loadApp(name)
		if err != nil {
			return err
		}
		return engine(name, app).Logs()
	case "backup":
		cfg, err := loadServerConfig()
		if err != nil {
			return err
		}
		return printBackup(name, cfg)
	case "history":
		return serverHistory(name, args)
	case "backups":
		return serverBackups(name)
	case "restore":
		return serverRestore(name, strings.Join(args, " "))
	case "remove":
		return serverRemove(name)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

// lock makes the commands that change an app run one at a time.
// The lock ends when the process exits.
func lock() error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0755); err != nil {
		return err
	}
	var err error
	lockFile, err = os.OpenFile(filepath.Dir(configPath())+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	return syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX)
}

// lockFile stays open for the life of the process. A closed file drops its lock.
var lockFile *os.File

func loadServerConfig() (serverConfig, error) {
	cfg, found, err := readServerConfig()
	if err == nil && !found {
		err = errors.New("the server is not set up. Run on the server: chasen-server setup --domain <base domain>")
	}
	return cfg, err
}

// readServerConfig reads the settings from the database. found is false on a
// server that has none yet. A server from before the settings were in the
// database has them in config.yml: they are copied once, and the file stays
// for the previous version, which an update can go back to.
func readServerConfig() (cfg serverConfig, found bool, err error) {
	db, err := openServerDB()
	if err != nil {
		return cfg, false, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name, value FROM settings")
	if err != nil {
		return cfg, false, err
	}
	saved := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return cfg, false, err
		}
		saved[name] = value
	}
	if err := rows.Close(); err != nil {
		return cfg, false, err
	}

	// A server with an empty database takes its settings from config.yml, one
	// time. That is the move from an older version, and it is also how a
	// script gives a new server its domain and its token before `setup`.
	// Programs outside this repository use it. Keep it.
	if len(saved) == 0 {
		data, err := os.ReadFile(configPath())
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, false, nil
		}
		if err != nil {
			return cfg, false, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, false, fmt.Errorf("%s: %w", configPath(), err)
		}
		return cfg, true, saveServerConfig(cfg)
	}

	cfg.Domain, cfg.Token = saved["domain"], saved["token"]
	cfg.Backup.HeartbeatURL = saved["heartbeat_url"]
	if value, ok := saved["auto_update"]; ok {
		on := value == "true"
		cfg.AutoUpdate = &on
	}
	if saved["s3_bucket"] != "" {
		cfg.Backup.S3 = &s3Config{
			Endpoint:        saved["s3_endpoint"],
			Region:          saved["s3_region"],
			Bucket:          saved["s3_bucket"],
			AccessKeyID:     saved["s3_access_key_id"],
			SecretAccessKey: saved["s3_secret_access_key"],
		}
	}
	return cfg, true, nil
}

// saveServerConfig replaces the settings in the database. A value that is
// empty has no row.
func saveServerConfig(cfg serverConfig) error {
	values := map[string]string{"domain": cfg.Domain, "token": cfg.Token, "heartbeat_url": cfg.Backup.HeartbeatURL}
	if cfg.AutoUpdate != nil {
		values["auto_update"] = strconv.FormatBool(*cfg.AutoUpdate)
	}
	if s3 := cfg.Backup.S3; s3 != nil {
		values["s3_endpoint"], values["s3_region"], values["s3_bucket"] = s3.Endpoint, s3.Region, s3.Bucket
		values["s3_access_key_id"], values["s3_secret_access_key"] = s3.AccessKeyID, s3.SecretAccessKey
	}

	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM settings"); err != nil {
		return err
	}
	for name, value := range values {
		if value == "" {
			continue
		}
		if _, err := tx.Exec("INSERT INTO settings (name, value) VALUES (?, ?)", name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// serverSettings shows the settings of the server, or changes one of the two
// that have no other command: auto_update and heartbeat_url.
func serverSettings(args []string) error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		update, bucket, heartbeat := "on", "none: the backups stay on this server", "none"
		if cfg.AutoUpdate != nil && !*cfg.AutoUpdate {
			update = "off"
		}
		if s3 := cfg.Backup.S3; s3 != nil {
			bucket = fmt.Sprintf("%s at %s (region %s)", s3.Bucket, s3.Endpoint, s3.Region)
		}
		if cfg.Backup.HeartbeatURL != "" {
			heartbeat = cfg.Backup.HeartbeatURL
		}
		fmt.Printf("domain         %s\nauto_update    %s\nheartbeat_url  %s\nbucket         %s\n", cfg.Domain, update, heartbeat, bucket)
		fmt.Println("\nThe token: chasen-server token. The bucket: chasen-server bucket.")
		return nil
	case len(args) == 2 && args[0] == "auto_update" && (args[1] == "on" || args[1] == "off"):
		on := args[1] == "on"
		cfg.AutoUpdate = &on
		if err := saveServerConfig(cfg); err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		fmt.Printf("The nightly update is %s.\n", args[1])
		return installTimer(self, on)
	case len(args) == 2 && args[0] == "heartbeat_url":
		if args[1] != "" && !strings.HasPrefix(args[1], "https://") && !strings.HasPrefix(args[1], "http://") {
			return errors.New("the heartbeat URL must start with https://")
		}
		cfg.Backup.HeartbeatURL = args[1]
		if err := saveServerConfig(cfg); err != nil {
			return err
		}
		if args[1] == "" {
			fmt.Println("No heartbeat.")
		} else {
			fmt.Println("The server calls the URL after each hourly backup that worked.")
		}
		return nil
	}
	return errors.New("usage: chasen-server settings [auto_update on|off | heartbeat_url <url>]")
}

// serverBucket sets the S3 bucket for the offsite backups and the live
// replica. It checks that the bucket works before it saves anything.
func serverBucket(args []string) error {
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if cfg.Backup.S3 == nil {
			fmt.Println("No bucket. The backups stay on this server.")
			return nil
		}
		fmt.Printf("Bucket %s at %s (region %s)\n", cfg.Backup.S3.Bucket, cfg.Backup.S3.Endpoint, cfg.Backup.S3.Region)
		return nil
	}

	flags := flag.NewFlagSet("bucket", flag.ContinueOnError)
	s3 := &s3Config{}
	flags.StringVar(&s3.Endpoint, "endpoint", "", "the S3 endpoint, for example https://fsn1.your-objectstorage.com")
	flags.StringVar(&s3.Region, "region", "auto", "the region")
	flags.StringVar(&s3.Bucket, "name", "", "the name of the bucket")
	flags.StringVar(&s3.AccessKeyID, "access-key-id", "", "the access key id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if s3.Endpoint == "" || s3.Bucket == "" || s3.AccessKeyID == "" {
		return errors.New("usage: chasen-server bucket --endpoint <url> --name <bucket> --access-key-id <id> [--region <region>]")
	}
	// The secret does not go on the command line: other users of the machine can read it there.
	if s3.SecretAccessKey = os.Getenv("S3_SECRET_ACCESS_KEY"); s3.SecretAccessKey == "" {
		fmt.Print("Secret access key: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		s3.SecretAccessKey = strings.TrimSpace(line)
	}

	// Prove that the keys can create the bucket, write, and delete.
	if err := s3.createBucket(); err != nil {
		return fmt.Errorf("the bucket does not work, nothing changed: %w", err)
	}
	probe := filepath.Join(os.TempDir(), "chasen-bucket-check")
	if err := os.WriteFile(probe, []byte("chasen"), 0600); err != nil {
		return err
	}
	defer os.Remove(probe)
	if err := s3.putFile("chasen-bucket-check", probe); err != nil {
		return fmt.Errorf("the bucket does not work, nothing changed: %w", err)
	}
	if err := s3.delete("chasen-bucket-check"); err != nil {
		return fmt.Errorf("the bucket does not work, nothing changed: %w", err)
	}

	cfg.Backup.S3 = s3
	if err := saveServerConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Backups now go to the bucket %s, and the live replica starts.\n", s3.Bucket)

	// The API reads the config when it starts.
	if running, _ := docker("ps", "-q", "--filter", "name=^"+agentContainer+"$"); running == "" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := startAgent(self); err != nil {
		return err
	}
	_, err = routeAgent(cfg.Domain)
	return err
}

func loadApp(name string) (matcha.AppConfig, error) {
	app, err := matcha.LoadAppFrom(appsPath(), name)
	if err != nil {
		return app, fmt.Errorf("app %q is not deployed", name)
	}
	return app, nil
}

// engine returns the matcha deployer for an app. Chasen keeps its apps in its
// own file, so the nightly `matcha update-all` does not touch them. The proxy
// and the network are the same as matcha uses.
func engine(name string, app matcha.AppConfig) *matcha.Matcha {
	return matcha.NewFromApp(name, app, matcha.Config{
		ConfigPath:  appsPath(),
		DataDirBase: root() + "/var/matcha",
		SkipPull:    true, // chasen pulls the image itself, with the login of the deploy
	})
}

// imageRepo is the local name of the images of an app. The server pulls an
// image under its real name and keeps it under this one. The registry host is
// under .invalid, which never resolves, so nothing can pull or push this name.
func imageRepo(name string) string { return "chasen.invalid/" + name }

// run runs a command and shows its output.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func serverSetup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	domain := flags.String("domain", "", "base domain: each app gets <name>.<domain>")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, _, err := readServerConfig()
	if err != nil {
		return err
	}
	if *domain != "" {
		cfg.Domain = *domain
	}
	if !validDomain(cfg.Domain) {
		return errors.New("usage: chasen-server setup --domain <base domain>")
	}

	// matcha.Setup restarts the proxy, so run it only when the proxy is down.
	// With CHASEN_ROOT, the first deploy starts the proxy under that root instead.
	if out, _ := docker("ps", "-q", "--filter", "name=^matcha-proxy$"); out == "" && root() == "" {
		if err := matcha.Setup(); err != nil {
			return err
		}
	}

	if cfg.Token == "" {
		var err error
		if cfg.Token, err = matcha.GeneratePrivateKey(); err != nil {
			return err
		}
	}
	if err := saveServerConfig(cfg); err != nil {
		return err
	}

	if cfg.Backup.S3 != nil {
		if err := cfg.Backup.S3.createBucket(); err != nil {
			return fmt.Errorf("the backup bucket is not ready: %w", err)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := startAgent(self); err != nil {
		return err
	}
	// The server keeps itself up to date, unless `chasen-server settings auto_update off` said no.
	if err := installTimer(self, cfg.AutoUpdate == nil || *cfg.AutoUpdate); err != nil {
		return err
	}
	routed, err := routeAgent(cfg.Domain)
	if err != nil {
		return err
	}

	fmt.Printf("Chasen is ready. Apps get %s\n", link("<name>."+cfg.Domain))
	if !isLocal(cfg.Domain) {
		fmt.Printf("Point a wildcard A record for *.%s to this server.\n", cfg.Domain)
	}
	if cfg.Backup.S3 == nil {
		fmt.Println("Backups stay on this server. For offsite copies too, run: chasen-server bucket --endpoint <url> --name <bucket> --access-key-id <id>")
	}
	if routed {
		target := cfg.Domain
		if isLocal(cfg.Domain) {
			target = "http://api." + cfg.Domain
		}
		fmt.Printf("\nOn your machine, run:\n  chasen add server %s\n", target)
	}
	fmt.Printf("Server token (the login page asks for it): %s\n", cfg.Token)
	return nil
}

// An app name becomes a container name, a directory, and a DNS label.
var appNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func checkAppName(name string) error {
	if !appNameRe.MatchString(name) {
		return fmt.Errorf("invalid app name %q: use lowercase letters, digits, and hyphens. Set `name:` in chasen.yml", name)
	}
	// "api" is the host of the API, and the others are names the server uses itself.
	if name == "api" || name == "proxy" || name == "matcha-proxy" || name == agentContainer {
		return fmt.Errorf("the app name %q is reserved", name)
	}
	return nil
}

// primaryDomain is the first custom domain, or the default domain.
func primaryDomain(domains []string) string {
	switch len(domains) {
	case 0:
		return ""
	case 1:
		return domains[0]
	}
	return domains[1]
}

var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)

// imageRe matches an image with its tag or digest: ghcr.io/you/app:3f9a2c1.
var imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[0-9]+)?(/[a-z0-9._/-]+)*[:@][A-Za-z0-9._:-]{1,128}$`)

// A static website follows the standard like an app: Caddy listens on $PORT
// and answers /up. This is the only image that a server makes, and the server
// owns its Dockerfile: the client sends the files and nothing else.
const (
	websiteDockerfile = "FROM caddy:2-alpine\nCOPY Caddyfile /etc/caddy/Caddyfile\nADD site.tar.gz /srv\n"
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

// websiteImage makes the image of a website from the tar.gz archive of its files.
func websiteImage(files io.Reader, as string) error {
	dir, err := os.MkdirTemp("", "chasen-website-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	archive, err := os.Create(filepath.Join(dir, "site.tar.gz"))
	if err != nil {
		return err
	}
	size, err := io.Copy(archive, files)
	archive.Close()
	if err != nil {
		return err
	}
	if size == 0 {
		return errors.New("nothing to deploy. A website sends its files, and an app names its image in chasen.yml")
	}
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(websiteDockerfile), 0644)
	os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(websiteCaddyfile), 0644)
	if out, err := exec.Command("docker", "build", "-q", "-t", as, dir).CombinedOutput(); err != nil {
		return fmt.Errorf("cannot make the image of the website: %s", lastLine(string(out)))
	}
	return nil
}

// fetchImage puts the image of a version on the server, under the name `as`.
// An app is an image in a registry: the server pulls it, with the login of
// the settings when the image is private. A website has no image: its files
// follow the settings in the input.
func fetchImage(settings protocol.Settings, files io.Reader, as string) error {
	if settings.Image == "" {
		return websiteImage(files, as)
	}

	// The login stays in a directory that is gone after the pull.
	config, err := os.MkdirTemp("", "chasen-registry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(config)
	if r := settings.Registry; r != nil {
		args := []string{"--config", config, "login", "-u", r.Username, "--password-stdin"}
		if host := protocol.RegistryHost(settings.Image); host != "" {
			args = append(args, host)
		}
		login := exec.Command("docker", args...)
		login.Stdin = strings.NewReader(r.Password)
		if out, err := login.CombinedOutput(); err != nil {
			return fmt.Errorf("the registry refused the login of %s: %s", r.Username, lastLine(string(out)))
		}
	}
	// The name of the image can be on the server already: another tool runs
	// its apps from it. Then the name stays, and it keeps the image it had.
	before, missing := docker("image", "inspect", "--format", "{{.Id}}", settings.Image)
	fmt.Println("Pulling", settings.Image)
	pull := exec.Command("docker", "--config", config, "pull", "-q", settings.Image)
	if out, err := pull.CombinedOutput(); err != nil {
		hint := "Is the image pushed? A private image needs `registry:` in chasen.yml"
		return fmt.Errorf("cannot pull %s: %s\n%s", settings.Image, lastLine(string(out)), hint)
	}
	if out, err := docker("tag", settings.Image, as); err != nil {
		return errors.New(out)
	}
	if missing != nil {
		docker("rmi", settings.Image) // only the name: `as` keeps the image
	} else {
		docker("tag", before, settings.Image)
	}
	return nil
}

// link is the address of a domain for a person: https, or http for a domain
// that only resolves on one machine and gets no certificate.
func link(domain string) string {
	if isLocal(domain) {
		return "http://" + domain
	}
	return "https://" + domain
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// serverDeploy gets the image of a version and deploys it. The input is the
// settings on one line, then the files of a website.
func serverDeploy(name, version string) error {
	if !versionRe.MatchString(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	settings, _, err := readSettings(in)
	if err != nil {
		return err
	}
	if err := saveSettings(name, settings); err != nil {
		return err
	}

	image := imageRepo(name) + ":" + version
	if err := fetchImage(settings, in, image); err != nil {
		return err
	}

	old, oldErr := matcha.LoadAppFrom(appsPath(), name)
	domains := []string{name + "." + cfg.Domain}
	privateKey := old.Env["PRIVATE_KEY"]
	if oldErr == nil {
		domains = strings.Split(old.Domain, ",")
	}
	if privateKey == "" {
		if privateKey, err = matcha.GeneratePrivateKey(); err != nil {
			return err
		}
	}

	sh, err := appShape(image, settings)
	if err != nil {
		return err
	}
	fmt.Printf("Port %d (%s). Health path %s. Storage %s.\n", sh.Port, sh.PortFrom, sh.Health, sh.Volumes[0])

	if err := prepareData(name, image, sh.Volumes, cfg); err != nil {
		return err
	}

	app := matcha.AppConfig{
		Image:         image,
		Domain:        strings.Join(domains, ","),
		Port:          sh.Port,
		HealthPath:    sh.Health,
		HealthTimeout: sh.HealthTimeout,
		Volumes:       sh.Volumes,
		Env:           appEnv(settings, domains, version, privateKey, sh),
	}
	fmt.Printf("Starting %s %s\n", name, version)
	if err := apply(name, app); err != nil {
		return err
	}

	// Keep the image of this version and of the one before it.
	if tags, err := docker("images", imageRepo(name), "--format", "{{.Repository}}:{{.Tag}}"); err == nil {
		for i, tag := range strings.Fields(tags) {
			if i >= 2 {
				docker("rmi", tag)
			}
		}
	}

	fmt.Printf("\nDeployed %s %s\n", name, version)
	for _, d := range domains {
		fmt.Println("  " + link(d))
	}
	return nil
}

// serverRestart starts the app again from the image it has, with the
// environment the client sent last. `chasen restart` uses it to change the
// configuration or a secret without a build.
func serverRestart(name string) error {
	app, err := loadApp(name)
	if err != nil {
		return err
	}
	if isBuilt(name, app.Image) {
		// New settings come with the restart. Without them, the last ones stay.
		// They can change the port, the health path, and the volumes too.
		settings, sent, err := readSettings(bufio.NewReader(os.Stdin))
		if err != nil {
			return err
		}
		if sent {
			if err := saveSettings(name, settings); err != nil {
				return err
			}
		} else if settings, err = loadSettings(name); err != nil {
			return err
		}
		sh, err := appShape(app.Image, settings)
		if err != nil {
			return err
		}
		if err := prepareVolumes(name, app.Image, sh.Volumes); err != nil {
			return err
		}
		version := app.Image[strings.LastIndex(app.Image, ":")+1:]
		app.Port, app.HealthPath, app.HealthTimeout, app.Volumes = sh.Port, sh.Health, sh.HealthTimeout, sh.Volumes
		app.Env = appEnv(settings, strings.Split(app.Domain, ","), version, app.Env["PRIVATE_KEY"], sh)
	}
	fmt.Println("Restarting", name)
	if err := apply(name, app); err != nil {
		return err
	}
	fmt.Printf("Restarted %s with the new configuration\n", name)
	return nil
}

// apply saves the app and deploys it. When the deploy fails, the previous
// container keeps the traffic and apply puts the previous record back.
func apply(name string, app matcha.AppConfig) error {
	old, oldErr := matcha.LoadAppFrom(appsPath(), name)
	if err := matcha.SaveAppTo(appsPath(), name, app); err != nil {
		return err
	}
	if err := engine(name, app).Deploy(); err != nil {
		if oldErr == nil {
			matcha.SaveAppTo(appsPath(), name, old)
		} else {
			matcha.RemoveAppFrom(appsPath(), name)
		}
		return fmt.Errorf("%w\nThe app must listen on port %d and answer 200 on %s", err, app.Port, app.HealthPath)
	}
	return nil
}

// prepareData makes the data of an app ready for a new container: the volume
// directories exist and belong to the user of the image, and the databases are
// either backed up or, on a server that has none, restored from the offsite copy.
func prepareData(name, image string, volumes []string, cfg serverConfig) error {
	if err := prepareVolumes(name, image, volumes); err != nil {
		return err
	}
	dbs, err := findDatabases(appDir(name))
	if err != nil {
		return err
	}
	if len(dbs) > 0 {
		if err := printBackup(name, cfg); err != nil {
			return fmt.Errorf("the backup before the deploy failed, so the deploy stops: %w", err)
		}
		return nil
	}
	// No data on this server. If a copy exists offsite, the app comes back with it.
	// A fresh database must not start on top of that copy.
	staged, from, err := stageOffsite(name, cfg)
	if err != nil {
		return fmt.Errorf("this server has no data for %s, and the restore of the offsite copy failed: %w", name, err)
	}
	if len(staged) > 0 {
		if _, err := swap(name, staged); err != nil {
			return err
		}
		fmt.Printf("Restored the data of %s from %s\n", name, from)
	}
	return nil
}

// prepareVolumes creates the host directory of each volume and gives it to the
// user of the image, so an image with a non-root USER can write its database.
// The engine maps the volume /app/storage to /var/matcha/<app>/storage.
func prepareVolumes(name, image string, volumes []string) error {
	uid, gid := imageUser(image)
	for _, volume := range volumes {
		dir := filepath.Join(appDir(name), filepath.Base(volume))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if uid < 0 {
			continue
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			fmt.Printf("Warning: %v\n", err)
		}
	}
	return nil
}

// imageUser returns the uid and gid that the image runs as, or -1 for root or unknown.
func imageUser(image string) (uid, gid int) {
	user, err := docker("image", "inspect", "-f", "{{.Config.User}}", image)
	if err != nil || user == "" || user == "root" {
		return -1, -1
	}
	uidText, gidText, hasGroup := strings.Cut(user, ":")
	uid, uidErr := strconv.Atoi(uidText)
	gid, gidErr := strconv.Atoi(gidText)
	if uidErr != nil || (hasGroup && gidErr != nil) {
		// The user is a name. Ask the image for the numbers.
		ids, err := docker("run", "--rm", "--entrypoint", "sh", image, "-c", "id -u; id -g")
		fields := strings.Fields(ids)
		if err != nil || len(fields) != 2 {
			fmt.Printf("Warning: cannot find the uid of user %q. The app may not have write access to its volumes\n", user)
			return -1, -1
		}
		uid, _ = strconv.Atoi(fields[0])
		gid, _ = strconv.Atoi(fields[1])
	} else if !hasGroup {
		gid = uid
	}
	return uid, gid
}

// An addon is a ready image that Chasen runs for you: no source, no build.
type addon struct {
	Image   string
	Health  string
	Volumes []string
	About   string
}

var addons = map[string]addon{
	"fusionaly":  {"karloscodes/fusionaly:latest", "/_health", []string{"/app/storage", "/app/logs"}, "privacy-first web analytics"},
	"formlander": {"karloscodes/formlander:latest", "/_health", []string{"/app/storage", "/app/logs"}, "form backend for static sites"},
	"lognorth":   {"karloscodes/lognorth:latest", "/_health", []string{"/app/storage"}, "logs, errors, alerts, and uptime"},
}

// isBuilt reports an app that Chasen built from source on this server.
func isBuilt(name, image string) bool { return strings.HasPrefix(image, imageRepo(name)+":") }

// serverEnable runs an addon, or updates it to the newest image. The optional
// argument is its domain.
func serverEnable(name string, args []string) error {
	product, ok := addons[name]
	if !ok {
		names := make([]string, 0, len(addons))
		for n, a := range addons {
			names = append(names, fmt.Sprintf("  %-11s %s", n, a.About))
		}
		slices.Sort(names)
		return fmt.Errorf("no addon %q. You can enable:\n%s", name, strings.Join(names, "\n"))
	}
	cfg, err := loadServerConfig()
	if err != nil {
		return err
	}

	old, oldErr := matcha.LoadAppFrom(appsPath(), name)
	if oldErr == nil && isBuilt(name, old.Image) {
		return fmt.Errorf("an app named %s runs on this server. Remove it first, or keep it", name)
	}
	domain := name + "." + cfg.Domain
	privateKey := old.Env["PRIVATE_KEY"]
	if oldErr == nil {
		domain = old.Domain
	}
	if len(args) > 0 {
		if domain = strings.ToLower(args[0]); !validDomain(domain) {
			return fmt.Errorf("invalid domain %q", domain)
		}
	}
	if privateKey == "" {
		if privateKey, err = matcha.GeneratePrivateKey(); err != nil {
			return err
		}
	}

	fmt.Println("Pulling", product.Image)
	if out, err := docker("pull", "-q", product.Image); err != nil {
		return fmt.Errorf("cannot pull %s: %s", product.Image, out)
	}
	if err := prepareData(name, product.Image, product.Volumes, cfg); err != nil {
		return err
	}

	app := matcha.AppConfig{
		Image:      product.Image,
		Domain:     domain,
		Port:       defaultPort,
		HealthPath: product.Health,
		Volumes:    product.Volumes,
		Env:        standardEnv([]string{domain}, "latest", privateKey, shape{Port: defaultPort, Volumes: product.Volumes}),
	}
	fmt.Println("Starting", name)
	if err := apply(name, app); err != nil {
		return err
	}
	fmt.Printf("\nEnabled %s\n  %s\n", name, link(domain))
	return nil
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validDomain(d string) bool {
	return len(d) <= 253 && domainRe.MatchString(d)
}

func serverDomains(name string, args []string) error {
	app, err := loadApp(name)
	if err != nil {
		return err
	}
	domains := strings.Split(app.Domain, ",")
	if len(args) > 0 && !isBuilt(name, app.Image) {
		return fmt.Errorf("an addon has one domain. Change it with: chasen enable %s <domain>", name)
	}

	if len(args) == 0 {
		for _, d := range domains {
			fmt.Println(d)
		}
		return nil
	}
	// The cloud adds a third argument: the name that a CNAME record must point to.
	cname := ""
	if len(args) == 3 && validDomain(args[2]) {
		cname, args = args[2], args[:2]
	}
	if len(args) != 2 || (args[0] != "add" && args[0] != "rm") {
		return errors.New("usage: chasen domains [add|rm <domain>]")
	}

	domain := strings.ToLower(args[1])
	i := slices.Index(domains, domain)
	if args[0] == "add" {
		if !validDomain(domain) {
			return fmt.Errorf("invalid domain %q", domain)
		}
		if i >= 0 {
			return nil
		}
		domains = append(domains, domain)
	} else {
		if i == 0 {
			return fmt.Errorf("%s is the default domain of the app", domain)
		}
		if i < 0 {
			return nil
		}
		domains = slices.Delete(domains, i, i+1)
	}

	app.Domain = strings.Join(domains, ",")
	app.Env["BASE_URL"] = "https://" + primaryDomain(domains)
	if err := apply(name, app); err != nil {
		return err
	}

	if args[0] == "add" && cname != "" {
		fmt.Printf("Added %s. Point a CNAME record for it to %s\n", domain, cname)
		fmt.Println("The certificate comes a few minutes after DNS resolves.")
	} else if args[0] == "add" {
		fmt.Printf("Added %s. Point an A record for it to this server.\n", domain)
		fmt.Println("The certificate comes on the first HTTPS request after DNS resolves.")
	} else {
		fmt.Printf("Removed %s.\n", domain)
	}
	return nil
}

func serverStatus(name string) error {
	app, err := loadApp(name)
	if err != nil {
		return err
	}
	state, _ := docker("ps", "--filter", "name=^"+name+"(-next)?$", "--format", "{{.Status}}")
	if state == "" {
		state = "not running"
	}
	_, version, _ := strings.Cut(app.Image, ":")

	fmt.Printf("App:      %s\n", name)
	fmt.Printf("Version:  %s\n", version)
	fmt.Printf("State:    %s\n", state)
	for _, d := range strings.Split(app.Domain, ",") {
		fmt.Printf("URL:      %s\n", link(d))
	}
	last := "none"
	if stamps := localBackups(name); len(stamps) > 0 {
		last = stamps[0]
	}
	fmt.Printf("Backup:   %s\n", last)
	replica := "off"
	if replicaPID() != 0 {
		replica = "live"
	}
	fmt.Printf("Replica:  %s\n", replica)
	return nil
}

func serverList() error {
	apps, err := matcha.ListAppsFrom(appsPath())
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tDOMAINS")
	for _, name := range matcha.ListAppsSorted(apps) {
		_, version, _ := strings.Cut(apps[name].Image, ":")
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, version, strings.ReplaceAll(apps[name].Domain, ",", " "))
	}
	return w.Flush()
}

func serverRemove(name string) error {
	app, err := loadApp(name)
	if err != nil {
		return err
	}
	m := engine(name, app)
	if err := m.RemoveFromProxy(); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
	m.StopApp()
	if err := matcha.RemoveAppFrom(appsPath(), name); err != nil {
		return err
	}
	forgetSettings(name)
	fmt.Printf("Removed %s. The data and the backups stay in %s\n", name, appDir(name))
	return nil
}

// serverRun runs one command in the container of an app, with the env and
// the storage of the app: a migration, a rake task, a query. The words go to
// docker as they are, with no shell on the way, and the command gets no
// input. Its exit code is the exit code of this command.
func serverRun(name string, words []string) error {
	if len(words) == 0 {
		return errors.New("usage: chasen run <command> [arguments]. For example: chasen run bin/rails db:migrate")
	}
	// After the name of the container, docker takes every word as the
	// command. A first word like an option is a mistake, not a command.
	if strings.HasPrefix(words[0], "-") {
		return fmt.Errorf("%q is not a command. For shell syntax: chasen run sh -c \"...\"", words[0])
	}
	if _, err := loadApp(name); err != nil {
		return err
	}
	container, err := activeContainer(name)
	if err != nil {
		return err
	}
	cmd := exec.Command("docker", append([]string{"exec", container}, words...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	var failed *exec.ExitError
	if errors.As(err, &failed) {
		os.Exit(failed.ExitCode())
	}
	return err
}
