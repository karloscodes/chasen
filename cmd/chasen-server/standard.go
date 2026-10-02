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
	"slices"
	"strconv"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// The standard (STANDARD.md). The rules that need no Docker are in the
// protocol package, so the CLI checks with the same ones before a deploy.
type shape = protocol.Shape

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
	if err := settings.Check(); err != nil {
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
	return protocol.ShapeOf(settings, ports, volumes)
}

// standardEnv is the env that Chasen sets in every container. An app cannot
// override these names: they are protocol.StandardEnv.
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
