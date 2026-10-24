package engine

import (
	"errors"
	"fmt"
	"time"
)

func (m *Engine) deploy() error {
	if err := m.createNetwork(); err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	// A private app does not need the proxy.
	if m.listen == "" && !m.isRunning(m.ProxyContainerName()) {
		if err := m.deployProxy(); err != nil {
			return fmt.Errorf("failed to deploy proxy: %w", err)
		}
	}

	// Deploy the image config.yml names, not whatever copy this host happens to have.
	// A failed pull is not fatal: the image may exist only on this host.
	// A caller that already put the image on this host skips the pull.
	if !m.config.SkipPull {
		if err := m.pullImage(m.config.AppImage); err != nil {
			printWarn("Could not pull %s, deploying the local copy: %v", m.config.AppImage, err)
		}
	}

	if m.listen != "" {
		return m.deployPrivate()
	}

	// Zero-downtime deploy:
	// 1. Find current container, pick alternate name for new one
	// 2. Start new container (old still serves traffic)
	// 3. Register new with proxy (health-checks, then switches traffic)
	// 4. Remove old container
	oldContainer := m.findActiveContainer()
	newContainer := m.nextContainerName(oldContainer)

	if err := m.deployApp(newContainer); err != nil {
		return fmt.Errorf("failed to deploy app: %w", err)
	}

	if err := m.deployToProxy(m.domain, newContainer); err != nil {
		// New container failed health check — clean it up, old is still running.
		// Keep what it left first: once it is removed, its logs are gone.
		failed := m.unhealthy(newContainer, err)
		m.stopAndRemove(newContainer)
		return failed
	}

	// Traffic has switched — safe to remove old container
	if oldContainer != newContainer {
		m.stopAndRemove(oldContainer)
	}

	return nil
}

// deployPrivate swaps the container of a private app. It has no proxy to
// move the traffic: its port is on an address of the server, and two
// containers cannot have one port. So the old container stops first, and the
// app does not answer for the seconds the new one needs to start. When the new
// one does not start or does not answer 200 on the health path, the old one
// starts again.
func (m *Engine) deployPrivate() error {
	oldContainer := m.findActiveContainer()
	newContainer := m.nextContainerName(oldContainer)
	wasRunning := m.isRunning(oldContainer)
	if wasRunning {
		m.runDocker("stop", oldContainer)
	}
	back := func() {
		m.stopAndRemove(newContainer)
		if wasRunning {
			m.runDocker("start", oldContainer)
		}
	}

	if err := m.deployApp(newContainer); err != nil {
		back()
		return fmt.Errorf("cannot start %s on %s: %w", m.config.Name, m.listen, err)
	}
	if _, problem := WaitHealthy(newContainer, m.config.AppPort, m.config.HealthPath, time.Duration(m.config.HealthTimeout)*time.Second); problem != "" {
		failed := m.unhealthy(newContainer, errors.New(problem))
		back()
		return failed
	}

	if oldContainer != newContainer {
		m.stopAndRemove(oldContainer)
	}
	return nil
}
