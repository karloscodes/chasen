// Package oauth is the OAuth 2.0 Device Authorization Grant (RFC 8628) for the
// chasen client. chasen-server and the cloud both mount it.
//
//  1. The client asks for a device code:  POST /oauth/device_authorization
//  2. The user opens the page and types their key:  GET and POST /oauth/device
//  3. The client polls for the access token:  POST /oauth/token
package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	GrantType = "urn:ietf:params:oauth:grant-type:device_code"
	lifetime  = 10 * time.Minute
	interval  = 2 // seconds between polls
)

// Server gives access tokens to clients that a user approved.
type Server struct {
	// Name tells the user what they log in to. The page shows it.
	Name string
	// Authenticate checks the key that the user types on the page. It returns
	// who the user is.
	Authenticate func(key string) (subject string, ok bool)
	// Issue makes and stores an access token for the subject.
	Issue func(subject string) (token string, err error)

	mu     sync.Mutex
	grants map[string]*grant // by device code
}

// A grant is one login that waits for the user. subject is empty until the
// user approves it.
type grant struct {
	userCode string
	subject  string
	expires  time.Time
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /oauth/device_authorization", s.authorize)
	mux.HandleFunc("GET /oauth/device", s.page)
	mux.HandleFunc("POST /oauth/device", s.approve)
	mux.HandleFunc("POST /oauth/token", s.token)
}

// The user reads the code in a terminal and types it in a browser, so it has
// no letters or digits that look the same.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = userCodeAlphabet[int(b[i])%len(userCodeAlphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	rand.Read(b)
	deviceCode := hex.EncodeToString(b)
	g := &grant{userCode: newUserCode(), expires: time.Now().Add(lifetime)}

	s.mu.Lock()
	if s.grants == nil {
		s.grants = map[string]*grant{}
	}
	for code, old := range s.grants {
		if time.Now().After(old.expires) {
			delete(s.grants, code)
		}
	}
	s.grants[deviceCode] = g
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 g.userCode,
		"verification_uri":          "/oauth/device",
		"verification_uri_complete": "/oauth/device?user_code=" + g.userCode,
		"expires_in":                int(lifetime.Seconds()),
		"interval":                  interval,
	})
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Chasen login</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:28rem;margin:4rem auto;padding:0 1rem}
input,button{font:inherit;width:100%;box-sizing:border-box;padding:.5rem;margin:.25rem 0 1rem}</style>
<h1>Log in to {{.Name}}</h1>
{{if .Done}}<p>The login is complete. Go back to your terminal.</p>
{{else}}{{if .Error}}<p><strong>{{.Error}}</strong></p>{{end}}
<p>Continue only if you started <code>chasen login</code> and your terminal shows this code.</p>
<form method="post" action="/oauth/device">
<label>Code <input name="user_code" value="{{.UserCode}}" autocomplete="off" required></label>
<label>Your key <input name="key" type="password" required autofocus></label>
<button>Log in</button>
</form>{{end}}`))

type pageData struct {
	Name, UserCode, Error string
	Done                  bool
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	pageTemplate.Execute(w, pageData{Name: s.Name, UserCode: r.URL.Query().Get("user_code")})
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	userCode := strings.ToUpper(strings.TrimSpace(r.FormValue("user_code")))
	refuse := func(reason string) {
		w.WriteHeader(http.StatusForbidden)
		pageTemplate.Execute(w, pageData{Name: s.Name, UserCode: userCode, Error: reason})
	}
	subject, ok := s.Authenticate(strings.TrimSpace(r.FormValue("key")))
	if !ok {
		refuse("That key is not correct.")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.grants {
		if g.userCode == userCode && time.Now().Before(g.expires) {
			g.subject = subject
			pageTemplate.Execute(w, pageData{Name: s.Name, Done: true})
			return
		}
	}
	refuse("That code is not correct, or it is too old. Run chasen login again.")
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("grant_type") != GrantType {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	deviceCode := r.FormValue("device_code")

	s.mu.Lock()
	g, ok := s.grants[deviceCode]
	switch {
	case !ok || time.Now().After(g.expires):
		delete(s.grants, deviceCode)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
		return
	case g.subject == "":
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
		return
	}
	// A device code gives one token.
	delete(s.grants, deviceCode)
	s.mu.Unlock()

	token, err := s.Issue(g.subject)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "server_error", "error_description": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "bearer"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintln(w, err)
	}
}
