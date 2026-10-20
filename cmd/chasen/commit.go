package main

import (
	"net/url"
	"os/exec"
	"regexp"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// The commit of a deploy goes with it to the server, for the history: its
// subject, and the page of the commit on the forge of the repository. So the
// history reads "deploy 3f9a2c1 Fix login redirect", and o on the screen
// opens the commit.

// shortHash matches a commit, by the 7 characters that git shows or more.
var shortHash = regexp.MustCompile("^[0-9a-f]{7,40}$")

// forgePath matches the path of a repository on a forge: owner/name, or
// group/subgroup/name.
var forgePath = regexp.MustCompile(`^[\w.~-]+(/[\w.~-]+)+$`)

// commitOf returns the commit for the history, or nil when git does not know
// the hash here, like the tag of an image that another repository released.
func commitOf(hash string) *protocol.Commit {
	full, err := exec.Command("git", "rev-parse", "--verify", "--quiet", hash+"^{commit}").Output()
	if err != nil {
		return nil
	}
	hash = strings.TrimSpace(string(full))
	subject, err := exec.Command("git", "log", "-1", "--format=%s", hash).Output()
	if err != nil {
		return nil
	}
	origin, _ := exec.Command("git", "remote", "get-url", "origin").Output()
	return &protocol.Commit{Subject: strings.TrimSpace(string(subject)), URL: commitURL(string(origin), hash)}
}

// commitURL returns the web page of a commit for the git origin of its
// repository, or "". The login of the origin never goes into it: an origin
// can carry a token.
func commitURL(origin, hash string) string {
	origin = strings.TrimSpace(origin)
	var host, path string
	switch {
	case strings.HasPrefix(origin, "https://"), strings.HasPrefix(origin, "http://"), strings.HasPrefix(origin, "ssh://"):
		u, err := url.Parse(origin)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	case strings.Contains(origin, "@") && strings.Contains(origin, ":"):
		// The short form of SSH: git@github.com:you/shop.git
		_, rest, _ := strings.Cut(origin, "@")
		host, path, _ = strings.Cut(rest, ":")
	default:
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || !forgePath.MatchString(path) || !commitHash.MatchString(hash) {
		return ""
	}
	page := "/commit/"
	switch {
	case strings.Contains(host, "gitlab"):
		page = "/-/commit/"
	case host == "bitbucket.org":
		page = "/commits/"
	}
	return "https://" + host + "/" + path + page + hash
}
