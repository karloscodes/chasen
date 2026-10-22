// Package engine runs the apps of chasen-server on Docker: the network and
// the proxy, the container swap, and the health check. It began as the matcha
// library (github.com/karloscodes/matcha, v0.17.0, by the same author), and
// moved here with only the parts that chasen-server uses. The names on the
// server stay those of matcha: the matcha-proxy container, /var/matcha, and
// /etc/matcha/config.yml, which adopt reads.
package engine

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Config defines how the engine deploys an app.
type Config struct {
	// Required
	Name     string // "fusionaly" → env prefix FUSIONALY_, container names, etc.
	AppImage string // "karloscodes/fusionaly:latest"

	// Optional with defaults
	ProxyImage string // default: basecamp/kamal-proxy:latest
	HealthPath string // default: /up
	AppPort    int    // default: 8080

	// Feature flags
	SkipPull bool // the caller put the image on this host: a deploy does not pull it

	// Custom configuration
	Volumes       []string // Container paths to mount (e.g., /app/storage)
	HealthTimeout int      // Health check timeout in seconds (default: 30)

	// Internal: override paths (for testing)
	ConfigPath  string
	DataDirBase string
}

// Engine deploys one app: its container, the proxy route, and the health check.
type Engine struct {
	config Config
	// Temporary state during installation
	domain string
	// The record a caller gave to DeployApp. With it, no config file is read.
	app *AppConfig
}

// New returns the engine of one app, with defaults for what cfg leaves empty.
func New(cfg Config) *Engine {
	// Apply defaults
	if cfg.ProxyImage == "" {
		cfg.ProxyImage = "basecamp/kamal-proxy:latest"
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = "/up"
	}
	if cfg.AppPort == 0 {
		cfg.AppPort = 8080
	}
	if cfg.HealthTimeout == 0 {
		cfg.HealthTimeout = 30
	}

	return &Engine{
		config: cfg,
	}
}

// configPath returns the config file path, using override if set.
func (m *Engine) configPath() string {
	if m.config.ConfigPath != "" {
		return m.config.ConfigPath
	}
	return ConfigPath()
}

// DataDir returns the data directory for this app.
func (m *Engine) DataDir() string {
	if m.config.DataDirBase != "" {
		return m.config.DataDirBase + "/" + m.config.Name
	}
	return DataDir(m.config.Name)
}

// resolveVolumes converts container paths to docker -v args using this instance's data dir.
func (m *Engine) resolveVolumes(volumes []string) []string {
	return resolveVolumesWithBase(m.DataDir(), volumes)
}

// EnvPrefix returns the uppercase name used for environment variables.
func (m *Engine) EnvPrefix() string {
	return strings.ToUpper(m.config.Name)
}

// NetworkName returns the Docker network name.
func (m *Engine) NetworkName() string {
	return "matcha-network"
}

// ProxyContainerName returns the proxy container name.
func (m *Engine) ProxyContainerName() string {
	return "matcha-proxy"
}

// Setup installs shared infrastructure: Docker, network, and proxy.
func Setup() error {
	m := &Engine{
		config: Config{
			ProxyImage: "basecamp/kamal-proxy:latest",
		},
	}

	fmt.Println()
	fmt.Println(bold("Setting up Docker and the proxy"))
	fmt.Println()

	sp := m.StartSpinner("Docker")
	if err := m.ensureDocker(); err != nil {
		sp.Stop(false)
		return err
	}
	sp.Stop(true)

	sp = m.StartSpinner("Network")
	if err := m.createNetwork(); err != nil {
		sp.Stop(false)
		return err
	}
	sp.Stop(true)

	sp = m.StartSpinner("Proxy")
	if err := m.deployProxy(); err != nil {
		sp.Stop(false)
		return err
	}
	sp.Stop(true)

	fmt.Println()
	return nil
}

// Logs streams logs from the app container.
func (m *Engine) Logs() error {
	cmd := exec.Command("docker", "logs", "--tail", "100", "-f", m.findActiveContainer())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DeployApp deploys the app from the record the caller gives. It reads no
// config file and writes none, so a caller can keep its apps where it wants:
// in a database, for example.
func (m *Engine) DeployApp(app AppConfig) error {
	m.app = &app
	m.useRecord(app)
	return m.deploy()
}
