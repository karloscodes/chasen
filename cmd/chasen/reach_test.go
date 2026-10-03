package main

import (
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestAddressesOfTheApp(t *testing.T) {
	t.Run("the addresses are the URL lines of the deploy, and local or example names are left out", func(t *testing.T) {
		output := "Starting shop 3f9a2c1\n\nDeployed shop 3f9a2c1\n  https://shop.example.com\n  https://shop.localhost\n  https://shop.com\n  https://getexample.com\n"

		urls := appURLs(output)

		if !slices.Equal(urls, []string{"https://shop.com", "https://getexample.com"}) {
			t.Errorf("appURLs = %v", urls)
		}
	})

	t.Run("a request that is answered is fine", func(t *testing.T) {
		said := reachable("https://shop.example.com", &http.Response{StatusCode: 200, Header: http.Header{}}, nil)

		if said != "✓ https://shop.example.com answers from here." {
			t.Errorf("said %q", said)
		}
	})

	t.Run("each failure says what to fix", func(t *testing.T) {
		wrap := func(err error) error { return &url.Error{Op: "Get", URL: "https://shop.example.com/up", Err: err} }
		cases := []struct {
			name string
			resp *http.Response
			err  error
			want string
		}{
			{"no DNS record", nil, wrap(&net.DNSError{Err: "no such host", Name: "shop.example.com", IsNotFound: true}), "DNS has no record for shop.example.com yet. Add an A record"},
			{"no certificate yet", nil, wrap(x509.UnknownAuthorityError{}), "the certificate is not there yet"},
			{"a firewall drops it", nil, wrap(&net.OpError{Op: "dial", Err: timeoutError{}}), "nothing answers on port 443. A firewall drops the traffic"},
			{"another machine refuses it", nil, wrap(&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}), "refuses port 443. Does its DNS record point to this server?"},
			{"Cloudflare cannot reach the server", &http.Response{StatusCode: 522, Header: http.Header{"Server": {"cloudflare"}}}, nil, "Cloudflare cannot reach the app (error 522)"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				said := reachable("https://shop.example.com", c.resp, c.err)

				if !strings.HasPrefix(said, "✗ ") || !strings.Contains(said, c.want) {
					t.Errorf("said %q, want %q in it", said, c.want)
				}
			})
		}
	})

	t.Run("a real request to a server that answers", func(t *testing.T) {
		app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer app.Close()
		var out strings.Builder

		checkAddresses(&out, []string{app.URL})

		// The test server has a certificate of its own, which this computer does not trust.
		if !strings.Contains(out.String(), "the certificate is not there yet") {
			t.Errorf("checkAddresses = %q", out.String())
		}
	})
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }
