// Package protocol is the contract between the client, the server, and the
// cloud. chasen-server answers it. The cloud answers it too, and passes the
// request on to the server of the account. So the client talks to both in the
// same way.
//
// A client sends one request for each command:
//
//	POST /v1/<command>?arg=<app>&arg=...      Authorization: Bearer <token>
//
// The request body is the input of the command: for deploy, check, and
// restart it is the settings of the app (see Settings). The response is the
// output of the command as it runs. The last line is ExitMarker and the exit
// code.
//
// The login is the OAuth 2.0 device flow in the oauth package.
package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ExitMarker starts the last line of each response. The exit code follows it.
const ExitMarker = "\x00chasen-exit "

// Commands are the commands a client can run. The first argument of each one
// is the app, except for list. For enable, the app is the name of the addon.
var Commands = []string{"list", "load", "deploy", "check", "enable", "restart", "status", "logs", "run", "history", "domains", "backup", "backups", "verify", "restore", "remove"}

// ServerCommands are the commands about the server itself, for its owner:
// `bucket` shows or sets where the backups go. Their first argument is not
// an app. A server answers them. A service in front of servers, which owns
// the servers it runs, does not pass them on.
var ServerCommands = []string{"bucket"}

// Settings is what the client sends with `deploy`, `check`, and `restart`:
// the image, the env and the secrets of the app, and the overrides of the
// standard from chasen.yml. A zero value means "use the default".
//
// The settings are the first line of the request body, as JSON. For a
// website, the files follow that line, as a tar.gz archive.
type Settings struct {
	// Image is the image that the server pulls for the next deploy, with its
	// tag: ghcr.io/you/shop:3f9a2c1. Empty for a website: its files come as
	// a tar archive in the body of the deploy.
	Image string `json:"image,omitempty"`
	// Registry is the login for a private image. A public image needs none.
	Registry *Registry `json:"registry,omitempty"`

	// Domain is the domain of an app at its first deploy: `chasen deploy
	// --domain shop.example.com`. Without it, the app gets <name>.<base
	// domain of the server>. An app that runs keeps its domains.
	Domain string `json:"domain,omitempty"`

	// NoBackup turns the backups of the app off: no snapshots, no live
	// replica, and no restore from the bucket at a deploy. It is `backup:
	// false` in chasen.yml, for an app whose data is made again at each
	// start, like a demo.
	NoBackup bool `json:"no_backup,omitempty"`

	Env           map[string]string `json:"env"`
	Port          int               `json:"port,omitempty"`
	Health        string            `json:"health,omitempty"`
	HealthTimeout int               `json:"health_timeout,omitempty"`
	Volumes       []string          `json:"volumes,omitempty"`
}

// Registry is the login of an image registry, like ghcr.io or Docker Hub.
type Registry struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// RegistryHost returns the registry of an image, or "" for Docker Hub: the
// first part of the name, when it looks like a host.
func RegistryHost(image string) string {
	host, _, found := strings.Cut(image, "/")
	if found && (strings.ContainsAny(host, ".:") || host == "localhost") {
		return host
	}
	return ""
}

// Body returns the request body of deploy, check, and restart: the settings
// on one line, then the files of a website when there are any.
func (s Settings) Body(files io.Reader) io.Reader {
	line, _ := json.Marshal(s)
	if files == nil {
		return bytes.NewReader(append(line, '\n'))
	}
	return io.MultiReader(bytes.NewReader(append(line, '\n')), files)
}

// Logout makes the API forget the token of the request. The API answers it
// itself: it is not a command of an app.
const Logout = "logout"

// CreatesServer reports the command that puts an app on a server. In the
// cloud, it creates the server of an account that has none.
func CreatesServer(command string) bool { return command == "deploy" }

// Placement is the answer of the cloud to "where does this app go?". The CLI
// asks before the first deploy of an app. A plain server has no such answer.
type Placement struct {
	Server    string         `json:"server"`     // the server that has the app, or ""
	Servers   []PlacedServer `json:"servers"`    // the servers of the account
	Types     []ServerType   `json:"types"`      // what a new server can be, cheapest first
	FeeCents  int64          `json:"fee_cents"`  // the monthly fee of Chasen for the account, with any number of servers
	CanCreate bool           `json:"can_create"` // false: this cloud cannot create servers now
	Reason    string         `json:"reason"`     // why not
}

type PlacedServer struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Region       string   `json:"region"`
	MonthlyCents int64    `json:"monthly_cents"` // the price of the provider
	Apps         []string `json:"apps"`
}

// ServerType is one size of server in one location, with the monthly price
// of the provider there.
type ServerType struct {
	Name         string `json:"name"`
	Location     string `json:"location"` // the name of the provider, like fsn1
	Region       string `json:"region"`   // for people: Europe, USA
	Cores        int    `json:"cores"`
	MemoryGB     int    `json:"memory_gb"`
	DiskGB       int    `json:"disk_gb"`
	MonthlyCents int64  `json:"monthly_cents"`
}

// ServerHeader carries the choice of the user to the cloud: the id of one of
// their servers, or "new" for a new one. A new one can name its type, its
// location, or both: "new:cx33", "new:@ash", "new:cx33@ash".
const ServerHeader = "Chasen-Server"

// ErrUnauthorized means the API refused the token.
var ErrUnauthorized = errors.New("the token is not valid")

// Client calls the API of a server or of the cloud.
type Client struct {
	URL    string
	Token  string
	Server string // sent as ServerHeader, when the user chose a server
}

// request sends one request to the API, with the login.
func (c Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	client, address := c.api()
	req, err := http.NewRequestWithContext(ctx, method, address+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Server != "" {
		req.Header.Set(ServerHeader, c.Server)
	}
	resp, err := client.Do(req)
	return resp, c.errSSH(err)
}

// Do sends one command and returns the response as it is.
func (c Client) Do(ctx context.Context, command string, args []string, stdin io.Reader) (*http.Response, error) {
	return c.request(ctx, "POST", "/v1/"+url.PathEscape(command)+"?"+url.Values{"arg": args}.Encode(), stdin)
}

// Run sends one command, writes its output to out as it arrives, and returns
// the exit code of the command.
func (c Client) Run(ctx context.Context, command string, args []string, stdin io.Reader, out io.Writer) (int, error) {
	resp, err := c.Do(ctx, command, args, stdin)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return 0, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("%s: %s: %s", c.URL, resp.Status, strings.TrimSpace(string(msg)))
	}

	lines := bufio.NewReader(resp.Body)
	for {
		line, err := lines.ReadString('\n')
		if code, ok := strings.CutPrefix(line, ExitMarker); ok {
			return strconv.Atoi(strings.TrimSpace(code))
		}
		io.WriteString(out, line)
		if err != nil {
			return 0, errors.New("the connection closed before the server finished")
		}
	}
}

// Placement asks the cloud where an app goes. A plain server does not know
// the question: then ok is false.
func (c Client) Placement(ctx context.Context, app string) (p Placement, ok bool, err error) {
	resp, err := c.request(ctx, "GET", "/v1/placement?app="+url.QueryEscape(app), nil)
	if err != nil {
		return p, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return p, true, json.NewDecoder(resp.Body).Decode(&p)
	case http.StatusUnauthorized:
		return p, false, ErrUnauthorized
	}
	return p, false, nil
}
