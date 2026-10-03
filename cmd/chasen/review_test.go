package main

import (
	"os"
	"strings"
	"testing"
)

// review prints the review of a chasen.yml the way a deploy does.
func review(found []finding) (output string, stopped bool) {
	var out strings.Builder
	err := printReview(&out, found)
	return out.String(), err != nil
}

func TestReviewOfTheAppFile(t *testing.T) {
	t.Run("a chasen.yml that follows the standard gets no word", func(t *testing.T) {
		app := appFile{
			Name: "shop", Image: "ghcr.io/you/shop",
			Env: map[string]string{"LOG_LEVEL": "info", "VAPID_PUBLIC_KEY": "BPk3"}, Secrets: []string{"STRIPE_KEY"},
			Port: 3000, Health: "/_health", HealthTimeout: 90, Volumes: []string{"/app/storage", "/app/logs"},
		}

		output, stopped := review(reviewAppFile(app, true, ""))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("what no server accepts is an error: the deploy stops", func(t *testing.T) {
		app := appFile{Name: "shop", Env: map[string]string{"PORT": "3000"}, Port: 70000, Health: "up", Volumes: []string{"/a/data", "/b/data"}}

		output, stopped := review(reviewAppFile(app, true, ""))

		want := `Error: chasen.yml: chasen sets PORT itself. Remove it from chasen.yml
  Chasen gives this variable to every app, with its own value. The app reads it; chasen.yml cannot change it.
  https://chasenhq.com/docs/standard/#4-environment

Error: chasen.yml: invalid port 70000: use 1 to 65535
  https://chasenhq.com/docs/deploy/#chasenyml

Error: chasen.yml: invalid health path "up": it starts with / and has no query
  https://chasenhq.com/docs/deploy/#chasenyml

Error: chasen.yml: the volumes /a/data and /b/data end in the same name. Rename one
  https://chasenhq.com/docs/deploy/#chasenyml

`
		if output != want || !stopped {
			t.Errorf("stopped = %v, output:\n%s\nwant:\n%s", stopped, output, want)
		}
	})

	t.Run("a secret that has a name of the standard is an error too", func(t *testing.T) {
		app := appFile{Name: "shop", Secrets: []string{"DATABASE_PATH"}}

		output, stopped := review(reviewAppFile(app, true, ""))

		if !stopped || !strings.Contains(output, "Error: chasen.yml: chasen sets DATABASE_PATH itself") {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("the secret key of the app can come as a secret: then the owner keeps it", func(t *testing.T) {
		app := appFile{Name: "shop", Secrets: []string{"SECRET_KEY_BASE"}}

		output, stopped := review(reviewAppFile(app, true, ""))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("a directory name that cannot be an app name says how to set one", func(t *testing.T) {
		app := appFile{Name: "my_shop"}

		output, stopped := review(reviewAppFile(app, true, ""))

		want := "Error: invalid app name \"my_shop\": use lowercase letters, digits, and hyphens\n" +
			"  The name is a part of the address of the app. Without `name:` in chasen.yml, it is the name of this directory.\n" +
			"  https://chasenhq.com/docs/deploy/#chasenyml\n\n"
		if output != want || !stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("a registry token in the file stops the deploy", func(t *testing.T) {
		app := appFile{Name: "shop"}
		app.Registry.Username, app.Registry.Password = "you", "ghp_0123456789abcdef"

		output, stopped := review(reviewAppFile(app, true, ""))

		if !stopped || !strings.Contains(output, "Error: chasen.yml: registry.password holds a token") || !strings.Contains(output, "password: GHCR_TOKEN") {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
		if strings.Contains(output, "ghp_0123456789abcdef") {
			t.Error("the review printed the token")
		}
	})

	t.Run("the name of a secret as the registry password is right", func(t *testing.T) {
		app := appFile{Name: "shop"}
		app.Registry.Username, app.Registry.Password = "you", "GHCR_TOKEN"

		output, stopped := review(reviewAppFile(app, true, ""))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("a value that looks like a secret in env is a warning: the deploy goes on", func(t *testing.T) {
		app := appFile{Name: "shop", Env: map[string]string{"STRIPE_SECRET_KEY": "sk_live_1"}}

		output, stopped := review(reviewAppFile(app, true, ""))

		want := "Warning: chasen.yml: the value of STRIPE_SECRET_KEY is in env, as plain text, and chasen.yml goes into git\n" +
			"  If it is a secret, add the name to `secrets:` and take the value out of the file.\n" +
			"  https://chasenhq.com/docs/standard/#5-secrets\n\n" +
			"1 warning. A warning does not stop the deploy.\n\n"
		if output != want || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("an image with a tag next to a Dockerfile is a warning: nothing is built", func(t *testing.T) {
		app := appFile{Name: "shop", Image: "ghcr.io/you/shop:1.2.0"}

		output, stopped := review(reviewAppFile(app, true, ""))

		if stopped || !strings.Contains(output, "Warning: chasen.yml: image names the tag 1.2.0, so chasen does not build the Dockerfile of this directory") {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("the same image with no Dockerfile here is right", func(t *testing.T) {
		app := appFile{Name: "shop", Image: "ghcr.io/you/shop:1.2.0"}

		output, stopped := review(reviewAppFile(app, false, ""))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("errors come before warnings", func(t *testing.T) {
		app := appFile{Name: "shop", Env: map[string]string{"API_TOKEN": "abc"}, Port: -1}

		output, stopped := review(reviewAppFile(app, true, ""))

		warning, problem := strings.Index(output, "Warning:"), strings.Index(output, "Error:")
		if !stopped || problem != 0 || warning < problem {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})
}

func TestReviewOfTheImage(t *testing.T) {
	t.Run("an image that says its port, its storage, and its command gets no word", func(t *testing.T) {
		image := imageFacts{Ports: []int{3000}, Volumes: []string{"/app/storage"}, Command: true}

		output, stopped := review(reviewImage(appFile{}, image))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("an image that says nothing about its port or storage gets no word: the defaults apply", func(t *testing.T) {
		image := imageFacts{Command: true}

		output, stopped := review(reviewImage(appFile{}, image))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("chasen.yml answers for the image", func(t *testing.T) {
		image := imageFacts{Command: true, Ports: []int{3000, 9090}}
		app := appFile{Port: 3000, Volumes: []string{"/data"}}

		output, stopped := review(reviewImage(app, image))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("a default Rails image keeps its files where Chasen keeps them", func(t *testing.T) {
		image := imageFacts{Command: true, Ports: []int{80}}

		output, stopped := review(reviewImage(appFile{}, image))

		if output != "" || stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("several ports and none is 80 is an error: Chasen cannot pick", func(t *testing.T) {
		image := imageFacts{Command: true, Ports: []int{3000, 9090}, Volumes: []string{"/data"}}

		output, stopped := review(reviewImage(appFile{}, image))

		want := "Error: the image declares the ports [3000 9090]\n" +
			"  Say which one serves HTTP: add `port:` to chasen.yml\n" +
			"  https://chasenhq.com/docs/standard/#the-dockerfile-is-the-contract\n\n"
		if output != want || !stopped {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})

	t.Run("an image with no command is an error", func(t *testing.T) {
		image := imageFacts{Ports: []int{8080}, Volumes: []string{"/data"}}

		output, stopped := review(reviewImage(appFile{}, image))

		if !stopped || !strings.Contains(output, "Error: the image has no command to start the app") {
			t.Errorf("stopped = %v, output:\n%s", stopped, output)
		}
	})
}

func TestAppFileWithAnUnknownKey(t *testing.T) {
	t.Chdir(t.TempDir())
	os.WriteFile("chasen.yml", []byte("name: shop\nprot: 3000\n"), 0644)

	_, err := loadAppFile()

	want := "chasen.yml: line 2: unknown key `prot`\n" +
		"  The keys of chasen.yml: name, server, image, registry, env, secrets, secrets_command, port, health, health_timeout, volumes, memory, backup.\n" +
		"  https://chasenhq.com/docs/deploy/#chasenyml"
	if err == nil || err.Error() != want {
		t.Errorf("got:\n%v\nwant:\n%s", err, want)
	}
}
