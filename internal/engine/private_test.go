package engine

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// privateApp is a private app whose health path is a real server on
// 127.0.0.1, where the fake docker says every container is. status is the
// answer of its health path.
func privateApp(t *testing.T, status int) *Engine {
	t.Helper()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(health.Close)
	_, port, _ := net.SplitHostPort(health.Listener.Addr().String())
	appPort, _ := strconv.Atoi(port)
	healthInterval = 10 * time.Millisecond
	m := New(Config{Name: "aja", AppPort: appPort, HealthPath: "/up", HealthTimeout: 1, SkipPull: true, DataDirBase: t.TempDir()})
	m.app = &AppConfig{Image: "ghcr.io/example/aja:1.2.0", Port: appPort, Listen: "127.0.0.1:41000"}
	m.useRecord(*m.app)
	return m
}

func TestDeployPrivate(t *testing.T) {
	t.Run("publishes the port on the address, with no proxy and no route", func(t *testing.T) {
		log := fakeDocker(t, false)
		m := privateApp(t, http.StatusOK)

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		run := indexOf(got, "run -d --name aja-next")
		if run < 0 || !strings.Contains(got[run], "-p 127.0.0.1:41000:"+strconv.Itoa(m.config.AppPort)) {
			t.Errorf("want the port on 127.0.0.1:41000, got calls:\n%s", strings.Join(got, "\n"))
		}
		if indexOf(got, "exec matcha-proxy") >= 0 || indexOf(got, "run -d --name matcha-proxy") >= 0 || indexOf(got, "ps -q --filter name=^matcha-proxy$") >= 0 {
			t.Errorf("a private app touched the proxy:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("stops the old container before the new one starts, and removes it after", func(t *testing.T) {
		log := fakeDocker(t, false)
		t.Setenv("OLD_RUNNING", "1")
		m := privateApp(t, http.StatusOK)

		err := m.deploy()

		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		got := calls(t, log)
		stop, run, rm := indexOf(got, "stop aja"), indexOf(got, "run -d --name aja-next"), slices.Index(got, "rm -f aja")
		if stop < 0 || run < stop || rm < run {
			t.Errorf("want stop aja, then the new container, then rm aja, got calls:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("starts the old container again when the new one does not answer", func(t *testing.T) {
		log := fakeDocker(t, false)
		t.Setenv("OLD_RUNNING", "1")
		m := privateApp(t, http.StatusServiceUnavailable)

		err := m.deploy()

		var failed *UnhealthyError
		if !errors.As(err, &failed) || !strings.Contains(err.Error(), "503") {
			t.Fatalf("deploy = %v, want the health check to fail with the answer", err)
		}
		got := calls(t, log)
		if rm, start := indexOf(got, "rm -f aja-next"), indexOf(got, "start aja"); rm < 0 || start < rm {
			t.Errorf("want the new container removed, then the old one started, got calls:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("starts the old container again when the new one cannot start", func(t *testing.T) {
		log := fakeDocker(t, false)
		t.Setenv("OLD_RUNNING", "1")
		t.Setenv("FAIL_RUN", "1")
		m := privateApp(t, http.StatusOK)

		err := m.deploy()

		if err == nil || !strings.Contains(err.Error(), "cannot start aja on 127.0.0.1:41000") {
			t.Fatalf("deploy = %v, want the address in the error", err)
		}
		if indexOf(calls(t, log), "start aja") < 0 {
			t.Error("want the old container started again")
		}
	})

	t.Run("a first deploy that fails starts nothing", func(t *testing.T) {
		log := fakeDocker(t, false)
		m := privateApp(t, http.StatusServiceUnavailable)

		err := m.deploy()

		if err == nil {
			t.Fatal("want the deploy to fail")
		}
		if indexOf(calls(t, log), "start aja") >= 0 {
			t.Error("started a container that never ran")
		}
	})
}
