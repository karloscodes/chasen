package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRailsMasterKey(t *testing.T) {
	rails := func(t *testing.T, files map[string]string) {
		t.Chdir(t.TempDir())
		for path, content := range files {
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte(content), 0600)
		}
	}

	t.Run("the key of config/master.key travels with the deploy", func(t *testing.T) {
		rails(t, map[string]string{"config/credentials.yml.enc": "x", "config/master.key": "abc123\n"})
		env := map[string]string{}

		err := railsMasterKey(env)

		if err != nil || env["RAILS_MASTER_KEY"] != "abc123" {
			t.Errorf("RAILS_MASTER_KEY = %q, %v, want the content of config/master.key", env["RAILS_MASTER_KEY"], err)
		}
	})

	t.Run("the key of the production credentials wins over the master key", func(t *testing.T) {
		rails(t, map[string]string{"config/master.key": "abc123", "config/credentials/production.key": "prod456"})
		env := map[string]string{}

		railsMasterKey(env)

		if env["RAILS_MASTER_KEY"] != "prod456" {
			t.Errorf("RAILS_MASTER_KEY = %q, want the production key", env["RAILS_MASTER_KEY"])
		}
	})

	t.Run("a key in the secrets wins over the file", func(t *testing.T) {
		rails(t, map[string]string{"config/master.key": "abc123"})
		env := map[string]string{"RAILS_MASTER_KEY": "from-secrets"}

		railsMasterKey(env)

		if env["RAILS_MASTER_KEY"] != "from-secrets" {
			t.Errorf("RAILS_MASTER_KEY = %q, want the one of the secrets", env["RAILS_MASTER_KEY"])
		}
	})

	t.Run("an app that is not Rails gets nothing", func(t *testing.T) {
		rails(t, map[string]string{"Dockerfile": "FROM scratch"})
		env := map[string]string{}

		err := railsMasterKey(env)

		if _, ok := env["RAILS_MASTER_KEY"]; ok || err != nil {
			t.Errorf("env = %v, %v, want no key", env, err)
		}
	})
}
