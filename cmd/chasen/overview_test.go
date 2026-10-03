package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
)

func TestOverviewOfEveryServer(t *testing.T) {
	t.Run("each server with its apps and its alerts, and the counts of all of them", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		server := mock.New(time.Now())
		api := httptest.NewServer(server)
		defer api.Close()
		if err := (logins{Current: api.URL, Tokens: map[string]string{api.URL: server.Token, "ssh://root@gone.invalid": "token"}}).save(); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder

		err := overviewAll(&out)

		var all overviewJSON
		if err != nil || json.Unmarshal([]byte(out.String()), &all) != nil {
			t.Fatalf("overview = %q, %v", out.String(), err)
		}
		if len(all.Servers) != 2 || all.Warnings != 3 || all.Errors != 0 {
			t.Fatalf("overview = %+v, want two servers, 2 warnings of the mock and 1 server that does not answer", all)
		}
		mockServer := all.Servers[0]
		if len(mockServer.Apps) != 3 || mockServer.Apps[2].App != "shop" || !mockServer.Apps[2].Up || len(mockServer.Alerts) != 2 {
			t.Errorf("the mock server = %+v", mockServer)
		}
		if gone := all.Servers[1]; gone.Name != "gone.invalid" || gone.Error == "" {
			t.Errorf("the server that does not answer = %+v", gone)
		}
	})

	t.Run("--watch prints a line at once, and again for a line on stdin, with the alerts of the first ask", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		server := mock.New(time.Now())
		var alertAsks atomic.Int32
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "alerts") {
				alertAsks.Add(1)
			}
			server.ServeHTTP(w, r)
		}))
		defer api.Close()
		if err := (logins{Current: api.URL, Tokens: map[string]string{api.URL: server.Token}}).save(); err != nil {
			t.Fatal(err)
		}
		stdin, ask := io.Pipe()
		output, out := io.Pipe()
		go watchOverview(stdin, out)
		lines := bufio.NewScanner(output)
		next := func() overviewJSON {
			var all overviewJSON
			if !lines.Scan() || json.Unmarshal(lines.Bytes(), &all) != nil {
				t.Fatalf("the line = %q", lines.Text())
			}
			return all
		}

		first := next()
		ask.Write([]byte("\n"))
		second := next()
		output.Close()

		if len(first.Servers[0].Apps) != 3 || len(second.Servers[0].Apps) != 3 {
			t.Errorf("the lines = %+v, %+v", first, second)
		}
		if second.Warnings != 2 || alertAsks.Load() != 1 {
			t.Errorf("the second line has %d warnings, and the server was asked for its alerts %d times: want 2, from one ask", second.Warnings, alertAsks.Load())
		}
	})
}
