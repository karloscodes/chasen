// Chasen is the client. It deploys the app in the current directory to a
// server that runs chasen-server. It calls the HTTP API of chasen-server.
package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/term"
)

var version = "dev"

const usage = `Usage: chasen <command>

  chasen                 With no command: the screen of your server. Its load, and the state,
                         history, backups, domains, and logs of each app. Keys restart and restore

  add server <user>@<host>   Log in to your own server through SSH, with no deploy
  add server <domain>    Use your own server through its address on the web, with a browser
  servers                List the servers you are logged in to, with a number. The star marks
                         the current one
  use [server]           Make another server the current one: its number in the list, or a part
                         of its address. With nothing, it shows the list and asks
  logout                 Forget the login, here and on the server
  report                 Something is wrong with Chasen? Open an issue, with your version filled in
  update                 Install the newest chasen. chasen tells you when there is one

Run these in the directory of your app:
  deploy                 Build the image of the current git commit, push it, and deploy it.
  deploy <user>@<host>   The same, to that server, through SSH. The first time, the server
                         gets Chasen and this computer gets its login: no other step
                         A directory with an index.html and no Dockerfile is a website
                         --tag <tag> deploys an image that is already in the registry. No build
                         --domain <domain> gives a new app its domain. A server with no base
                         domain needs it at the first deploy of each app
                         A folder with image: in chasen.yml and no Dockerfile: the newest image
  check                  Test the current git commit against the standard. Changes nothing live
  restart                Start the app again with the env and the secrets of now. Same image
  rollback               Start the version before the current one again, in seconds: the server
                         keeps its image. The data stays as it is. Again goes forward
  secrets edit           Change the secrets of the app in your editor. They stay in the repository,
                         encrypted, in chasen.secrets.enc. The key is chasen.key, or CHASEN_KEY in CI
  secrets                List the names of the secrets. secrets show prints the values too
  status                 Show the version, the URLs, and the last backup
  logs                   Follow the app logs
  run <command>          Run one command in the container of the app: chasen run bin/rails db:migrate
                         No input: for a console that waits for lines, use ssh
  ssh                    Open a shell in the container of the app. It is not SSH: nobody logs in
                         to the server
  history [id]           Show the deploys and changes of the app, or the output of one
  domains                List the domains
  domains add <domain>   Add a custom domain
  domains rm <domain>    Remove a custom domain
  backup                 Back up the SQLite databases now
  backups                List the backups
  verify                 Prove that the copies restore: the live replica and the newest snapshot
                         are restored next to the real databases, checked, and removed
  download [backup]      Save the databases of a backup here, as a tar.gz file (default: the newest one)
  restore [backup]       Restore a backup (default: the newest one)
  restore live           Restore the newest state from the live replica
  remove                 Stop the app. Keeps the data and the backups
  list                   List all apps on the server
  load                   Show the load, the memory, and the disk of the server
  overview               Each app of the server: its state, its version, and the change that runs now
                         --json: every server you are logged in to, with its apps and alerts
  alerts                 What is wrong with the server, or puts it at risk: apps that are down,
                         late backups, a full disk, SSH with passwords, no firewall, no updates
                         --waybar: the alerts of every server, for the top bar of Omarchy
  bucket                 Show where the backups of the server go
  bucket --endpoint <url> --name <bucket> --access-key-id <id> [--region <region>]
                         Send the backups to an S3 bucket too, and start the live replica.
                         It asks for the secret access key

Apps that others release run from their image, in any directory:
  deploy <image> [<domain>] [<user>@<host>]
                         Deploy the newest image, like ghcr.io/acme/chat, at that
                         domain. Nothing of the directory counts. The app is named after the
                         image, or -a <app>. Run it again to update: the settings of the app stay

Addons run from a ready image, with the same backups:
  enable <addon> [domain]   Run fusionaly, formlander, or lognorth. Run it again to update
  -a <app> <command>        Run a command for an addon, or for an app of another directory
`

func main() {
	if len(os.Args) < 2 {
		// With no command, a person at a terminal gets the screen of the apps.
		// Without a login there is nothing to show yet: then it is the usage.
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			if app, err := loadAppFile(); err == nil {
				if creds, err := loadCredentials(app.Server); err == nil {
					screen := serverScreen(creds, directoryApp(app))
					screen.update = newerRelease()
					if err := runScreen(screen); err != nil {
						fmt.Fprintln(os.Stderr, "Error:", err)
						os.Exit(1)
					}
					if screen.update != "" {
						autoUpdate(screen.update)
					}
					return
				}
			}
		}
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "report":
		err = report()
	case "update":
		err = update()
	case "version":
		fmt.Println("chasen " + version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = runClient(os.Args[1:])
		if latest := newerRelease(); err == nil && latest != "" {
			autoUpdate(latest)
		}
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// directoryApp returns the name of the app of the current directory, or ""
// when the directory is not an app: it has no Dockerfile, no index.html, and
// no chasen.yml.
func directoryApp(app appFile) string {
	for _, file := range []string{"Dockerfile", "index.html", "chasen.yml"} {
		if _, err := os.Stat(file); err == nil {
			return app.Name
		}
	}
	return ""
}

// issuesURL is where a report goes.
const issuesURL = "https://github.com/karloscodes/chasen/issues/new"

// reportURL is the page of a new issue, with the questions that every report
// answers and the facts that the program knows: its version and the system.
// It has nothing about the server and no token.
// what is the command and its output, when the program has them.
func reportURL(what ...string) string {
	body := "**What I ran**\n\n```\n\n```\n\n**What I saw**\n\n```\n\n```\n\n**What I expected**\n\n\n\n---\n" +
		"chasen " + version + " on " + runtime.GOOS + "/" + runtime.GOARCH + "\n" +
		"For a failed deploy, `chasen history` lists the entries and `chasen history <id>` prints the output of one.\n"
	if len(what) > 0 {
		// A URL has a limit. The end of the output says what went wrong.
		seen := what[0]
		if len(seen) > 4000 {
			seen = "…" + seen[len(seen)-4000:]
		}
		body = "**What I ran, and what I saw**\n\n" + seen + "\n\n**What I expected**\n\n\n\n---\n" +
			"chasen " + version + " on " + runtime.GOOS + "/" + runtime.GOARCH + ", from the screen\n"
	}
	return issuesURL + "?" + url.Values{"body": {body}}.Encode()
}

// report opens a new issue in the browser.
func report() error {
	page := reportURL()
	fmt.Printf("Open this page to report the problem:\n  %s\n\nYour version and your system are filled in. Add what you ran and what you saw.\n", page)
	for _, opener := range []string{"xdg-open", "open"} {
		if exec.Command(opener, page).Start() == nil {
			break
		}
	}
	return nil
}
