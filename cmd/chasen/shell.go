package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/term"
)

// shell is `chasen ssh`: a shell in the container of the app, in this
// terminal. It is not SSH. The keys go to the same API as every command, and
// the server opens the shell in the container (see protocol.Shell).
func shell(creds credentials, app string) error {
	// One plain command first: it gives the usual errors for a login that is
	// not valid any more and for an app that is not deployed.
	var reason strings.Builder
	code, err := protocol.Client(creds).Run(context.Background(), "status", []string{app}, nil, &reason)
	if errors.Is(err, protocol.ErrUnauthorized) {
		return errUnauthorized
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return errors.New(strings.TrimPrefix(strings.TrimSpace(reason.String()), "Error: "))
	}
	stdin, stdout := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	cols, rows, _ := term.GetSize(stdout)
	session, err := protocol.Client(creds).Shell(app, cols, rows)
	if err != nil {
		return err
	}
	defer session.Close()

	// A terminal sends each key at once, and the shell on the other side
	// shows it. Without a terminal (a pipe), the lines of the input go as they are.
	restore := func() {}
	if term.IsTerminal(stdin) {
		state, err := term.MakeRaw(stdin)
		if err != nil {
			return err
		}
		restore = func() { term.Restore(stdin, state) }
		defer restore()
		resized := make(chan os.Signal, 1)
		signal.Notify(resized, syscall.SIGWINCH)
		defer signal.Stop(resized)
		go func() {
			for range resized {
				if cols, rows, err := term.GetSize(stdout); err == nil {
					session.Resize(cols, rows)
				}
			}
		}()
	}
	go func() {
		io.Copy(session, os.Stdin)
		// The input ended (a pipe): tell the shell, as Ctrl+D does.
		session.Write([]byte{4})
	}()

	for {
		output, code, done, err := session.Next()
		if err != nil {
			restore()
			return fmt.Errorf("the shell ended: %w", err)
		}
		if done {
			restore()
			if code != 0 {
				os.Exit(code)
			}
			return nil
		}
		os.Stdout.Write(output)
	}
}

var backupFileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-[0-9]{8}T[0-9]{6}Z\.tar\.gz$`)

// downloadBackup is `chasen download [backup]`: it saves the databases of one
// backup of the app in the current directory, as a tar.gz file.
func downloadBackup(creds credentials, app, backup string) error {
	resp, err := protocol.Client(creds).Download(context.Background(), app, backup)
	if errors.Is(err, protocol.ErrUnauthorized) {
		return errUnauthorized
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The server names the file after the app and the backup. Only that form
	// is accepted: a name from the network must not choose where the file goes.
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	name := params["filename"]
	if !backupFileRe.MatchString(name) {
		return fmt.Errorf("the server sent a file with the name %q. Want <app>-<backup>.tar.gz", name)
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s exists already. Move it away first", name)
	}
	if err != nil {
		return err
	}
	size, err := io.Copy(file, resp.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("the download stopped before the end, and nothing was saved: %w", err)
	}
	fmt.Printf("Saved %s (%s): the SQLite databases of the backup, ready to open.\nUnpack it: tar -xzf %s\n", name, megabytes(size), name)
	return nil
}

func megabytes(bytes int64) string {
	if bytes < 1<<20 {
		return fmt.Sprintf("%d kB", (bytes+1023)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
}
