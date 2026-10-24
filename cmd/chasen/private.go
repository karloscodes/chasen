package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/karloscodes/chasen/protocol"
	"gopkg.in/yaml.v3"
)

// privateValue is `private:` in chasen.yml: true is 127.0.0.1, or the
// address of the server in a VPN, with a port or without. The server checks
// the address, and so does the review.
type privateValue string

func (p *privateValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Value {
	case "true":
		*p = protocol.DefaultPrivate
	case "false", "":
		return fmt.Errorf("line %d: private: %s is not a setting. A private app stays private until a deploy with --domain puts it on the internet: remove the line, then run chasen deploy --domain <domain>", node.Line, node.Value)
	default:
		*p = privateValue(node.Value)
	}
	return nil
}

// privateAddress is the address that a deploy sends: from --private, or from
// private: in chasen.yml. The two say one thing, or the deploy stops before
// the build. A domain puts an app on the internet, so it does not come with
// private: in chasen.yml.
func privateAddress(inFile string) (string, error) {
	switch {
	case privateFlag != "" && inFile != "" && privateFlag != inFile:
		return "", fmt.Errorf("chasen.yml says private: %s, and the command says --private=%s. Keep one of them", inFile, privateFlag)
	case inFile != "" && domainFlag != "":
		return "", errors.New("chasen.yml says private:, and the command says --domain. To put the app on the internet, remove private: from chasen.yml first")
	}
	return cmp.Or(privateFlag, inFile), nil
}

// privateSince is the first chasen-server with private apps. A server from
// before does not know the setting: it would put the app on the internet.
const privateSince = "0.13.0"

// requirePrivate stops a private deploy to a server from before private
// apps. The server says its version in chasen load; a server from before
// says none.
func requirePrivate(creds credentials) error {
	var out strings.Builder
	if code, err := protocol.Client(creds).Run(context.Background(), "load", nil, nil, &out); err == nil && code == 0 {
		if v := statusValues(cleanLines(out.String()), "Version"); len(v) > 0 && atLeast(v[0], privateSince) {
			return nil
		}
	}
	return fmt.Errorf("the server runs a chasen-server from before private apps, and it would put the app on the internet. Update it first, on the server: chasen-server update (private apps need v%s)", privateSince)
}

// atLeast compares the version of a release, like v0.13.0 or v0.13.0-rc.1,
// with a minimum. A build from source (dev) has everything.
func atLeast(version, minimum string) bool {
	if version == "dev" {
		return true
	}
	have, want := versionParts(version), versionParts(minimum)
	for i := range want {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

func versionParts(v string) [3]int {
	var parts [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	for i, s := range strings.SplitN(core, ".", 3) {
		parts[i], _ = strconv.Atoi(s)
	}
	return parts
}
