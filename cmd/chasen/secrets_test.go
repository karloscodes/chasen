package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// app makes the directory of an app and goes into it. The editor of the test
// is a script: it writes what a person would type.
func secretsApp(t *testing.T) (dir string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("CHASEN_KEY", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	return dir
}

// typeInEditor makes $EDITOR a script that adds the lines to the file.
func typeInEditor(t *testing.T, lines string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '"+lines+"' >> \"$1\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)
}

func TestSecrets(t *testing.T) {
	t.Run("the first edit makes the encrypted file and its key, and the secrets come back", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=sk_live_1\nGREETING="two words"\n`)

		err := editSecrets()

		if err != nil {
			t.Fatal(err)
		}
		stored, err := storedSecrets()
		if err != nil || stored["STRIPE_KEY"] != "sk_live_1" || stored["GREETING"] != "two words" {
			t.Errorf("stored = %v (%v), want the two secrets", stored, err)
		}
		sealed, _ := os.ReadFile("chasen.secrets.enc")
		if strings.Contains(string(sealed), "sk_live_1") || !strings.HasPrefix(string(sealed), "v1:") {
			t.Errorf("chasen.secrets.enc = %q, want it encrypted", sealed)
		}
		if info, err := os.Stat("chasen.key"); err != nil || info.Mode().Perm() != 0600 {
			t.Errorf("chasen.key: %v, %v, want a file that only its owner reads", info, err)
		}
	})

	t.Run("git ignores the key and takes the encrypted file", func(t *testing.T) {
		secretsApp(t)
		exec.Command("git", "init", "-q").Run()
		os.WriteFile(".gitignore", []byte("/tmp"), 0644)
		typeInEditor(t, `TOKEN=abc\n`)

		err := editSecrets()

		if err != nil {
			t.Fatal(err)
		}
		if exec.Command("git", "check-ignore", "-q", "chasen.key").Run() != nil {
			t.Error("git does not ignore chasen.key")
		}
		if exec.Command("git", "check-ignore", "-q", "chasen.secrets.enc").Run() == nil {
			t.Error("git ignores chasen.secrets.enc: it must be in the repository")
		}
		if rules, _ := os.ReadFile(".gitignore"); string(rules) != "/tmp\nchasen.key\n" {
			t.Errorf(".gitignore = %q, want the rule from before and the key", rules)
		}
	})

	t.Run("a second edit shows the secrets of before and keeps the key", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `FIRST=1\n`)
		editSecrets()
		key, _ := os.ReadFile("chasen.key")
		typeInEditor(t, `SECOND=2\n`)

		err := editSecrets()

		stored, _ := storedSecrets()
		if err != nil || stored["FIRST"] != "1" || stored["SECOND"] != "2" {
			t.Errorf("stored = %v (%v), want both secrets", stored, err)
		}
		if after, _ := os.ReadFile("chasen.key"); string(after) != string(key) {
			t.Error("the key changed")
		}
	})

	t.Run("an edit that changes nothing leaves the file as it is", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `FIRST=1\n`)
		editSecrets()
		before, _ := os.ReadFile("chasen.secrets.enc")
		t.Setenv("EDITOR", "true")

		err := editSecrets()

		if after, _ := os.ReadFile("chasen.secrets.enc"); err != nil || string(after) != string(before) {
			t.Errorf("the file changed (%v): git would show a change where there is none", err)
		}
	})

	t.Run("an editor with a window, like VS Code, waits for the person without a flag from them", func(t *testing.T) {
		secretsApp(t)
		// This "code" does what VS Code does: with --wait it stays until the file is saved, and without it, it comes back at once.
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "code"), []byte("#!/bin/sh\n[ \"$1\" = --wait ] && printf 'TOKEN=typed-in-the-window\\n' >> \"$2\"\nexit 0\n"), 0755)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("EDITOR", "code")

		err := editSecrets()

		if stored, _ := storedSecrets(); err != nil || stored["TOKEN"] != "typed-in-the-window" {
			t.Errorf("stored = %v (%v), want what the person typed in the window", stored, err)
		}
	})

	t.Run("the command of the editor", func(t *testing.T) {
		for editor, want := range map[string]string{
			"code":                   "code --wait",
			"cursor":                 "cursor --wait",
			"/usr/local/bin/code -n": "/usr/local/bin/code -n --wait",
			"code --wait":            "code --wait",
			"subl -w":                "subl -w",
			"vim":                    "vim",
			"nano --restricted":      "nano --restricted",
			"emacsclient -t":         "emacsclient -t",
		} {
			if got := editorCommand(editor); got != want {
				t.Errorf("editorCommand(%q) = %q, want %q", editor, got, want)
			}
		}
	})

	t.Run("no editor: the error says how to give one", func(t *testing.T) {
		secretsApp(t)

		err := editSecrets()

		if err == nil || !strings.Contains(err.Error(), `EDITOR="code --wait" chasen secrets edit`) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("one key at the top of a repository opens the secrets of each app in it", func(t *testing.T) {
		dir := secretsApp(t)
		typeInEditor(t, `TOKEN=abc\n`)
		editSecrets()
		os.Rename("chasen.key", filepath.Join(filepath.Dir(dir), "chasen.key"))

		stored, err := storedSecrets()

		if err != nil || stored["TOKEN"] != "abc" {
			t.Errorf("stored = %v (%v), want the secret, opened with the key of the directory above", stored, err)
		}
	})

	t.Run("in CI the key is CHASEN_KEY", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `TOKEN=abc\n`)
		editSecrets()
		key, _ := os.ReadFile("chasen.key")
		os.Remove("chasen.key")
		t.Setenv("CHASEN_KEY", strings.TrimSpace(string(key)))

		stored, err := storedSecrets()

		if err != nil || stored["TOKEN"] != "abc" {
			t.Errorf("stored = %v (%v), want the secret", stored, err)
		}
	})

	t.Run("the file without its key says where the key goes", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `TOKEN=abc\n`)
		editSecrets()
		os.Remove("chasen.key")

		_, err := storedSecrets()

		if err == nil || !strings.Contains(err.Error(), "chasen.key") || !strings.Contains(err.Error(), "CHASEN_KEY") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("another key does not open the file", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `TOKEN=abc\n`)
		editSecrets()
		t.Setenv("CHASEN_KEY", strings.Repeat("ab", 32))

		_, err := storedSecrets()

		if err == nil || !strings.Contains(err.Error(), "the key does not open chasen.secrets.enc") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a directory with no secrets file has no secrets, and needs no key", func(t *testing.T) {
		secretsApp(t)

		stored, err := storedSecrets()

		if err != nil || len(stored) != 0 {
			t.Errorf("stored = %v (%v)", stored, err)
		}
	})
}

func TestSecretsAtADeploy(t *testing.T) {
	t.Run("every secret of the file goes with the deploy, with no list in chasen.yml", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=sk_live_1\n`)
		editSecrets()

		settings, err := appSettings(appFile{Name: "shop", Env: map[string]string{"LOG_LEVEL": "info"}})

		if err != nil || settings.Env["STRIPE_KEY"] != "sk_live_1" || settings.Env["LOG_LEVEL"] != "info" {
			t.Errorf("env = %v (%v), want the secret of the file and the env of chasen.yml", settings.Env, err)
		}
	})

	t.Run("the environment wins over the file: CI can replace a value", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=from-the-file\n`)
		editSecrets()
		t.Setenv("STRIPE_KEY", "from-the-environment")

		settings, err := appSettings(appFile{Name: "shop", Secrets: []string{"STRIPE_KEY"}})

		if err != nil || settings.Env["STRIPE_KEY"] != "from-the-environment" {
			t.Errorf("env = %v (%v)", settings.Env, err)
		}
	})

	t.Run("a name in secrets: of chasen.yml can come from the file", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `GHCR_TOKEN=token-of-the-registry\n`)
		editSecrets()

		values, err := secretValues(appFile{Name: "shop"}, []string{"GHCR_TOKEN"})

		if err != nil || values["GHCR_TOKEN"] != "token-of-the-registry" {
			t.Errorf("values = %v (%v)", values, err)
		}
	})
}

func TestSecretKeyOfANewApp(t *testing.T) {
	notDeployed := func() bool { return false }
	deployed := func() bool { return true }

	t.Run("the first deploy makes the key and keeps it with the secrets", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=sk_live_1\n`)
		editSecrets()

		err := keepSecretKey(notDeployed)

		stored, _ := storedSecrets()
		if err != nil || len(stored["SECRET_KEY_BASE"]) != 64 || stored["STRIPE_KEY"] != "sk_live_1" {
			t.Errorf("stored = %v (%v), want a key of 64 characters next to the secret from before", stored, err)
		}
	})

	t.Run("the next deploy keeps the same key", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=sk_live_1\n`)
		editSecrets()
		keepSecretKey(notDeployed)
		first, _ := storedSecrets()

		err := keepSecretKey(notDeployed)

		if again, _ := storedSecrets(); err != nil || again["SECRET_KEY_BASE"] != first["SECRET_KEY_BASE"] {
			t.Errorf("the key changed (%v): everybody would be logged out", err)
		}
	})

	t.Run("an app that runs already keeps the key of the server", func(t *testing.T) {
		secretsApp(t)
		typeInEditor(t, `STRIPE_KEY=sk_live_1\n`)
		editSecrets()

		err := keepSecretKey(deployed)

		if stored, _ := storedSecrets(); err != nil || stored["SECRET_KEY_BASE"] != "" {
			t.Errorf("stored = %v (%v), want no new key: a new one would log everybody out", stored, err)
		}
	})

	t.Run("a directory with no secrets file gets none", func(t *testing.T) {
		secretsApp(t)

		err := keepSecretKey(notDeployed)

		if _, statErr := os.Stat("chasen.secrets.enc"); err != nil || statErr == nil {
			t.Errorf("err = %v, file exists = %v: want nothing made", err, statErr == nil)
		}
	})
}
