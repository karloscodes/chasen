package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/karloscodes/matcha"
)

// serverConnect joins the input and the output of this command to the API of
// the server. The CLI runs it through SSH (`ssh root@server chasen-server
// connect`) and then speaks the same protocol as on the web. So a server
// needs no name in DNS, no certificate, and no open port but the one of SSH.
//
// SSH is the way in, not a second API: every command still goes through the
// API, with its token, its lock, and its history.
func serverConnect() error {
	ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", agentContainer)
	if err != nil || ip == "" {
		return errors.New("the API of this server does not run. Run: chasen-server setup")
	}
	api, err := net.Dial("tcp", net.JoinHostPort(ip, apiPort))
	if err != nil {
		return err
	}
	defer api.Close()
	go func() {
		io.Copy(api, os.Stdin)
		// The client is done. The API still sends the rest of its answer.
		api.(*net.TCPConn).CloseWrite()
	}()
	_, err = io.Copy(os.Stdout, api)
	return err
}

// serverLogin makes a login for the CLI of the person who runs this command,
// and prints its token. It is the login of the SSH way: who can run commands
// on the server as root owns it, so no browser asks for a proof. The token is
// a login like the ones of the browser: `chasen logout` ends it, and the
// token of the server itself never leaves the server.
func serverLogin() error {
	if _, err := loadServerConfig(); err != nil {
		return err
	}
	token, err := matcha.GeneratePrivateKey()
	if err != nil {
		return err
	}
	db, err := openServerDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO logins (token_sha256) VALUES (?)", hashToken(token)); err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
