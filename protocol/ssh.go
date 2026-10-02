package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// A server that you host has two ways in. Its API has an address on the web
// (https://api.example.com), or the CLI reaches the same API through SSH:
//
//	ssh://root@203.0.113.5
//
// SSH is only the way to the API. The CLI runs the ssh program, and the
// program runs `chasen-server connect` on the server, which joins the
// connection to the API. The requests, the commands, and the answers are the
// same as on the web. So a server needs no name in DNS, no certificate, and
// no open port but the one of SSH.
//
// The ssh program is the one of the user: its config, its keys, its agent,
// and its known hosts all apply.

// IsSSH reports an address that goes through SSH.
func IsSSH(address string) bool { return strings.HasPrefix(address, "ssh://") }

// RemoteConnect is the command that the CLI runs on the server to reach the API.
const RemoteConnect = "chasen-server connect"

// SSHCommand returns the ssh command that runs one command on the server of
// the address, as root: a user that is not root needs sudo with no password.
func SSHCommand(address, command string) (*exec.Cmd, error) {
	server, err := url.Parse(address)
	if err != nil || server.Scheme != "ssh" || server.Hostname() == "" || strings.HasPrefix(server.Hostname(), "-") || strings.HasPrefix(server.User.Username(), "-") {
		return nil, fmt.Errorf("invalid SSH address %q: use the form ssh://root@203.0.113.5", address)
	}
	if strings.Contains(command, "'") {
		return nil, fmt.Errorf("the command %q has a quote", command)
	}
	args := []string{"-T"}
	if port := server.Port(); port != "" {
		args = append(args, "-p", port)
	}
	target := server.Hostname()
	if user := server.User.Username(); user != "" {
		target = user + "@" + target
	}
	// The shell of the user on the server can be any shell: sh runs the line.
	remote := fmt.Sprintf(`sh -c 'if [ "$(id -u)" = 0 ]; then exec %s; else exec sudo -n %s; fi'`, command, command)
	return exec.Command("ssh", append(args, target, remote)...), nil
}

// sshConn is the connection to the API of a server through the ssh program:
// what the client writes goes to the input of ssh, and what it reads comes
// from its output.
type sshConn struct {
	ssh    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	close  sync.Once
}

func dialSSH(address string) (net.Conn, error) {
	ssh, err := SSHCommand(address, RemoteConnect)
	if err != nil {
		return nil, err
	}
	// The messages of ssh are for the person: a password question, a host key
	// that changed, a login that the server refuses.
	ssh.Stderr = os.Stderr
	conn := &sshConn{ssh: ssh}
	if conn.input, err = ssh.StdinPipe(); err != nil {
		return nil, err
	}
	if conn.output, err = ssh.StdoutPipe(); err != nil {
		return nil, err
	}
	if err := ssh.Start(); err != nil {
		return nil, fmt.Errorf("cannot run ssh: %w", err)
	}
	return conn, nil
}

func (c *sshConn) Read(p []byte) (int, error)  { return c.output.Read(p) }
func (c *sshConn) Write(p []byte) (int, error) { return c.input.Write(p) }

func (c *sshConn) Close() error {
	c.close.Do(func() {
		c.input.Close()
		c.ssh.Process.Kill()
		go c.ssh.Wait()
	})
	return nil
}

// The connection is a pair of pipes: it has no addresses and no deadlines.
type sshAddr struct{}

func (sshAddr) Network() string { return "ssh" }
func (sshAddr) String() string  { return "ssh" }

func (c *sshConn) LocalAddr() net.Addr              { return sshAddr{} }
func (c *sshConn) RemoteAddr() net.Addr             { return sshAddr{} }
func (c *sshConn) SetDeadline(time.Time) error      { return nil }
func (c *sshConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshConn) SetWriteDeadline(time.Time) error { return nil }

// The requests through SSH go to this name. Nothing resolves it: the
// connection is the one of ssh.
const sshAPI = "http://chasen-server"

// sshClients keeps one HTTP client for each SSH address, so the commands of
// one process, like the screen that asks the server every few seconds, use
// one ssh connection and not a new one each time.
var sshClients sync.Map

// api returns the HTTP client that reaches the API of the server, and the
// address that the requests go to.
func (c Client) api() (*http.Client, string) {
	if !IsSSH(c.URL) {
		return http.DefaultClient, c.URL
	}
	address := c.URL
	client, _ := sshClients.LoadOrStore(address, &http.Client{Transport: &http.Transport{
		// The connection outlives the request that opened it: no context here.
		DialContext:     func(context.Context, string, string) (net.Conn, error) { return dialSSH(address) },
		IdleConnTimeout: 90 * time.Second,
	}})
	return client.(*http.Client), sshAPI
}

// errSSH says where a failed request went, in the words of the user.
func (c Client) errSSH(err error) error {
	if err == nil || !IsSSH(c.URL) {
		return err
	}
	return errors.New("cannot reach the server through SSH (" + c.URL + "): " + err.Error() + ". Does ssh log in to it, and does it have chasen-server 0.6 or newer?")
}
