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
  settings                       Show the settings of the server
  settings auto_update on|off    Turn the nightly update on or off
  settings heartbeat_url <url>   Call this URL after each hourly backup that worked. "" turns it off
  token                          Print the login token
  update                         Install the newest release now. A timer does this each night
  check                          Check the security of the server (with no arguments)
  list                           List all apps
  load                           Show the load, the memory, and the disk of the server
  quiet-hour                     Print the hour of the day with the fewest requests, from the last week
  backup                         Back up every app now
  auto-update [app]              Deploy the newest image of each app deployed with --auto-update,
                                 now. The API does it each night at 05:30 UTC
  adopt <app>                    Take over an app that matcha runs on this server. Nothing restarts
  adopt --undo <app>             Give it back to matcha

The API runs these for the chasen client:
  deploy <app> <version>         Deploy the image of the settings. Stdin: the settings as one line of
                                 JSON, then the files of a website as tar.gz
  check <app> <version>          Test that image against the standard. Stdin like deploy
  enable <addon> [domain]        Run fusionaly, formlander, or lognorth from its image
  restart <app>                  Start the app again. Stdin: new settings, or nothing to keep the last ones
  run <app> <command> [args]     Run one command in the container of the app. No shell, no input
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
