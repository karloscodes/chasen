# Commands, files, and limits

## Commands

| Command | What it does |
|---|---|
| `chasen login` | Log in to the Chasen cloud, with a browser |
| `chasen deploy <user>@<host>` | Deploy to that server, through SSH. The first time, the server gets `chasen-server` and this computer gets its login |
| `chasen add server <user>@<host>` | Log in to your own server through SSH, with no deploy |
| `chasen add server <domain>` | Use your own server through its address on the web (`https://api.<domain>`), with a login in the browser |
| `chasen servers` | List the servers you are logged in to. The star marks the current one |
| `chasen use [server]` | Make another server the current one: its number in `chasen servers`, a part of its address, or `cloud`. With nothing, it shows the list and asks |
| `chasen logout` | Forget the login, here and on the server |
| `chasen update` | Install the newest release of the CLI, after a check of its checksum. `chasen` tells you when there is one |
| `chasen report` | Something is wrong with Chasen? Open a new issue on GitHub, with your version and your system filled in. It sends nothing by itself |
| `chasen deploy` | Build the image of the current git commit, push it, and deploy it. Or deploy a static website. In a folder with an `image:` in `chasen.yml` and no `Dockerfile`: deploy the newest image, with no build. `--tag <tag>` deploys an image that is already in the registry. `--domain <domain>` gives a new app its domain. In the cloud: `--on <id>` or `--new[=type@location]` picks the server of a new app |
| `chasen check` | Test the current git commit against the standard. Changes nothing live |
| `chasen restart` | Start the app again with the env and secrets of `chasen.yml`, from the image it has |
| `chasen rollback` | Start the version before the current one again, in seconds: the server keeps its image. The data stays as it is: `chasen restore` brings back a backup. A second rollback goes forward again |
| `chasen secrets edit` | Change the secrets of the app in your editor. They stay in the repository, encrypted |
| `chasen secrets` | List the names of the secrets. `chasen secrets show` prints the values too |
| `chasen status` | Show the version, the state, the URLs, the last backup, and the replica |
| `chasen logs` | Follow the app logs |
| `chasen enable <addon> [domain]` | Run fusionaly, formlander, or lognorth from its image |
| `chasen -a <app> <command>` | Run a command for an addon, or for an app of another directory |
| `chasen run <command>` | Run one command in the container of the app, with its env and its storage: `chasen run bin/rails db:migrate`. The output comes back as it is written, and the exit code is the one of the command. The history keeps it. See "Run a command in the app" below |
| `chasen ssh` | Open a shell in the container of the app. See "A shell in the app" below |
| `chasen history [id]` | Show the deploys and changes of the app, or the output of one |
| `chasen domains [add\|rm <domain>]` | List or change the domains |
| `chasen backup` | Make a snapshot now |
| `chasen backups` | List the backups |
| `chasen verify` | Prove that the copies restore: the live replica and the newest snapshot are restored next to the real databases, checked, and removed |
| `chasen download [backup]` | Save the databases of a backup in the current directory, as a `tar.gz` file. The default is the newest backup |
| `chasen restore [backup\|live]` | Restore a backup |
| `chasen remove` | Stop the app. Keeps the data and the backups |
| `chasen list` | List all apps on the server |
| `chasen load` | Show how busy the server is: the load, the memory in use, and the disk of the apps |
| `chasen alerts` | Show what is wrong with the server, or puts it at risk. See "Alerts" below |
| `chasen bucket` | Show where the backups of the server go. With `--endpoint`, `--name`, and `--access-key-id`: send them to an S3 bucket too |

On the server, as root:

| Command | What it does |
|---|---|
| `chasen-server setup --domain <domain>` | Start the proxy and the API, and the timer of the nightly update |
| `chasen-server bucket ...` | Set the S3 bucket for offsite backups. From your computer it is `chasen bucket ...` |
| `chasen-server update` | Install the newest release now. The timer does this each night |
| `chasen-server check` | The same as `chasen alerts`, on the server |
| `chasen-server settings quiet_alerts <name,name>` | Turn alerts off by their name: `firewall` when the firewall of your provider does that job. An empty value turns them all on again |
| `chasen-server list` | List all apps |
| `chasen-server quiet-hour` | Print the hour of the day with the fewest requests in the last week, for work that takes the server away |
| `chasen-server backup` | Back up every app now |
| `chasen-server adopt <app>` | Take over an app that [matcha](https://github.com/karloscodes/matcha) runs on this server. See "Coming from matcha" below |

## Run a command in the app

```bash
chasen run bin/rails db:migrate
chasen run python manage.py createsuperuser --noinput
chasen run sh -c "ls -la /storage | head"
```

The command runs in the container that has the traffic, so it has the env, the secrets, and the storage of the app.

- **No shell on the way.** Chasen passes the words to the container as they are. For a pipe, a redirect, or a variable, call the shell yourself: `chasen run sh -c "..."`.
- **No input.** A program that waits for lines, like a console, gets none and ends or hangs. Use `run` for a command that has an end, and `chasen ssh` for a console.
- **It runs to its end.** If you press Ctrl-C, your terminal stops listening, and the command goes on in the container.
- **The history has it.** `chasen history` lists each run with its output.
- **The app must run.** A stopped app has no container for the command.

## A shell in the app

```bash
chasen ssh
```

You get a shell in the container that has the traffic, with the env, the secrets, and the storage of the app. It is `bash` when the image has it, and `sh` when it does not. Use it for a console (`bin/rails console`, `sqlite3 /storage/db.sqlite3`), or to look around.

- **It is not SSH.** The name is the one people look for. Nobody logs in to the server: your keys go to the same HTTPS API as every command, with the same login, and the server opens the shell in the container. No port 22, no key to manage.
- **It is the container, not the server.** You see the files of the app. You do not see the other apps or the server itself.
- **What you change outside the storage is gone at the next deploy.** A deploy starts a new container from the image.
- **The history has it:** when the shell opened and for how long, not what you typed.
- **It needs a shell in the image.** An image with no `sh` (distroless, scratch) has nothing to open.
- **A pipe works too:** `echo "bin/rails runner 'puts User.count'" | chasen ssh` runs the lines and ends with the exit code of the shell.

## Coming from matcha

A server that runs its apps with matcha can move to Chasen one app at a time, with no downtime and no copy of data. Chasen is built on matcha: it uses the same proxy, the same names for containers, and the same directories for the data. So the app does not move. Only its record does.

```bash
curl -fsSL https://chasenhq.com/server | sudo sh
sudo chasen-server setup --domain example.com    # next to matcha: the proxy that runs stays as it is
sudo chasen-server adopt shop                     # for each app
```

- **Nothing restarts.** The container keeps running, with its env, its domains, and its databases in `/var/matcha/shop`.
- **The record moves** from `/etc/matcha/config.yml` to Chasen, as it is. matcha does not know the app any more, so its updates leave it alone. A copy of the file from before stays next to it.
- **From then on** `chasen status`, `logs`, `backup`, `restore`, `domains`, `ssh`, and `restart` work on the app, and the hourly backups include it.
- **The first `chasen restart`** starts the app again from the same record, with the health check and the swap that matcha did. If the new container does not get healthy, the old one keeps the traffic.
- **The way back:** `sudo chasen-server adopt --undo shop`.

If a nightly job on the server updates the app with matcha (a cron line with `matcha update`), turn that job off for an app you adopt.

## The screen

Run `chasen` with no command, in a terminal, to open the screen of your apps. It is for watching and running a server: it shows the apps of the current server on the left, and under them how busy the server is. On the right it shows one app: its state, its history, its backups, its domains, and its logs.

The screen does not deploy. A deploy needs the directory of an app, and the screen is about the whole server. Run `chasen deploy` in the directory, or in CI, and the new version shows up on the screen.

| Key | What it does |
|---|---|
| left, right (or `h`, `l`) | Go to a side: the apps on the left, or the tab on the right |
| up, down (or `k`, `j`) | Move in the side you are on: the next app, the next row, or the next lines |
| tab, shift+tab | The next tab, the tab before. `1` to `5` go to a tab |
| enter | Open the row: the output of a history entry, or the restore of a backup |
| `:` | Run a command of the CLI for the chosen app: `:restore live`, `:domains add shop.com`, `:remove`. A command that changes something asks first |
| the mouse | A click chooses an app, a tab, or a row. A click on the chosen row opens it. The wheel scrolls. Hold shift to select text |
| `/` | Narrow the rows, or the logs, to what you type. The logs keep coming, and only the lines with the text show. Esc takes the filter away |
| `r` | Restart the app, from the same image |
| `b` | Back up the app now |
| `a`, `x` | On the domains tab: add a domain, remove the chosen domain |
| `o` | Open the app in the browser |
| `s` | Go to another server that you are logged in to |
| `!` | Show the alerts of the server, with their fix |
| `t` | Go to the next theme of colors |
| `g` | Load everything again |
| `?` | Show the keys |
| `q` | Close |

**A deploy shows up while it runs.** Start `chasen deploy` in another terminal, or let CI do it. Within five seconds the app gets a spinner in the list, and its overview shows "Running now" with the output as it comes. Enter on the entry in the history follows it to its end.

**The screen shows its commands.** Each tab has the line of the CLI that prints it, like `chasen -a shop backups`, and each action shows the line that does the same. So the screen also teaches the CLI.

**When an action fails,** `!` opens a new issue in the browser with the command and the end of its output. You read it and send it; the screen sends nothing by itself.

A restart, a restore, and the removal of a domain ask first. Each action is a command of this page, so the screen can do nothing that the commands cannot. Without a terminal, or without a login, `chasen` prints its usage.

**The alerts of the server** are in the top line, and `!` shows them with their fix. They refresh every minute.

**The colors follow your theme.** `t` goes to the next theme, and the screen remembers it:

- `omarchy`: on [Omarchy](https://omarchy.org), the accent and the red of the current theme. The screen changes with it when you switch themes. This is the default when you have Omarchy.
- `chasen`: the amber of Chasen. The default everywhere else.
- `terminal`: the 16 colors of your terminal, so any terminal theme applies.

`CHASEN_THEME=terminal` (or `chasen`, `omarchy`) wins over the choice. The screen has no color when `NO_COLOR` is set.

## Alerts

`chasen alerts` says what is wrong with the server now (an error), and what puts it at risk or goes wrong soon (a warning). Each alert has its fix. The screen asks for them every minute: the top line counts them, and `!` shows them.

| Alert | Level | Name |
|---|---|---|
| An app does not run, or starts again and again | error | `app` |
| The newest backup of an app is more than two hours old: the hourly backup fails | error | `backups` |
| The live replica misses changes for more than 10 minutes | error | `replica` |
| The disk is 90% full or more (80%: a warning) | error | `disk` |
| Less than 10% of the memory is free | warning | `memory` |
| SSH accepts passwords | warning | `ssh` |
| No firewall runs on the server (ufw, or nftables that drops what it does not allow) | warning | `firewall` |
| The server does not install its security updates by itself (Debian and Ubuntu) | warning | `updates` |
| An update waits for a reboot for more than a day | warning | `reboot` |
| The nightly update of `chasen-server` is off | warning | `auto_update` |

The server looks when you ask: nothing runs in the background. Chasen does not fix these for you: the operating system is yours. When an alert does not apply, turn it off by its name on the server, for example when the firewall of your provider protects the server: `chasen-server settings quiet_alerts firewall`.

## Several servers

You can be logged in to several servers and to the cloud at the same time.

```bash
chasen add server root@203.0.113.5   # through SSH
chasen add server example.org        # through its address on the web
chasen servers                       # numbered. The star marks where commands go
chasen use 2                         # change it: the number, or a part of the address
chasen use                           # or choose from the list
```

In the screen (`chasen` with no command), `s` switches to another server.

An app can name its server in `chasen.yml`, so `chasen deploy` always goes to the right one:

```yaml
name: shop
server: root@203.0.113.5   # or a base domain like example.org, or: cloud
```

- `chasen logout` makes the server forget the login.
- In CI, set `CHASEN_URL` and `CHASEN_TOKEN` (the server token) instead.

### The two ways to a server

| | Through SSH | On the web |
|---|---|---|
| You type | `chasen deploy root@203.0.113.5`, or `chasen add server root@203.0.113.5` for a login with no deploy | `chasen add server example.com` |
| The server needs | SSH, as root or with `sudo` and no password | a base domain, a DNS record for `api.<domain>`, and a certificate, which it gets by itself |
| The login | who can log in with SSH owns the server | the token of the server, typed in a browser |
| Good for | your own computers. No DNS, no open port but SSH | CI with a token, and people with no SSH access |

Both are the same API with the same commands. SSH is only the way in: the CLI runs `ssh`, which runs `chasen-server connect` on the server, and that joins the connection to the API. The CLI never runs other commands on your server. A server can have both ways at the same time.

The SSH way uses the `ssh` program of your computer. A key with a password, an agent, another port (`chasen add server root@203.0.113.5:2222`), a jump host in `~/.ssh/config`, a private network: what works with `ssh` works here.

## Files on the server

```
/usr/local/bin/chasen-server         the binary; the API container runs it
/usr/local/bin/chasen-server.previous  the binary before the last update
/etc/chasen/server.sqlite3           the database of the server, and all its state: its settings (domain,
                                     token, bucket), the apps, the env and secrets of each app, the
                                     logins, and the activity feed
/var/matcha/<app>/storage/           the storage of the app, /storage in the container
/var/matcha/<app>/backups/<time>/    snapshots
/var/matcha/proxy/                   certificates
```

The server has no config file: the database is all of it. An older version kept the apps in `/etc/chasen/apps.yml`. A server that has that file copies it into the database after the update, one time. The file stays, so an update that fails can go back to the older version. After that you can delete it.

`docker logs chasen-server` shows the API, the hourly backups, and the live replica.

## Limits

- The log of each container is capped at 3 files of 10 MB. `chasen logs` shows the newest lines; ship the logs elsewhere (the LogNorth addon) to keep more.
- One owner for each server. Every login to a server can deploy every app on that server.
- An app stays on its server. No command moves it to another one.
- One container for each app. Each container gets 512 MB of memory unless `memory:` in `chasen.yml` says more.
- SQLite only. Files in the storage that are not SQLite databases persist, but they have no backup.
- A server never builds an image. `chasen deploy` builds it where it runs: your computer, or CI.
- No rollback command. Deploy the previous commit again: `chasen deploy --tag <commit>`.
