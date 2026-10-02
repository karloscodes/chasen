package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A store that has the bucket already, the way Hetzner answers for a bucket
// made in its console: it refuses to create it again. owner says if the keys
// of the test can write to it.
func storeWithTheBucket(t *testing.T, owner bool) string {
	t.Helper()
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.Path == "/backups":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`<Error><Code>BucketAlreadyExists</Code><Message>The requested bucket name is not available.</Message></Error>`))
		case !owner:
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(store.Close)
	return store.URL
}

func TestBucketThatExistsAlready(t *testing.T) {
	server := func(t *testing.T) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		t.Setenv("S3_SECRET_ACCESS_KEY", "the-secret")
		if err := saveServerConfig(serverConfig{Domain: "example.com", Token: "token"}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a bucket made in the console of the store is taken, when the keys can write to it", func(t *testing.T) {
		server(t)
		store := storeWithTheBucket(t, true)

		err := serverBucket([]string{"--endpoint", store, "--name", "backups", "--access-key-id", "the-id"})

		cfg, _ := loadServerConfig()
		if err != nil || cfg.Backup.S3 == nil || cfg.Backup.S3.Bucket != "backups" {
			t.Errorf("err = %v, bucket = %+v, want the bucket saved", err, cfg.Backup.S3)
		}
	})

	t.Run("a bucket of somebody else changes nothing", func(t *testing.T) {
		server(t)
		store := storeWithTheBucket(t, false)

		err := serverBucket([]string{"--endpoint", store, "--name", "backups", "--access-key-id", "the-id"})

		cfg, _ := loadServerConfig()
		if err == nil || !strings.Contains(err.Error(), "nothing changed") || cfg.Backup.S3 != nil {
			t.Errorf("err = %v, bucket = %+v, want an error and no bucket", err, cfg.Backup.S3)
		}
	})
}
