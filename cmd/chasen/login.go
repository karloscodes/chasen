package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/karloscodes/chasen/oauth"
	"github.com/karloscodes/chasen/protocol"
)

// credentials is the saved login: the API of a server, and the token.
type credentials struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Server has the fields of protocol.Client, so a login converts to a
	// client. The CLI leaves it empty.
	Server string `json:"-"`
}

func credentialsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "chasen", "credentials.json")
}

// logins is the file of saved logins. You can be logged in to several
// servers. Commands go to the current one, unless chasen.yml names a server.
type logins struct {
	Current string            `json:"current"`
	Tokens  map[string]string `json:"logins"` // by API address
}

func loadLogins() logins {
	saved := logins{Tokens: map[string]string{}}
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		return saved
	}
	// The first format of the file had one login: {"url": ..., "token": ...}.
	var one credentials
	if json.Unmarshal(data, &one) == nil && one.URL != "" {
		return logins{Current: one.URL, Tokens: map[string]string{one.URL: one.Token}}
	}
	json.Unmarshal(data, &saved)
	if saved.Tokens == nil {
		saved.Tokens = map[string]string{}
	}
	return saved
}

func (l logins) save() error {
	data, _ := json.MarshalIndent(l, "", "  ")
	if err := os.MkdirAll(filepath.Dir(credentialsPath()), 0700); err != nil {
		return err
	}
	return os.WriteFile(credentialsPath(), data, 0600)
}

// apiAddress turns what a person types into the address of an API:
// example.com is https://api.example.com, root@203.0.113.5 is reached
// through SSH, and a full address stays as it is.
func apiAddress(name string) string {
	switch {
	case strings.Contains(name, "://"):
		return strings.TrimRight(name, "/")
	case strings.Contains(name, "@"):
		// root@203.0.113.5 is a server that the CLI reaches through SSH.
		return "ssh://" + name
	}
	return "https://api." + name
}

// loadCredentials picks where a command goes: CHASEN_URL and CHASEN_TOKEN
// (for CI), then the server that chasen.yml names, then the current login.
func loadCredentials(server string) (credentials, error) {
	creds := credentials{URL: os.Getenv("CHASEN_URL"), Token: os.Getenv("CHASEN_TOKEN")}
	if creds.URL != "" && creds.Token != "" {
		return creds, nil
	}
	saved := loadLogins()
	creds.URL = saved.Current
	if server != "" {
		creds.URL = apiAddress(server)
	}
	token, ok := saved.Tokens[creds.URL]
	switch {
	case ok:
		creds.Token = token
		return creds, nil
	case server != "":
		return creds, fmt.Errorf("chasen.yml names the server %s, and you are not logged in to it. Run: chasen add server %s", server, server)
	}
	return creds, errors.New("not logged in. " + howToLogin)
}

// serverLogin returns the login of the server that a command names, like
// `chasen deploy --server root@203.0.113.5`. A server that this computer does not
// know yet is added first: it gets Chasen when it has none, and this
// computer gets its login. A server that it knows is used for this command
// only: the current server stays the current one.
func serverLogin(server string) (credentials, error) {
	address := apiAddress(server)
	if token, ok := loadLogins().Tokens[address]; ok {
		return credentials{URL: address, Token: token}, nil
	}
	if err := connect(address); err != nil {
		return credentials{}, err
	}
	return credentials{URL: address, Token: loadLogins().Tokens[address]}, nil
}

// listServers shows the saved logins. The star marks the current one.
func listServers() error {
	saved := loadLogins()
	if len(saved.Tokens) == 0 {
		return errors.New("not logged in. " + howToLogin)
	}
	printServers(os.Stdout, saved)
	return nil
}

// printServers prints the logins with a number: `chasen use 2` takes one.
// The star marks the current one.
func printServers(w io.Writer, saved logins) {
	for i, address := range slices.Sorted(maps.Keys(saved.Tokens)) {
		mark := " "
		if address == saved.Current {
			mark = "*"
		}
		fmt.Fprintf(w, "%s %d  %s\n", mark, i+1, address)
	}
}

// useServer makes another saved login the current one. The server is its
// number in chasen servers, its address, or a part of the address that
// only one login has. With nothing, it shows the list and asks.
func useServer(args []string) error { return chooseServer(args, os.Stdin, os.Stdout) }

func chooseServer(args []string, in io.Reader, out io.Writer) error {
	saved := loadLogins()
	if len(saved.Tokens) == 0 {
		return errors.New("not logged in. " + howToLogin)
	}
	var said string
	switch len(args) {
	case 0:
		printServers(out, saved)
		fmt.Fprint(out, "Use which server? ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if said = strings.TrimSpace(line); said == "" {
			return errors.New("usage: chasen use <server>: a number of the list, or a part of the address")
		}
	case 1:
		said = args[0]
	default:
		return errors.New("usage: chasen use <server>: a number of chasen servers, or a part of the address")
	}
	address, err := findServer(saved, said)
	if err != nil {
		return err
	}
	saved.Current = address
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Commands now go to", address)
	return nil
}

// findServer returns the login that a person means.
func findServer(saved logins, said string) (string, error) {
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	if n, err := strconv.Atoi(said); err == nil {
		if n < 1 || n > len(addresses) {
			return "", fmt.Errorf("there is no server %d: chasen servers lists %d", n, len(addresses))
		}
		return addresses[n-1], nil
	}
	if address := apiAddress(said); slices.Contains(addresses, address) {
		return address, nil
	}
	var found []string
	for _, address := range addresses {
		if strings.Contains(address, said) {
			found = append(found, address)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("you are not logged in to %s. Run: chasen servers", said)
	}
	return "", fmt.Errorf("%s matches %s. Say more of the address, or its number in chasen servers", said, strings.Join(found, " and "))
}

const howToLogin = "Run: chasen add server <user>@<host>"

// addServer logs in to a server you host yourself. The server example.com
// has its API on https://api.example.com.
func addServer(args []string) error {
	if len(args) != 2 || args[0] != "server" {
		return errors.New("usage: chasen add server <domain>, or chasen add server <user>@<host> to reach it through SSH")
	}
	return connect(apiAddress(args[1]))
}

// connect logs in to the API at target, saves the login, and makes it the
// current one.
func connect(target string) error {
	server, err := url.Parse(strings.TrimRight(target, "/"))
	if err != nil || server.Host == "" {
		return fmt.Errorf("invalid address %q", target)
	}
	if protocol.IsSSH(server.String()) {
		return connectSSH(server.String())
	}
	// The token goes in each request. Plain http is only for a server on this machine or in a test.
	host := server.Hostname()
	ip := net.ParseIP(host)
	local := host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate()))
	if server.Scheme != "https" && !(server.Scheme == "http" && local) {
		return errors.New("the address must start with https://")
	}

	// CI sets CHASEN_TOKEN. A person logs in with a browser.
	creds := credentials{URL: server.String(), Token: os.Getenv("CHASEN_TOKEN")}
	if creds.Token == "" {
		if creds.Token, err = deviceLogin(creds.URL); err != nil {
			return err
		}
	}
	if err := remote(creds, nil, io.Discard, "list"); err != nil {
		return err
	}

	saved := loadLogins()
	saved.Tokens[creds.URL] = creds.Token
	saved.Current = creds.URL
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Println("Logged in to", creds.URL)
	return nil
}

// connectSSH logs in to a server through SSH. Who can log in to the server
// with SSH, as root or with sudo, owns it: the server gives its token to
// that person, and no browser is needed.
func connectSSH(address string) error {
	creds := credentials{URL: address, Token: os.Getenv("CHASEN_TOKEN")}
	if creds.Token == "" {
		var err error
		if creds.Token, err = serverToken(address); err != nil {
			return err
		}
	}
	if err := remote(creds, nil, io.Discard, "list"); err != nil {
		return err
	}
	saved := loadLogins()
	saved.Tokens[creds.URL] = creds.Token
	saved.Current = creds.URL
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Println("Logged in to", creds.URL)
	return nil
}

// installScript installs chasen-server on a server: the same lines as in
// the docs, for a person who does it by hand.
const installScript = "curl -fsSL https://chasenhq.com/server | sh\n"

// serverToken gets a login for a server through SSH: the server makes a
// token for this CLI. A server that has no chasen-server gets it first, and
// a server that is not set up is set up: from a new machine to a Chasen
// server in this one command.
func serverToken(address string) (string, error) {
	out, problem, err := onServer(address, "chasen-server login", "")
	missing := strings.Contains(problem, "chasen-server: not found") || strings.Contains(problem, "chasen-server: command not found")
	notSetUp := strings.Contains(problem, "is not set up")
	switch {
	case err == nil:
		return strings.TrimSpace(out), nil
	case !missing && !notSetUp:
		return "", fmt.Errorf("cannot log in to the server through SSH: %s. The SSH user must be root, or have sudo with no password", cmp.Or(lastLineOf(problem), err.Error()))
	}
	// The output of the install and of the setup is for a person on the
	// server. Here it shows only when a step fails.
	if missing {
		fmt.Println("chasen-server is not on this server yet. Installing it.")
		script := installScript
		// CHASEN_DOWNLOADS names another place for the release: a test uses it.
		if from := os.Getenv("CHASEN_DOWNLOADS"); from != "" && !strings.Contains(from, "'") {
			script = "export CHASEN_DOWNLOADS='" + from + "'\n" + script
		}
		if out, problem, err := onServer(address, "sh", script); err != nil {
			return "", fmt.Errorf("the install of chasen-server failed:\n%s%s\nTo do it by hand, log in to the server and run: %s", out, problem, strings.TrimSpace(installScript))
		}
	}
	fmt.Println("Setting up the server.")
	// The setup says each step on stderr as it starts: show those lines now,
	// so a minute of Docker install is not a minute of silence.
	if out, problem, err := onServerShowing(address, "chasen-server setup", indent{os.Stdout}); err != nil {
		return "", fmt.Errorf("the setup of the server failed:\n%s%s\nLog in to the server and run: chasen-server setup", out, problem)
	}
	out, problem, err = onServer(address, "chasen-server login", "")
	if err != nil {
		return "", fmt.Errorf("cannot log in to the server through SSH: %s", cmp.Or(lastLineOf(problem), err.Error()))
	}
	fmt.Println("The server is ready.")
	return strings.TrimSpace(out), nil
}

// onServer runs one command on a server through SSH, as root. input goes to
// the command. The output and the errors of the command come back.
func onServer(address, command, input string) (out, problem string, err error) {
	return runOnServer(address, command, input, nil)
}

// onServerShowing is onServer for a command that says what it does on
// stderr: those lines also go to show as they come.
func onServerShowing(address, command string, show io.Writer) (out, problem string, err error) {
	return runOnServer(address, command, "", show)
}

func runOnServer(address, command, input string, show io.Writer) (out, problem string, err error) {
	ssh, err := protocol.SSHCommand(address, command)
	if err != nil {
		return "", "", err
	}
	var stdout, stderr strings.Builder
	ssh.Stdin, ssh.Stdout, ssh.Stderr = strings.NewReader(input), &stdout, &stderr
	if show != nil {
		ssh.Stderr = io.MultiWriter(&stderr, show)
	}
	err = ssh.Run()
	return stdout.String(), stderr.String(), err
}

// indent writes each line two spaces in, under the line that announced it.
type indent struct{ w io.Writer }

func (i indent) Write(p []byte) (int, error) {
	for _, line := range strings.SplitAfter(string(p), "\n") {
		if line != "" {
			io.WriteString(i.w, "  "+line)
		}
	}
	return len(p), nil
}

func lastLineOf(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// deviceLogin is the OAuth 2.0 device flow: show a page and a code to the
// user, then wait until they approve the login in a browser.
func deviceLogin(server string) (string, error) {
	var device struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		Interval   int    `json:"interval"`
	}
	resp, err := http.PostForm(server+"/oauth/device_authorization", url.Values{"client_id": {"chasen"}})
	if err != nil {
		return "", err
	}
	err = json.NewDecoder(resp.Body).Decode(&device)
	resp.Body.Close()
	if err != nil || device.DeviceCode == "" {
		return "", fmt.Errorf("%s does not answer like a Chasen server (%s)", server, resp.Status)
	}

	// The page is always on the server the user named, not on an address from the answer.
	page := server + "/oauth/device?user_code=" + url.QueryEscape(device.UserCode)
	fmt.Printf("Open this page to log in:\n  %s\nCode: %s\n", page, device.UserCode)
	for _, opener := range []string{"xdg-open", "open"} {
		if exec.Command(opener, page).Start() == nil {
			break
		}
	}

	for {
		time.Sleep(time.Duration(max(device.Interval, 1)) * time.Second)
		resp, err := http.PostForm(server+"/oauth/token", url.Values{
			"grant_type": {oauth.GrantType}, "device_code": {device.DeviceCode}, "client_id": {"chasen"},
		})
		if err != nil {
			return "", err
		}
		var answer struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&answer)
		resp.Body.Close()
		switch {
		case err != nil:
			return "", err
		case answer.AccessToken != "":
			return answer.AccessToken, nil
		case answer.Error != "authorization_pending":
			return "", fmt.Errorf("the login did not complete (%s). Try again", answer.Error)
		}
	}
}
