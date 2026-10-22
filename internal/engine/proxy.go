package engine

import (
	"fmt"
	"strings"
)

// proxyDeployArgs builds the docker exec args for kamal-proxy deploy.
func (m *Engine) proxyDeployArgs(domain string, targetContainer string) []string {
	serviceName := m.config.Name
	proxyContainer := m.ProxyContainerName()
	target := fmt.Sprintf("%s:%d", targetContainer, m.config.AppPort)

	args := []string{
		"exec", proxyContainer, "kamal-proxy", "deploy", serviceName,
		"--target", target,
	}

	if domain == "localhost" {
		// Bare localhost: skip --host (catches all requests) and --tls
	} else if isLocalhost(domain) {
		// Localhost subdomains (e.g., app.localhost): set --host for routing, skip --tls
		args = append(args, "--host", domain)
	} else {
		args = append(args, "--host", domain, "--tls")
	}

	args = append(args, "--health-check-path", m.config.HealthPath)

	if m.config.HealthTimeout > 0 && m.config.HealthTimeout != 30 {
		args = append(args, "--deploy-timeout", fmt.Sprintf("%ds", m.config.HealthTimeout))
	}

	// Always forward X-Forwarded-* headers so the app gets the real client IP.
	// When --tls is set, kamal-proxy defaults to stripping these headers.
	// Behind Cloudflare (or any reverse proxy), these headers carry the real IP.
	args = append(args, "--forward-headers")

	return args
}

// deployToProxy registers the app container with kamal-proxy.
func (m *Engine) deployToProxy(domain string, targetContainer string) error {
	args := m.proxyDeployArgs(domain, targetContainer)
	_, err := m.runDocker(args...)
	if err != nil {
		return fmt.Errorf("kamal-proxy deploy failed: %w", err)
	}
	return nil
}

// RemoveFromProxy removes the service from kamal-proxy.
func (m *Engine) RemoveFromProxy() error {
	proxyContainer := m.ProxyContainerName()
	_, err := m.runDocker("exec", proxyContainer, "kamal-proxy", "remove", m.config.Name)
	return err
}

// isLocalhost checks if domain is localhost.
func isLocalhost(domain string) bool {
	domain = strings.ToLower(domain)
	return domain == "localhost" ||
		strings.HasPrefix(domain, "localhost:") ||
		strings.HasSuffix(domain, ".localhost") ||
		domain == "127.0.0.1" ||
		strings.HasPrefix(domain, "127.0.0.1:")
}
