package protocol

import (
	"strconv"
	"strings"
	"testing"
)

func TestParsePrivate(t *testing.T) {
	t.Run("takes an address of the server or of a VPN, with a port or without", func(t *testing.T) {
		for value, want := range map[string]string{
			"127.0.0.1":             "127.0.0.1 0",
			"10.8.0.1:3001":         "10.8.0.1 3001",
			"100.101.102.103":       "100.101.102.103 0",
			"fd7a:115c:a1e0::1":     "fd7a:115c:a1e0::1 0",
			"[::1]:3001":            "::1 3001",
			"[fd7a:115c:a1e0::1]":   "fd7a:115c:a1e0::1 0",
			"192.168.1.20:8080":     "192.168.1.20 8080",
			"172.16.0.1":            "172.16.0.1 0",
			"[fd7a:115c:a1e0::9]:1": "fd7a:115c:a1e0::9 1",
		} {
			addr, port, err := ParsePrivate(value)

			if err != nil {
				t.Errorf("ParsePrivate(%q) = %v", value, err)
				continue
			}
			if got := addr.String() + " " + strconv.Itoa(port); got != want {
				t.Errorf("ParsePrivate(%q) = %s, want %s", value, got, want)
			}
		}
	})

	t.Run("refuses what the internet reaches, and what is not an address", func(t *testing.T) {
		for value, want := range map[string]string{
			"0.0.0.0":         "not a private address",
			"::":              "not a private address",
			"203.0.113.5":     "not a private address",
			"lognorth.ts.net": "invalid private address",
			"10.8.0.1:80":     "the proxy's",
			"10.8.0.1:443":    "the proxy's",
			"10.8.0.1:70000":  "invalid port",
			"10.8.0.1:http":   "invalid port",
		} {
			_, _, err := ParsePrivate(value)

			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("ParsePrivate(%q) = %v, want an error with %q", value, err, want)
			}
		}
	})
}

func TestAppListen(t *testing.T) {
	t.Run("a deploy with no address keeps what the app has", func(t *testing.T) {
		got, err := AppListen("", "", "127.0.0.1:8080", 8080, nil)

		if err != nil || got != "127.0.0.1:8080" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("an app on the internet stays there", func(t *testing.T) {
		got, err := AppListen("", "", "", 8080, nil)

		if err != nil || got != "" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("a domain puts a private app on the internet", func(t *testing.T) {
		got, err := AppListen("", "logs.example.com", "127.0.0.1:8080", 8080, nil)

		if err != nil || got != "" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("refuses an address and a domain in one deploy", func(t *testing.T) {
		_, err := AppListen("127.0.0.1", "logs.example.com", "", 8080, nil)

		if err == nil || !strings.Contains(err.Error(), "not both") {
			t.Errorf("AppListen = %v, want an error", err)
		}
	})

	t.Run("a new private app gets the port of the app", func(t *testing.T) {
		got, err := AppListen("127.0.0.1", "", "", 3000, nil)

		if err != nil || got != "127.0.0.1:3000" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("it skips the ports of the other private apps", func(t *testing.T) {
		got, err := AppListen("127.0.0.1", "", "", 8080, []int{8080, 8081})

		if err != nil || got != "127.0.0.1:8082" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("an app that moves to another address keeps its port", func(t *testing.T) {
		got, err := AppListen("10.8.0.1", "", "127.0.0.1:8082", 8080, []int{8080})

		if err != nil || got != "10.8.0.1:8082" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("the port of the deploy comes first", func(t *testing.T) {
		got, err := AppListen("[fd7a:115c:a1e0::1]:3001", "", "127.0.0.1:8080", 8080, nil)

		if err != nil || got != "[fd7a:115c:a1e0::1]:3001" {
			t.Errorf("AppListen = %q, %v", got, err)
		}
	})

	t.Run("refuses a port that another private app has", func(t *testing.T) {
		_, err := AppListen("127.0.0.1:3001", "", "", 8080, []int{3001})

		if err == nil || !strings.Contains(err.Error(), "taken by another private app") {
			t.Errorf("AppListen = %v, want an error", err)
		}
	})
}
