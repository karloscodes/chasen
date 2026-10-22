package engine

import (
	"testing"
)

func TestNew(t *testing.T) {
	t.Run("applies defaults", func(t *testing.T) {
		m := New(Config{
			Name:     "testapp",
			AppImage: "test/image:latest",
		})

		if m.config.ProxyImage != "basecamp/kamal-proxy:latest" {
			t.Errorf("ProxyImage = %q, want basecamp/kamal-proxy:latest", m.config.ProxyImage)
		}

		if m.config.HealthPath != "/up" {
			t.Errorf("HealthPath = %q, want /up", m.config.HealthPath)
		}

		if m.config.AppPort != 8080 {
			t.Errorf("AppPort = %d, want 8080", m.config.AppPort)
		}
	})

	t.Run("respects custom values", func(t *testing.T) {
		m := New(Config{
			Name:       "testapp",
			AppImage:   "test/image:latest",
			HealthPath: "/healthz",
			AppPort:    3000,
		})

		if m.config.HealthPath != "/healthz" {
			t.Errorf("HealthPath = %q, want /healthz", m.config.HealthPath)
		}

		if m.config.AppPort != 3000 {
			t.Errorf("AppPort = %d, want 3000", m.config.AppPort)
		}
	})
}

func TestEngineDataDir(t *testing.T) {
	m := New(Config{Name: "fusionaly", AppImage: "test:latest"})

	got := m.DataDir()

	if got != "/var/matcha/fusionaly" {
		t.Errorf("DataDir() = %q, want /var/matcha/fusionaly", got)
	}
}

func TestEnvPrefix(t *testing.T) {
	m := New(Config{Name: "fusionaly", AppImage: "test:latest"})

	got := m.EnvPrefix()

	if got != "FUSIONALY" {
		t.Errorf("EnvPrefix() = %q, want FUSIONALY", got)
	}
}

func TestNetworkName(t *testing.T) {
	m := New(Config{Name: "fusionaly", AppImage: "test:latest"})

	got := m.NetworkName()

	if got != "matcha-network" {
		t.Errorf("NetworkName() = %q, want matcha-network", got)
	}
}

func TestProxyContainerName(t *testing.T) {
	m := New(Config{Name: "fusionaly", AppImage: "test:latest"})

	got := m.ProxyContainerName()

	if got != "matcha-proxy" {
		t.Errorf("ProxyContainerName() = %q, want matcha-proxy", got)
	}
}

func TestFindActiveContainer(t *testing.T) {
	m := New(Config{Name: "myapp", AppImage: "test:latest"})

	t.Run("returns base name by default", func(t *testing.T) {
		got := m.findActiveContainer()

		if got != "myapp" {
			t.Errorf("findActiveContainer() = %q, want myapp", got)
		}
	})
}

func TestNextContainerName(t *testing.T) {
	m := New(Config{Name: "myapp", AppImage: "test:latest"})

	t.Run("base returns next", func(t *testing.T) {
		got := m.nextContainerName("myapp")

		if got != "myapp-next" {
			t.Errorf("nextContainerName(myapp) = %q, want myapp-next", got)
		}
	})

	t.Run("next returns base", func(t *testing.T) {
		got := m.nextContainerName("myapp-next")

		if got != "myapp" {
			t.Errorf("nextContainerName(myapp-next) = %q, want myapp", got)
		}
	})
}
