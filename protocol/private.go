package protocol

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// A private app is not on the internet. It has no domain and no route on the
// proxy: its port listens on an address of the server that only the server
// reaches, 127.0.0.1, or the machines of a VPN, like the address of the
// server in Tailscale or WireGuard.

// DefaultPrivate is the address of `--private` and `private: true`.
const DefaultPrivate = "127.0.0.1"

// errPrivateDomain is a deploy that asks for both.
var errPrivateDomain = errors.New("a private app has no domain: deploy with --private or with --domain, not both")

// sharedAddresses is 100.64.0.0/10, the addresses that Tailscale and NetBird
// give their machines. The internet does not route them.
var sharedAddresses = netip.MustParsePrefix("100.64.0.0/10")

// ParsePrivate reads the address of a private app: an IP address of the
// server, with a port or without one (0). It refuses an address that the
// internet reaches: a public one, or 0.0.0.0, which is every address.
func ParsePrivate(value string) (netip.Addr, int, error) {
	host, port := value, ""
	if h, p, err := net.SplitHostPort(value); err == nil {
		host, port = h, p
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid private address %q: use an IP address of the server, like 127.0.0.1 or 10.8.0.1, with a port or without", value)
	}
	if !addr.IsLoopback() && !addr.IsPrivate() && !sharedAddresses.Contains(addr) {
		return netip.Addr{}, 0, fmt.Errorf("%s is not a private address: the internet reaches it. Use 127.0.0.1, or the address of the server in your VPN", addr)
	}
	if port == "" {
		return addr, 0, nil
	}
	n, err := strconv.Atoi(port)
	switch {
	case err != nil || n < 1 || n > 65535:
		return netip.Addr{}, 0, fmt.Errorf("invalid port %q in %q: use 1 to 65535", port, value)
	case n == 80 || n == 443:
		return netip.Addr{}, 0, fmt.Errorf("port %d is the proxy's: give the private app another one", n)
	}
	return addr, n, nil
}

// AppListen decides where an app listens after a deploy: an address and a
// port for a private app, or "" for an app on the internet. A deploy that
// sends no address keeps what the app has, and only a domain puts a private
// app on the internet. The port is the one the deploy sends, then the one
// the app has, then the first free one from the port of the app. taken is
// the ports of the other private apps of the server.
func AppListen(sent, domain, has string, appPort int, taken []int) (string, error) {
	switch {
	case sent != "" && domain != "":
		return "", errPrivateDomain
	case sent == "" && domain != "":
		return "", nil
	case sent == "":
		return has, nil
	}
	addr, port, err := ParsePrivate(sent)
	if err != nil {
		return "", err
	}
	if port == 0 && has != "" {
		_, port, _ = ParsePrivate(has)
	}
	if port == 0 {
		port = appPort
		for port == 80 || port == 443 || slices.Contains(taken, port) {
			port++
		}
	}
	if slices.Contains(taken, port) {
		return "", fmt.Errorf("port %d is taken by another private app: choose another one, like --private=%s:%d", port, addr, port+1)
	}
	return netip.AddrPortFrom(addr, uint16(port)).String(), nil
}
