package mock

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/net/websocket"
)

// shell answers GET /v1/ssh with a shell that runs nothing: it repeats each
// line. It is enough to work on `chasen ssh`: the keys, the echo, the exit.
func (s *Server) shell(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("arg")
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			send := func(text bool, format string, args ...any) {
				protocol.ShellCodec.Send(ws, protocol.ShellMessage{Text: text, Data: fmt.Appendf(nil, format, args...)})
			}
			s.mu.Lock()
			known := s.app(name) != nil
			s.mu.Unlock()
			if !known {
				send(true, "%s app %q is not deployed", protocol.ShellError, name)
				return
			}
			s.mu.Lock()
			s.app(name).record("ssh", "succeeded", "A shell in the container.\n")
			s.mu.Unlock()
			send(false, "The mock server has no container. This shell repeats each line. exit ends it.\r\n%s$ ", name)
			var line []byte
			for {
				var m protocol.ShellMessage
				if protocol.ShellCodec.Receive(ws, &m) != nil {
					return
				}
				if m.Text {
					continue // a new size of the terminal
				}
				for _, key := range m.Data {
					switch {
					case key == '\r' || key == '\n':
						typed := strings.TrimSpace(string(line))
						line = line[:0]
						if code, ok := strings.CutPrefix(typed+" ", "exit "); ok {
							send(false, "\r\n")
							send(true, "%s %d", protocol.ShellExit, atoi(code))
							return
						}
						send(false, "\r\nyou typed: %s\r\n%s$ ", typed, name)
					case key == 4: // Ctrl+D
						send(false, "\r\n")
						send(true, "%s 0", protocol.ShellExit)
						return
					case key == 127 && len(line) > 0: // backspace
						line = line[:len(line)-1]
						send(false, "\b \b")
					case key >= ' ' && key != 127:
						line = append(line, key)
						send(false, "%c", key)
					}
				}
			}
		},
	}.ServeHTTP(w, r)
}

// atoi reads the exit code of "exit 3". Anything else is 0.
func atoi(s string) int {
	var n int
	fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}

// download answers GET /v1/download with a small archive: one made-up
// database for the backup.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	args := r.URL.Query()["arg"]
	s.mu.Lock()
	var a *app
	if len(args) > 0 {
		a = s.app(args[0])
	}
	var backups []string
	if a != nil {
		backups = slices.Clone(a.backups)
	}
	s.mu.Unlock()
	switch {
	case a == nil:
		http.Error(w, "the app is not deployed", http.StatusNotFound)
		return
	case len(backups) == 0:
		http.Error(w, a.name+" has no backup yet. Make one: chasen backup", http.StatusNotFound)
		return
	}
	stamp := backups[0]
	if len(args) > 1 {
		if stamp = args[1]; !slices.Contains(backups, stamp) {
			http.Error(w, "no backup "+stamp+". Run `chasen backups` to see the list", http.StatusNotFound)
			return
		}
	}
	s.mu.Lock()
	a.record("download "+stamp, "succeeded", "The databases of the backup "+stamp+" went to the client.\n")
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.tar.gz"`, a.name, stamp))
	zip := gzip.NewWriter(w)
	archive := tar.NewWriter(zip)
	content := "The mock server has no database. This file stands in for the one of " + a.name + ".\n"
	archive.WriteHeader(&tar.Header{Name: "storage/db.sqlite3", Mode: 0600, Size: int64(len(content))})
	archive.Write([]byte(content))
	archive.Close()
	zip.Close()
}
