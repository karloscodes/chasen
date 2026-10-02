package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/net/websocket"
)

// `chasen ssh` opens a shell in the container of an app. It is not SSH: the
// client talks to the same API as every command, over a WebSocket (see the
// protocol package), and the API asks Docker for a terminal in the container.
// Nobody logs in to the server for it.

// shellCommand starts bash when the image has it, and sh when it does not.
// Its first line of output is its process id in the container: the API reads
// it and does not show it. Docker leaves a shell running when its client goes
// away, so the API needs the id to end it.
const shellCommand = "echo $$; command -v bash >/dev/null 2>&1 && exec bash; exec sh"

// activeContainer returns the container of an app that has the traffic:
// <app>, or <app>-next after a swap.
func activeContainer(name string) (string, error) {
	container, _ := docker("ps", "--filter", "name=^"+name+"(-next)?$", "--format", "{{.Names}}")
	if container, _, _ = strings.Cut(container, "\n"); container == "" {
		return "", fmt.Errorf("%s does not run now, so it has no container. See: chasen status", name)
	}
	return container, nil
}

// serveShell answers GET /v1/ssh. The caller checked the token.
func serveShell(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	args := r.URL.Query()["arg"]
	if len(args) != 1 || checkAppName(args[0]) != nil {
		http.Error(w, "usage: /v1/ssh?arg=<app>", http.StatusBadRequest)
		return
	}
	name := args[0]
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	websocket.Server{
		// The token is the check. A browser cannot send it, so the origin says nothing.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			container, err := activeContainer(name)
			if _, known := loadApp(name); known != nil {
				err = known
			}
			if err != nil {
				protocol.ShellCodec.Send(ws, protocol.ShellMessage{Text: true, Data: []byte(protocol.ShellError + " " + err.Error())})
				return
			}
			// The feed says who was in the container and for how long, not what they typed.
			activity, _ := startActivity(db, name, "ssh")
			start := time.Now()
			code, err := shellSession(ws, container, cols, rows)
			output := fmt.Sprintf("A shell in the container, for %s.\n", time.Since(start).Round(time.Second))
			if err != nil {
				output += "Error: " + err.Error() + "\n"
			}
			finishActivity(db, activity, err == nil && code == 0, output)
		},
	}.ServeHTTP(w, r)
}

// shellSession runs one shell in a container and connects it to the
// WebSocket of the client, until the shell ends or the client goes away. It
// returns the exit code of the shell.
func shellSession(ws *websocket.Conn, container string, cols, rows int) (int, error) {
	ws.MaxPayloadBytes = 1 << 20
	send := func(text bool, data []byte) error {
		return protocol.ShellCodec.Send(ws, protocol.ShellMessage{Text: text, Data: data})
	}
	shell, err := openShell(container, cols, rows)
	if err != nil {
		send(true, []byte(protocol.ShellError+" "+err.Error()))
		return 1, err
	}
	defer shell.conn.Close()

	// The container to the client. When the shell ends, the client gets its exit code.
	ended := make(chan int, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := shell.out.Read(buf)
			if n > 0 && send(false, buf[:n]) != nil {
				break
			}
			if err != nil {
				break
			}
		}
		code := shell.exitCode()
		send(true, fmt.Appendf(nil, "%s %d", protocol.ShellExit, code))
		ws.Close()
		ended <- code
	}()

	// The client to the container.
	for {
		var m protocol.ShellMessage
		if protocol.ShellCodec.Receive(ws, &m) != nil {
			break // the shell ended, or the client went away
		}
		if !m.Text {
			if _, err := shell.conn.Write(m.Data); err != nil {
				break
			}
			continue
		}
		var cols, rows int
		if _, err := fmt.Sscanf(string(m.Data), protocol.ShellResize+" %d %d", &cols, &rows); err == nil {
			shell.resize(cols, rows)
		}
	}
	select {
	case code := <-ended:
		return code, nil
	default:
	}
	// The client went away, and the shell still runs. End it: the kernel then
	// ends what the shell started in its terminal too.
	shell.hangUp(container)
	shell.conn.Close()
	select {
	case code := <-ended:
		return code, nil
	case <-time.After(5 * time.Second):
		return 1, nil
	}
}

// The API of Docker, on its socket. The docker command cannot give a
// terminal to a program that has none itself, and the API can.
const dockerSocket = "/var/run/docker.sock"

var dockerHTTP = &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
	},
}}

// dockerAPI sends one request to Docker and decodes the JSON answer.
func dockerAPI(method, path string, body, answer any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://docker"+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := dockerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("docker: %s", strings.TrimSpace(string(msg)))
	}
	if answer == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(answer)
}

// containerShell is a shell that runs in a container, with a terminal.
type containerShell struct {
	id   string
	pid  string    // of the shell, in the container
	conn net.Conn  // what the user types goes in
	out  io.Reader // what the terminal shows comes out
}

// hangUp ends the shell, as a terminal that closes does.
func (s *containerShell) hangUp(container string) {
	if s.pid != "" {
		docker("exec", container, "sh", "-c", "kill -HUP "+s.pid)
	}
}

// openShell starts a shell in a container, with a terminal of a size.
func openShell(container string, cols, rows int) (*containerShell, error) {
	var created struct{ Id string }
	err := dockerAPI("POST", "/containers/"+container+"/exec", map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Env": []string{"TERM=xterm-256color"},
		"Cmd": []string{"sh", "-c", shellCommand},
	}, &created)
	if err != nil {
		return nil, err
	}
	// The start of the shell turns the connection into its terminal: a plain
	// stream of bytes in both directions.
	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return nil, err
	}
	const start = `{"Detach":false,"Tty":true}`
	fmt.Fprintf(conn, "POST /exec/%s/start HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: %d\r\n\r\n%s", created.Id, len(start), start)
	out := bufio.NewReader(conn)
	resp, err := http.ReadResponse(out, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		conn.Close()
		return nil, fmt.Errorf("docker: %s", strings.TrimSpace(string(msg)))
	}
	shell := &containerShell{id: created.Id, conn: conn, out: out}
	// The first line is the process id of the shell (see shellCommand). An
	// image with no sh gives the error of Docker there: show that.
	if first, err := out.ReadString('\n'); err == nil {
		if pid := strings.TrimSpace(first); pid != "" && strings.Trim(pid, "0123456789") == "" {
			shell.pid = pid
		} else {
			shell.out = io.MultiReader(strings.NewReader(first), out)
		}
	}
	shell.resize(cols, rows)
	return shell, nil
}

func (s *containerShell) resize(cols, rows int) {
	if cols > 0 && rows > 0 {
		dockerAPI("POST", fmt.Sprintf("/exec/%s/resize?h=%d&w=%d", s.id, rows, cols), nil, nil)
	}
}

// exitCode returns the exit code of the shell after it ended.
func (s *containerShell) exitCode() int {
	var state struct{ ExitCode int }
	if err := dockerAPI("GET", "/exec/"+s.id+"/json", nil, &state); err != nil {
		return 1
	}
	return state.ExitCode
}
