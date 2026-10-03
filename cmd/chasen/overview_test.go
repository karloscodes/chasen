package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
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
}
