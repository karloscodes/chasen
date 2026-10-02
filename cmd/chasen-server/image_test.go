package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/karloscodes/chasen/protocol"
)

// The pull of an image on a real Docker. It runs with CHASEN_TEST_DOCKER=1.
func TestPullOfAnImage(t *testing.T) {
	if os.Getenv("CHASEN_TEST_DOCKER") == "" {
		t.Skip("set CHASEN_TEST_DOCKER=1: this test pulls images")
	}
	id := func(image string) string {
		out, _ := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
		return strings.TrimSpace(string(out))
	}
	const kept = "chasen.invalid/pull-test:1"
	t.Cleanup(func() { exec.Command("docker", "rmi", kept).Run() })

	t.Run("a name that another tool uses on the server stays, with the image it had", func(t *testing.T) {
		// Another tool pulled busybox and runs its apps from that name.
		if out, err := exec.Command("docker", "pull", "-q", "busybox:latest").CombinedOutput(); err != nil {
			t.Fatalf("docker pull: %s", out)
		}
		before := id("busybox:latest")

		err := fetchImage(protocol.Settings{Image: "busybox:latest"}, nil, kept)

		if err != nil {
			t.Fatal(err)
		}
		if id("busybox:latest") != before {
			t.Errorf("busybox:latest is %q now, it was %q: the pull took the name of the other tool away", id("busybox:latest"), before)
		}
		if id(kept) != before {
			t.Errorf("the image is not under the name of Chasen: %q", id(kept))
		}
	})

	t.Run("a name that only Chasen pulled does not stay", func(t *testing.T) {
		const once = "busybox:1.36.1-musl"
		if id(once) != "" {
			t.Skip(once + " is on this machine already")
		}

		err := fetchImage(protocol.Settings{Image: once}, nil, kept)

		if err != nil {
			t.Fatal(err)
		}
		if id(once) != "" {
			exec.Command("docker", "rmi", once).Run()
			t.Errorf("%s is still on the machine: only the name of Chasen should be", once)
		}
		if id(kept) == "" {
			t.Error("the image is not under the name of Chasen")
		}
	})
}
