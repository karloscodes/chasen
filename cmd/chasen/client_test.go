package main

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretValues(t *testing.T) {
	t.Run("secrets come from the secrets command, then from the environment", func(t *testing.T) {
		t.Setenv("FROM_ENV", "env-value")
		app := appFile{
			Env:            map[string]string{"LOG_LEVEL": "info"},
			Secrets:        []string{"PLAIN", "QUOTED", "FROM_ENV"},
			SecretsCommand: `printf '# comment\nPLAIN=a=b\nexport QUOTED="two words"\nUNUSED=x\n'`,
		}

		env, err := secretValues(app, app.Secrets)

		want := map[string]string{"PLAIN": "a=b", "QUOTED": "two words", "FROM_ENV": "env-value"}
		if err != nil || !maps.Equal(env, want) {
			t.Errorf("secretValues = %v, %v, want %v", env, err, want)
		}
	})

	t.Run("a missing secret stops the deploy and names the secret", func(t *testing.T) {
		app := appFile{Secrets: []string{"CHASEN_TEST_NOT_SET"}}

		_, err := secretValues(app, app.Secrets)

		if err == nil || !strings.Contains(err.Error(), "CHASEN_TEST_NOT_SET") {
			t.Errorf("secretValues error = %v, want it to name CHASEN_TEST_NOT_SET", err)
		}
	})
}

func TestServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CHASEN_URL", "")
	saved := logins{Current: "https://api.one.example.com", Tokens: map[string]string{
		"https://api.one.example.com": "token-one",
		"https://api.two.example.com": "token-two",
	}}
	if err := saved.save(); err != nil {
		t.Fatal(err)
	}

	t.Run("a command goes to the current server", func(t *testing.T) {
		creds, err := loadCredentials("")

		if err != nil || creds.URL != "https://api.one.example.com" || creds.Token != "token-one" {
			t.Errorf("loadCredentials = %+v, %v, want server one", creds, err)
		}
	})

	t.Run("chasen.yml can name the server of an app", func(t *testing.T) {
		creds, err := loadCredentials("two.example.com")

		if err != nil || creds.URL != "https://api.two.example.com" || creds.Token != "token-two" {
			t.Errorf("loadCredentials = %+v, %v, want server two", creds, err)
		}
	})

	t.Run("a server without a login is an error that says how to log in", func(t *testing.T) {
		_, err := loadCredentials("three.example.com")

		if err == nil || !strings.Contains(err.Error(), "chasen add server three.example.com") {
			t.Errorf("loadCredentials error = %v, want the command to add the server", err)
		}
	})

	t.Run("use makes another server the current one", func(t *testing.T) {
		if err := useServer([]string{"two.example.com"}); err != nil {
			t.Fatal(err)
		}

		creds, err := loadCredentials("")

		if err != nil || creds.URL != "https://api.two.example.com" {
			t.Errorf("after use, loadCredentials = %+v, %v, want server two", creds, err)
		}
	})

	t.Run("use takes the number of chasen servers, or a part of the address", func(t *testing.T) {
		for said, want := range map[string]string{"1": "https://api.one.example.com", "two": "https://api.two.example.com"} {
			var out strings.Builder

			err := chooseServer([]string{said}, strings.NewReader(""), &out)

			if creds, _ := loadCredentials(""); err != nil || creds.URL != want {
				t.Errorf("use %s = %v, now %s, want %s", said, err, creds.URL, want)
			}
		}
	})

	t.Run("use with nothing shows the list and asks", func(t *testing.T) {
		var out strings.Builder

		err := chooseServer(nil, strings.NewReader("2\n"), &out)

		if !strings.Contains(out.String(), " 2  https://api.two.example.com") || !strings.Contains(out.String(), "Use which server?") {
			t.Errorf("use showed:\n%s", out.String())
		}
		if creds, _ := loadCredentials(""); err != nil || creds.URL != "https://api.two.example.com" {
			t.Errorf("use 2 = %v, now %s", err, creds.URL)
		}
	})

	t.Run("a part that two addresses have asks for more", func(t *testing.T) {
		err := chooseServer([]string{"example"}, strings.NewReader(""), io.Discard)

		if err == nil || !strings.Contains(err.Error(), "matches https://api.one.example.com and https://api.two.example.com") {
			t.Errorf("use example = %v, want both matches named", err)
		}
	})
}

func TestOriginImage(t *testing.T) {
	images := map[string]string{
		"https://github.com/You/Shop.git\n": "ghcr.io/you/shop",
		"git@github.com:you/shop.git":       "ghcr.io/you/shop",
		"https://github.com/you/shop":       "ghcr.io/you/shop",
		"ssh://git@github.com/you/shop.git": "ghcr.io/you/shop",
		"https://gitlab.com/you/shop.git":   "",
		"":                                  "",
	}
	for origin, want := range images {
		if got := originImage(origin); got != want {
			t.Errorf("originImage(%q) = %q, want %q", origin, got, want)
		}
	}
}

func TestRegistryLogin(t *testing.T) {
	// The login that `docker login` saved: the name and the password, in base64.
	config := t.TempDir()
	os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"auths": {"ghcr.io": {"auth": "eW91OmZyb20tZG9ja2Vy"}}}`), 0600)
	t.Setenv("DOCKER_CONFIG", config)
	t.Setenv("GHCR_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("an image on ghcr.io uses the login of docker", func(t *testing.T) {
		login, _, err := registryLogin(appFile{Image: "ghcr.io/you/shop:abc"})

		if err != nil || login == nil || login.Username != "you" || login.Password != "from-docker" {
			t.Errorf("registryLogin = %+v, %v, want the login of docker", login, err)
		}
	})

	t.Run("in CI, the token of the job wins", func(t *testing.T) {
		t.Setenv("GHCR_TOKEN", "from-ci")

		login, _, err := registryLogin(appFile{Image: "ghcr.io/you/shop:abc"})

		if err != nil || login == nil || login.Username != "you" || login.Password != "from-ci" {
			t.Errorf("registryLogin = %+v, %v, want the token of the job", login, err)
		}
	})

	t.Run("chasen.yml wins over everything", func(t *testing.T) {
		t.Setenv("HUB_TOKEN", "from-yml")
		app := appFile{Image: "ghcr.io/you/shop:abc"}
		app.Registry.Username, app.Registry.Password = "bot", "HUB_TOKEN"

		login, _, err := registryLogin(app)

		if err != nil || login == nil || login.Username != "bot" || login.Password != "from-yml" {
			t.Errorf("registryLogin = %+v, %v, want the login of chasen.yml", login, err)
		}
	})

	t.Run("a registry with no login anywhere gets none", func(t *testing.T) {
		login, _, err := registryLogin(appFile{Image: "registry.example.com/you/shop:abc"})

		if err != nil || login != nil {
			t.Errorf("registryLogin = %+v, %v, want no login and no error", login, err)
		}
	})
}

func TestReport(t *testing.T) {
	t.Setenv("CHASEN_TOKEN", "secret-token")

	page := reportURL()

	if !strings.HasPrefix(page, "https://github.com/karloscodes/chasen/issues/new?body=") {
		t.Errorf("the report goes to %s", page)
	}
	if !strings.Contains(page, "chasen+"+version) || strings.Contains(page, "secret-token") {
		t.Errorf("the report must have the version and no token: %s", page)
	}
}

func TestBucket(t *testing.T) {
	login := func(t *testing.T) {
		t.Helper()
		creds := mockServer(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("CHASEN_URL", creds.URL)
		t.Setenv("CHASEN_TOKEN", creds.Token)
		t.Chdir(t.TempDir())
	}
	// output runs one command of the CLI and returns what it prints.
	output := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		read, write, _ := os.Pipe()
		stdout := os.Stdout
		os.Stdout = write
		err := runClient(args)
		write.Close()
		os.Stdout = stdout
		printed, _ := io.ReadAll(read)
		return string(printed), err
	}

	t.Run("the bucket of the server is set from this computer, and the secret goes in the request", func(t *testing.T) {
		login(t)
		t.Setenv("S3_SECRET_ACCESS_KEY", "the-secret")

		set, err := output(t, "bucket", "--endpoint", "https://s3.example.com", "--name", "backups", "--access-key-id", "the-id")

		shown, _ := output(t, "bucket")
		if err != nil || !strings.Contains(set, "Backups now go to the bucket backups") || !strings.Contains(shown, "Bucket backups") {
			t.Errorf("set = %q (%v), shown = %q", set, err, shown)
		}
	})

	t.Run("with no secret and no terminal, nothing is sent", func(t *testing.T) {
		login(t)
		t.Setenv("S3_SECRET_ACCESS_KEY", "")

		_, err := output(t, "bucket", "--endpoint", "https://s3.example.com", "--name", "backups", "--access-key-id", "the-id")

		shown, _ := output(t, "bucket")
		if err == nil || !strings.Contains(err.Error(), "S3_SECRET_ACCESS_KEY") || !strings.Contains(shown, "No bucket") {
			t.Errorf("err = %v, shown = %q, want an error and no bucket", err, shown)
		}
	})
}
