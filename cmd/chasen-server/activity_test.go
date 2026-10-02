package main

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivity(t *testing.T) {
	t.Run("the feed has the output of a command while it still runs", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		db, err := openServerDB()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		id, err := startActivity(db, "shop", "deploy 3f9a2c1")
		if err != nil {
			t.Fatal(err)
		}
		out := &flushWriter{w: httptest.NewRecorder(), keep: &bytes.Buffer{}}
		out.save = func(output string) { saveActivity(db, id, output) }
		read := func() (status, output string) {
			if err := db.QueryRow("SELECT status, output FROM activity WHERE id = ?", id).Scan(&status, &output); err != nil {
				t.Fatal(err)
			}
			return status, output
		}

		out.Write([]byte("Pulling ghcr.io/you/shop:3f9a2c1\n"))

		if status, output := read(); status != "running" || output != "Pulling ghcr.io/you/shop:3f9a2c1\n" {
			t.Fatalf("while it runs: %s, %q", status, output)
		}

		// A line that follows at once waits for the next save. A later one is saved.
		out.Write([]byte("Starting shop 3f9a2c1\n"))
		if _, output := read(); output != "Pulling ghcr.io/you/shop:3f9a2c1\n" {
			t.Errorf("a save ran for every line: %q", output)
		}
		out.saved = time.Now().Add(-2 * saveEvery)
		out.Write([]byte("Deployed shop 3f9a2c1\n"))
		if _, output := read(); output != "Pulling ghcr.io/you/shop:3f9a2c1\nStarting shop 3f9a2c1\nDeployed shop 3f9a2c1\n" {
			t.Errorf("after a second: %q", output)
		}

		finishActivity(db, id, true, out.keep.String())
		saveActivity(db, id, "a late save must not change a finished entry")
		if status, output := read(); status != "succeeded" || output != out.keep.String() {
			t.Errorf("at the end: %s, %q", status, output)
		}
	})
}
