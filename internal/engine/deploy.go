package engine

import (
	"fmt"
)

func (m *Engine) deploy() error {
	if err := m.createNetwork(); err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	if !m.isRunning(m.ProxyContainerName()) {
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
