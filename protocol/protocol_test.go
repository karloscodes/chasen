package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRun(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "not authorized", http.StatusUnauthorized)
			return
		}
		stdin, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s\n", r.URL.Path, strings.Join(r.URL.Query()["arg"], ","), stdin)
		if r.URL.Path == "/v1/logs" {
			return // the connection drops before the command ends
		}
		fmt.Fprintf(w, "%s3\n", ExitMarker)
	}))
	defer api.Close()

	t.Run("it sends the command, the arguments, and stdin, and returns the output and the exit code", func(t *testing.T) {
		var out strings.Builder

		code, err := Client{URL: api.URL, Token: "good"}.Run(context.Background(), "deploy", []string{"shop", "a b"}, strings.NewReader("tar"), &out)

		if err != nil || code != 3 || out.String() != "/v1/deploy shop,a b tar\n" {
			t.Errorf("Run = %d, %v, output %q", code, err, out.String())
		}
	})

	t.Run("a refused token is ErrUnauthorized", func(t *testing.T) {
		_, err := Client{URL: api.URL, Token: "bad"}.Run(context.Background(), "list", nil, nil, io.Discard)

		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Run error = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("a response without the exit marker is an error", func(t *testing.T) {
		_, err := Client{URL: api.URL, Token: "good"}.Run(context.Background(), "logs", []string{"shop"}, nil, io.Discard)

		if err == nil {
			t.Error("Run returned no error for a response that stops halfway")
		}
	})
}
