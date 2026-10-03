package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// After a deploy, the CLI asks each address of the app from this computer.
// The server checks the app from inside; DNS, the firewalls, the certificate,
// and Cloudflare are outside, and only a request from outside sees them. One
// line for each address: it answers, or what to fix.

// appURLs are the addresses that the output of the server names: the lines
// that are only an https:// URL. The names that never belong to a real app
// are left out: local ones, and the ones reserved for examples and tests.
var (
	appURL   = regexp.MustCompile(`(?m)^\s+(https://[a-z0-9.-]+)\s*$`)
	reserved = regexp.MustCompile(`(^|\.)(localhost|test|example|invalid|example\.com|example\.org|example\.net)$`)
)

func appURLs(output string) []string {
	var urls []string
	for _, m := range appURL.FindAllStringSubmatch(output, -1) {
		if !reserved.MatchString(strings.TrimPrefix(m[1], "https://")) {
			urls = append(urls, m[1])
		}
	}
	return urls
}

// checkAddresses asks each address at the same time, and prints what it
// found in the order of the addresses.
func checkAddresses(w io.Writer, urls []string) {
	if len(urls) == 0 {
		return
	}
	// The first HTTPS request is the one that gets the certificate: give it time.
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	said := make([]string, len(urls))
	var wg sync.WaitGroup
	for i, url := range urls {
		wg.Go(func() {
			resp, err := client.Get(url + "/up")
			if resp != nil {
				resp.Body.Close()
			}
			said[i] = reachable(url, resp, err)
		})
	}
	wg.Wait()
	fmt.Fprintln(w)
	for _, line := range said {
		fmt.Fprintln(w, line)
	}
}

// reachable says in one line what a request to an address found.
func reachable(url string, resp *http.Response, err error) string {
	host := strings.TrimPrefix(url, "https://")
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var alert tls.AlertError
	switch {
	case err == nil && resp.StatusCode >= 520 && resp.StatusCode <= 527 && resp.Header.Get("Server") == "cloudflare":
		return fmt.Sprintf("✗ %s: Cloudflare cannot reach the app (error %d). Check that ports 80 and 443 of the server are open, and that SSL in Cloudflare is Full (strict).", url, resp.StatusCode)
	case err == nil:
		return "✓ " + url + " answers from here."
	case errors.As(err, &dns) && dns.IsNotFound:
		return fmt.Sprintf("✗ %s: DNS has no record for %s yet. Add an A record that points to the server. The certificate comes on the first visit after that.", url, host)
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &hostname), errors.As(err, &alert):
		return fmt.Sprintf("✗ %s: the certificate is not there yet. Let's Encrypt gives it when %s points to the server and port 80 is open. Try again in a minute.", url, host)
	case isTimeout(err):
		return fmt.Sprintf("✗ %s: nothing answers on port 443. A firewall drops the traffic: open ports 80 and 443 on the server, and at your provider.", url)
	case strings.Contains(err.Error(), "connection refused"):
		return fmt.Sprintf("✗ %s: the address of %s refuses port 443. Does its DNS record point to this server?", url, host)
	}
	return fmt.Sprintf("✗ %s does not answer from here: %v", url, err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
