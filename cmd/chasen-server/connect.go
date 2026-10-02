package main

import (
	"errors"
	"io"
	"net"
	"os"
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
