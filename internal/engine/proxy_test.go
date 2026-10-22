package engine

import "testing"

func TestProxyDeployArgs(t *testing.T) {
	m := New(Config{
		Name:     "testapp",
		AppImage: "test:latest",
		AppPort:  8080,
	})

	args := m.proxyDeployArgs("app.example.com", "testapp-next")

	expected := []string{
		"exec", "matcha-proxy", "kamal-proxy", "deploy", "testapp",
		"--target", "testapp-next:8080",
		"--host", "app.example.com",
		"--tls",
		"--health-check-path", "/up",
		"--forward-headers",
	}

	if len(args) != len(expected) {
		t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
	}

	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("arg[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestProxyDeployArgsLocalhost(t *testing.T) {
	m := New(Config{
		Name:     "testapp",
		AppImage: "test:latest",
		AppPort:  3000,
	})

	args := m.proxyDeployArgs("localhost", "testapp")

	for _, arg := range args {
		if arg == "--tls" {
			t.Error("should not include --tls for localhost")
		}
		if arg == "--host" {
			t.Error("should not include --host for localhost")
		}
	}

	found := false
	for i, arg := range args {
		if arg == "--target" && i+1 < len(args) && args[i+1] == "testapp:3000" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing --target testapp:3000 in %v", args)
	}

	forwardHeaders := false
	for _, arg := range args {
		if arg == "--forward-headers" {
			forwardHeaders = true
		}
	}
	if !forwardHeaders {
		t.Error("should include --forward-headers for localhost")
	}
}
