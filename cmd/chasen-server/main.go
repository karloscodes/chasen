// Chasen-server manages one server: it builds and runs the apps, sets their
// domains, and keeps the backups of their SQLite databases. The chasen client
// calls its HTTP API.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

const usage = `Usage: chasen-server <command>

Run these on the server, as root:
  setup --domain <base domain>   Start the proxy and the API. Print the login token
  bucket --endpoint <url> --name <bucket> --access-key-id <id>
                                 Set the S3 bucket for offsite backups. Asks for the secret key
  bucket                         Show the bucket
  check                          Check the security of the server (with no arguments)
  list                           List all apps
  backup                         Back up every app now

The API runs these for the chasen client:
  env <app>                      Replace the settings of the app with the JSON on stdin
  deploy <app> <version>         Build the tar archive on stdin and deploy it
  check <app> <version>          Build the tar archive on stdin and test it against the standard
  enable <addon> [domain]        Run fusionaly, formlander, or lognorth from its image
  restart <app>                  Start the app again with the env that was sent last
  status|logs|backup|backups|remove <app>
  history <app> [id]             The activity feed of the app, or the output of one entry
  domains <app> [add|rm <domain>]
  restore <app> [backup|live]

Internal:
  serve                          The API, the hourly backups, and the live replica
  replicate                      The live replica (serve runs this)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("chasen-server " + version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = runServer(os.Args[1:])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
