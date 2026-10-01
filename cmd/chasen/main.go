// Chasen is the client. It deploys the app in the current directory to a
// server that runs chasen-server. It calls the HTTP API of chasen-server.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

const usage = `Usage: chasen <command>

  add server <domain>    Log in to your own server, with a browser
  login                  Log in to the Chasen cloud instead
  servers                List the servers you are logged in to. The star marks the current one
  use <server>           Make another server the current one: a name from the list
  logout                 Forget the login, here and on the server

Run these in the directory of your app:
  deploy                 Build the image of the current git commit, push it, and deploy it.
                         A directory with an index.html and no Dockerfile is a website
                         --tag <tag> deploys an image that is already in the registry. No build
                         In the cloud, the first deploy of an app asks for its server:
                         --on <id> uses one of your servers, --new creates one (--new=cx33, --new=@ash)
  check                  Test the current git commit against the standard. Changes nothing live
  restart                Start the app again with the env and secrets of chasen.yml. Same image
  status                 Show the version, the URLs, and the last backup
  logs                   Follow the app logs
  history [id]           Show the deploys and changes of the app, or the output of one
  domains                List the domains
  domains add <domain>   Add a custom domain
  domains rm <domain>    Remove a custom domain
  backup                 Back up the SQLite databases now
  backups                List the backups
  restore [backup]       Restore a backup (default: the newest one)
  restore live           Restore the newest state from the live replica
  remove                 Stop the app. Keeps the data and the backups
  list                   List all apps on the server

Addons run from a ready image, with the same backups:
  enable <addon> [domain]   Run fusionaly, formlander, or lognorth. Run it again to update
  -a <app> <command>        Run a command for an addon, or for an app of another directory
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("chasen " + version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = runClient(os.Args[1:])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
