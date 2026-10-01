package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func post(t *testing.T, target string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(target, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&answer)
	return resp.StatusCode, answer
}

func TestDeviceLogin(t *testing.T) {
	server := &Server{
		Name:         "test",
		Authenticate: func(key string) (string, bool) { return "ana", key == "the-key" },
		Issue:        func(subject string) (string, error) { return "token-for-" + subject, nil },
	}
	mux := http.NewServeMux()
	server.Register(mux)
	web := httptest.NewServer(mux)
	defer web.Close()
	start := func() (deviceCode, userCode string) {
		_, device := post(t, web.URL+"/oauth/device_authorization", nil)
		return device["device_code"].(string), device["user_code"].(string)
	}
	poll := func(deviceCode string) (int, map[string]any) {
		return post(t, web.URL+"/oauth/token", url.Values{"grant_type": {GrantType}, "device_code": {deviceCode}})
	}

	t.Run("the client gets a token after the user approves with the right key", func(t *testing.T) {
		deviceCode, userCode := start()
		if _, answer := poll(deviceCode); answer["error"] != "authorization_pending" {
			t.Fatalf("poll before the approval = %v, want authorization_pending", answer)
		}

		status, _ := post(t, web.URL+"/oauth/device", url.Values{"user_code": {userCode}, "key": {"the-key"}})
		_, answer := poll(deviceCode)

		if status != http.StatusOK || answer["access_token"] != "token-for-ana" {
			t.Errorf("approval status %d, poll = %v, want the token for ana", status, answer)
		}
		if _, again := poll(deviceCode); again["error"] != "expired_token" {
			t.Errorf("second poll = %v, want expired_token: a device code gives one token", again)
		}
	})

	t.Run("a wrong key approves nothing", func(t *testing.T) {
		deviceCode, userCode := start()

		status, _ := post(t, web.URL+"/oauth/device", url.Values{"user_code": {userCode}, "key": {"wrong"}})
		_, answer := poll(deviceCode)

		if status != http.StatusForbidden || answer["error"] != "authorization_pending" {
			t.Errorf("approval status %d, poll = %v, want 403 and authorization_pending", status, answer)
		}
	})

	t.Run("the right key with a wrong code approves nothing", func(t *testing.T) {
		deviceCode, _ := start()

		status, _ := post(t, web.URL+"/oauth/device", url.Values{"user_code": {"XXXX-XXXX"}, "key": {"the-key"}})
		_, answer := poll(deviceCode)

		if status != http.StatusForbidden || answer["error"] != "authorization_pending" {
			t.Errorf("approval status %d, poll = %v, want 403 and authorization_pending", status, answer)
		}
	})
}

func TestLimits(t *testing.T) {
	newServer := func() *httptest.Server {
		login := &Server{
			Name:         "test",
			Authenticate: func(key string) (string, bool) { return "owner", key == "right" },
			Issue:        func(string) (string, error) { return "token", nil },
		}
		mux := http.NewServeMux()
		login.Register(mux)
		return httptest.NewServer(mux)
	}

	t.Run("the login page stops after many wrong keys, also for the right one", func(t *testing.T) {
		server := newServer()
		defer server.Close()
		try := func(key string) int {
			resp, err := http.PostForm(server.URL+"/oauth/device", url.Values{"user_code": {"AAAA-AAAA"}, "key": {key}})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}

		for range maxFailures {
			try("wrong")
		}

		if status := try("right"); status != http.StatusTooManyRequests {
			t.Errorf("status after %d wrong keys = %d, want 429", maxFailures, status)
		}
	})

	t.Run("only so many logins can wait at one time", func(t *testing.T) {
		server := newServer()
		defer server.Close()
		start := func() int {
			resp, err := http.Post(server.URL+"/oauth/device_authorization", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}

		for range maxGrants {
			start()
		}

		if status := start(); status != http.StatusTooManyRequests {
			t.Errorf("status of login %d = %d, want 429", maxGrants+1, status)
		}
	})
}
