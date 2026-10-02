package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/websocket"
)

// Two requests are not commands with a text output: the shell and the
// download of a backup. Both are GET requests with the token of the client,
// and the cloud passes both on like a command.
//
//	GET /v1/ssh?arg=<app>&cols=80&rows=24        a WebSocket: a shell in the container of the app
//	GET /v1/download?arg=<app>&arg=<backup>      a tar.gz file: the databases of one backup
const (
	ShellPath    = "/v1/ssh"
	DownloadPath = "/v1/download"
)

// The shell is a WebSocket, because it passes every proxy that is in front
// of a server. A binary message carries the bytes of the terminal, in both
// directions. A text message is one of these:
//
//	client: "resize <cols> <rows>"     the terminal of the user has a new size
//	server: "exit <code>"              the shell ended. It is the last message
//	server: "error <message>"          there is no shell. It is the last message
const (
	ShellResize = "resize"
	ShellExit   = "exit"
	ShellError  = "error"
)

// ShellMessage is one message of the shell: the bytes of the terminal, or a
// text like "resize 80 24".
type ShellMessage struct {
	Text bool
	Data []byte
}

// ShellCodec sends and receives a ShellMessage on a WebSocket. The client,
// the server, and the mock server use it, so they agree on the messages.
var ShellCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		if m := v.(ShellMessage); m.Text {
			return m.Data, websocket.TextFrame, nil
		} else {
			return m.Data, websocket.BinaryFrame, nil
		}
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		*v.(*ShellMessage) = ShellMessage{Text: payloadType == websocket.TextFrame, Data: data}
		return nil
	},
}

// Shell is an open shell in the container of an app.
type Shell struct{ ws *websocket.Conn }

// Shell opens a shell in the container of an app. cols and rows are the size
// of the terminal of the user.
func (c Client) Shell(app string, cols, rows int) (*Shell, error) {
	_, origin := c.api()
	address, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	address.Scheme = strings.Replace(address.Scheme, "http", "ws", 1) // https becomes wss
	address.Path = ShellPath
	address.RawQuery = url.Values{"arg": {app}, "cols": {strconv.Itoa(cols)}, "rows": {strconv.Itoa(rows)}}.Encode()
	config, err := websocket.NewConfig(address.String(), origin)
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Server != "" {
		config.Header.Set(ServerHeader, c.Server)
	}
	var ws *websocket.Conn
	if IsSSH(c.URL) {
		// The shell has a connection of its own, through ssh, for as long as it is open.
		var conn net.Conn
		if conn, err = dialSSH(c.URL); err == nil {
			if ws, err = websocket.NewClient(config, conn); err != nil {
				conn.Close()
			}
		}
	} else {
		ws, err = websocket.DialConfig(config)
	}
	// The answer was not a WebSocket: an older server, or a token it refuses.
	if dial := (*websocket.DialError)(nil); (errors.As(err, &dial) && dial.Err == websocket.ErrBadStatus) || err == websocket.ErrBadStatus {
		return nil, fmt.Errorf("%s refused the shell. It needs chasen-server 0.5 or newer, and a valid login", c.URL)
	}
	if err != nil {
		return nil, c.errSSH(err)
	}
	ws.MaxPayloadBytes = 1 << 20
	return &Shell{ws}, nil
}

// Write sends what the user typed.
func (s *Shell) Write(keys []byte) (int, error) {
	return len(keys), ShellCodec.Send(s.ws, ShellMessage{Data: keys})
}

// Resize tells the shell the new size of the terminal.
func (s *Shell) Resize(cols, rows int) error {
	return ShellCodec.Send(s.ws, ShellMessage{Text: true, Data: fmt.Appendf(nil, "%s %d %d", ShellResize, cols, rows)})
}

// Next returns the next output of the shell. When the shell ended, done is
// true and code is its exit code.
func (s *Shell) Next() (output []byte, code int, done bool, err error) {
	var m ShellMessage
	if err := ShellCodec.Receive(s.ws, &m); err != nil {
		return nil, 0, false, err
	}
	if !m.Text {
		return m.Data, 0, false, nil
	}
	if rest, ok := strings.CutPrefix(string(m.Data), ShellExit+" "); ok {
		code, err = strconv.Atoi(rest)
		return nil, code, true, err
	}
	if rest, ok := strings.CutPrefix(string(m.Data), ShellError+" "); ok {
		return nil, 0, false, errors.New(rest)
	}
	return nil, 0, false, fmt.Errorf("the server sent a message this client does not know: %q", m.Data)
}

func (s *Shell) Close() error { return s.ws.Close() }

// Download asks for the databases of one backup of an app, as a tar.gz
// file. An empty backup means the newest one. The caller closes the body.
// The name of the file is in the Content-Disposition header.
func (c Client) Download(ctx context.Context, app, backup string) (*http.Response, error) {
	query := url.Values{"arg": {app}}
	if backup != "" {
		query.Add("arg", backup)
	}
	resp, err := c.request(ctx, "GET", DownloadPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusUnauthorized:
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return nil, fmt.Errorf("%s: %s: %s", c.URL, resp.Status, strings.TrimSpace(string(msg)))
}
