package engine

import (
	"crypto/rand"
	"encoding/hex"
)

// record returns the record of the app: the one the caller gave to DeployApp,
// or the one in the YAML config.
func (m *Engine) record() (AppConfig, error) {
	if m.app != nil {
		return *m.app, nil
	}
	return LoadAppFrom(m.configPath(), m.config.Name)
}

// useRecord makes this instance deploy what a record says.
func (m *Engine) useRecord(app AppConfig) {
	if app.Image != "" {
		m.config.AppImage = app.Image
	}
	if app.Domain != "" {
		m.domain = app.Domain
	}
	if app.Port != 0 {
		m.config.AppPort = app.Port
	}
	if app.HealthPath != "" {
		m.config.HealthPath = app.HealthPath
	}
	m.config.Volumes = app.Volumes
	if app.HealthTimeout > 0 {
		m.config.HealthTimeout = app.HealthTimeout
	}
}

// GeneratePrivateKey creates a secure random key.
func GeneratePrivateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}
