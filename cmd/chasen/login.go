package main

import (
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
	"strings"
	"time"

	"github.com/karloscodes/chasen/oauth"
)

// credentials is the saved login: the API of the cloud or of a server, and the token.
type credentials struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Server is the cloud server a command goes to, when the user chose one:
	// an id, or "new", or "new:<type>". It is not saved.
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
// servers and to the cloud. Commands go to the current one, unless chasen.yml
// names a server.
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

// apiAddress turns what a person types into the address of an API: "cloud"
// is the Chasen cloud, example.com is https://api.example.com, and
// a full address stays as it is.
func apiAddress(name string) string {
	switch {
	case name == "cloud":
		return cmp.Or(os.Getenv("CHASEN_CLOUD"), cloudURL)
	case strings.Contains(name, "://"):
		return strings.TrimRight(name, "/")
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

// listServers shows the saved logins. The star marks the current one.
func listServers() error {
	saved := loadLogins()
	if len(saved.Tokens) == 0 {
		return errors.New("not logged in. " + howToLogin)
	}
	addresses := slices.Sorted(maps.Keys(saved.Tokens))
	for _, address := range addresses {
		mark := " "
		if address == saved.Current {
			mark = "*"
		}
		fmt.Println(mark, address)
	}
	return nil
}

// useServer makes another saved login the current one.
func useServer(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: chasen use <server>. chasen servers lists them")
	}
	saved := loadLogins()
	address := apiAddress(args[0])
	if _, ok := saved.Tokens[address]; !ok {
		return fmt.Errorf("you are not logged in to %s. Run: chasen servers", address)
	}
	saved.Current = address
	if err := saved.save(); err != nil {
		return err
	}
	fmt.Println("Commands now go to", address)
	return nil
}

// cloudURL is the Chasen cloud that `chasen login` uses. CHASEN_CLOUD sets
// another one.
var cloudURL = "https://cloud.chasenhq.com"

const howToLogin = "Run: chasen login. For your own server, run: chasen add server <domain>"

// login logs in to the Chasen cloud.
func login(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: chasen login. For your own server: chasen add server <domain>")
	}
	return connect(apiAddress("cloud"))
}

// addServer logs in to a server you host yourself. The server example.com
// has its API on https://api.example.com.
func addServer(args []string) error {
	if len(args) != 2 || args[0] != "server" {
		return errors.New("usage: chasen add server <domain>")
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
	page := server + "/oauth/device?user_code=" + device.UserCode
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
