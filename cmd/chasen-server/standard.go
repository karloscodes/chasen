package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// The standard (STANDARD.md). The image says which port it serves and where it
// keeps its data. chasen.yml can override both. These are the values for an
// image that says nothing.
const (
	defaultPort          = 8080
	defaultHealth        = "/up"
	defaultHealthTimeout = 30
)

// The default storage. Rails keeps its files in /rails/storage, so the same
// directory is at both paths.
var defaultVolumes = []string{"/storage", "/rails/storage"}

// shape is how an app meets the standard: the result of the image and the overrides.
type shape struct {
	Port          int
	PortFrom      string // where the port comes from, for messages
	Health        string
	HealthTimeout int
	Volumes       []string
}

var (
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	healthRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
)

// readSettings reads the settings that come with a deploy, a check, or a
// restart: the first line of the input, as JSON. What follows the line stays
// in the reader. An empty input gives ok = false.
func readSettings(in *bufio.Reader) (settings protocol.Settings, ok bool, err error) {
	line, err := in.ReadBytes('\n')
	if len(bytes.TrimSpace(line)) == 0 {
		return settings, false, nil
	}
	if len(line) > 1<<20 {
		return settings, false, errors.New("the settings are too large")
	}
	if err := json.Unmarshal(line, &settings); err != nil {
		return settings, false, fmt.Errorf("the settings are not valid: %w. Is the chasen CLI as new as the server?", err)
	}
	reserved := standardEnv(nil, "", "", shape{Volumes: defaultVolumes})
	for key := range settings.Env {
		if !envKeyRe.MatchString(key) {
			return settings, false, fmt.Errorf("invalid env name %q", key)
		}
		if _, ok := reserved[key]; ok {
			return settings, false, fmt.Errorf("chasen sets %s. Remove it from chasen.yml", key)
		}
	}
	if settings.Image != "" && !imageRe.MatchString(settings.Image) {
		return settings, false, fmt.Errorf("invalid image %q: use the form ghcr.io/you/app:tag", settings.Image)
	}
	if settings.Port < 0 || settings.Port > 65535 {
		return settings, false, fmt.Errorf("invalid port %d", settings.Port)
	}
	if settings.Health != "" && !healthRe.MatchString(settings.Health) {
		return settings, false, fmt.Errorf("invalid health path %q: it starts with / and has no query", settings.Health)
	}
	if settings.HealthTimeout < 0 || settings.HealthTimeout > 900 {
		return settings, false, fmt.Errorf("invalid health_timeout %d: use 1 to 900 seconds", settings.HealthTimeout)
	}
	if err := checkVolumes(settings.Volumes); err != nil {
		return settings, false, err
	}

	return settings, true, nil
}

// saveSettings keeps the settings of an app for its next restart. The login
// of the registry is for one pull only: the server does not keep it.
func saveSettings(name string, settings protocol.Settings) error {
	settings.Registry = nil
	data, _ := json.Marshal(settings)
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO app_settings (app, settings) VALUES (?, ?) ON CONFLICT (app) DO UPDATE SET settings = excluded.settings", name, string(data))
	return err
}

// checkVolumes refuses paths the engine cannot keep apart. The engine maps a
// volume to /var/matcha/<app>/<last part of the path>.
func checkVolumes(volumes []string) error {
	seen := map[string]string{}
	for _, v := range volumes {
		if !path.IsAbs(v) || path.Clean(v) != v || v == "/" {
			return fmt.Errorf("invalid volume %q: use an absolute path like /app/storage", v)
		}
		base := path.Base(v)
		if base == "backups" || strings.HasPrefix(base, "pre-restore-") || strings.HasSuffix(base, "-litestream") {
			return fmt.Errorf("the volume name %q is reserved", base)
		}
		if other, ok := seen[base]; ok {
			return fmt.Errorf("the volumes %s and %s end in the same name. Rename one", other, v)
		}
		seen[base] = v
	}
	return nil
}

// loadSettings returns the settings that the last deploy of an app saved, or
// none. An app from before the settings were in the database has them in a
// file under /etc/chasen/env.
func loadSettings(name string) (protocol.Settings, error) {
	var settings protocol.Settings
	db, err := openServerDB()
	if err != nil {
		return settings, err
	}
	defer db.Close()
	var data []byte
	err = db.QueryRow("SELECT settings FROM app_settings WHERE app = ?", name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		if data, err = os.ReadFile(envPath(name)); errors.Is(err, fs.ErrNotExist) {
			return settings, nil
		}
	}
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, fmt.Errorf("the saved settings of %s: %w", name, err)
	}
	return settings, nil
}

// forgetSettings removes the saved settings of an app.
func forgetSettings(name string) {
	if db, err := openServerDB(); err == nil {
		db.Exec("DELETE FROM app_settings WHERE app = ?", name)
		db.Close()
	}
	os.Remove(envPath(name))
}

// imageDeclares returns the TCP ports and the volumes that the image declares
// with EXPOSE and VOLUME.
func imageDeclares(image string) (ports []int, volumes []string, err error) {
	out, err := docker("image", "inspect", "-f", "{{json .Config.ExposedPorts}}\n{{json .Config.Volumes}}", image)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read the image %s: %s", image, out)
	}
	exposed, declared, _ := strings.Cut(out, "\n")
	var portSet, volumeSet map[string]struct{}
	json.Unmarshal([]byte(exposed), &portSet)
	json.Unmarshal([]byte(declared), &volumeSet)
	for key := range portSet {
		number, protocol, _ := strings.Cut(key, "/")
		if n, err := strconv.Atoi(number); err == nil && protocol == "tcp" {
			ports = append(ports, n)
		}
	}
	for v := range volumeSet {
		volumes = append(volumes, v)
	}
	slices.Sort(ports)
	slices.Sort(volumes)
	return ports, volumes, nil
}

// appShape decides the port, the health path, and the volumes of an app:
// chasen.yml first, then what the image declares, then the defaults.
func appShape(image string, settings protocol.Settings) (shape, error) {
	ports, volumes, err := imageDeclares(image)
	if err != nil {
		return shape{}, err
	}
	s := shape{
		Port: settings.Port, PortFrom: "chasen.yml",
		Health:        settings.Health,
		HealthTimeout: settings.HealthTimeout,
		Volumes:       settings.Volumes,
	}
	switch {
	case s.Port != 0:
	case len(ports) == 0:
		s.Port, s.PortFrom = defaultPort, "the default, the image has no EXPOSE"
	case len(ports) == 1 || slices.Contains(ports, 80):
		s.Port = ports[0]
		if slices.Contains(ports, 80) {
			s.Port = 80
		}
		s.PortFrom = "EXPOSE in the image"
	default:
		return s, fmt.Errorf("the image declares the ports %v. Say which one serves HTTP: add `port:` to chasen.yml", ports)
	}
	if s.Health == "" {
		s.Health = defaultHealth
	}
	if s.HealthTimeout == 0 {
		s.HealthTimeout = defaultHealthTimeout
	}
	if len(s.Volumes) == 0 {
		// The image can declare volumes that the engine cannot keep apart. Then the override is needed.
		if s.Volumes = volumes; checkVolumes(volumes) != nil {
			return s, fmt.Errorf("the image declares the volumes %v. %w. List them with `volumes:` in chasen.yml", volumes, checkVolumes(volumes))
		}
	}
	if len(s.Volumes) == 0 {
		s.Volumes = defaultVolumes
	}
	return s, nil
}

// standardEnv is the env that Chasen sets in every container. An app cannot
// override these names.
func standardEnv(domains []string, version, privateKey string, s shape) map[string]string {
	return map[string]string{
		"PORT":            strconv.Itoa(s.Port),
		"BASE_URL":        "https://" + primaryDomain(domains),
		"SECRET_KEY_BASE": privateKey,
		"PRIVATE_KEY":     privateKey,
		"STORAGE_DIR":     s.Volumes[0],
		"DATABASE_PATH":   s.Volumes[0] + "/db.sqlite3",
		"APP_VERSION":     version,
		"APP_ENV":         "production",
	}
}

// appEnv is the environment of a container: what the client sent, then what
// Chasen sets.
func appEnv(settings protocol.Settings, domains []string, version, privateKey string, s shape) map[string]string {
	env := map[string]string{}
	for k, v := range settings.Env {
		env[k] = v
	}
	for k, v := range standardEnv(domains, version, privateKey, s) {
		env[k] = v
	}
	return env
}
