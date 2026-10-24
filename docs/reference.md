# Commands, files, and limits

## Commands

| Command | What it does |
|---|---|
| `chasen deploy --server <user>@<host>` | Deploy to that server, through SSH. The first time, the server gets `chasen-server` and this computer gets its login |
| `chasen add server <user>@<host>` | Log in to your own server through SSH, with no deploy |
| `chasen add server <domain>` | Use your own server through its address on the web (`https://api.<domain>`), with a login in the browser |
| `chasen servers` | List the servers you are logged in to. The star marks the current one |
| `chasen use [server]` | Make another server the current one: its number in `chasen servers`, or a part of its address. With nothing, it shows the list and asks |
| `chasen logout` | Forget the login, here and on the server |
| `chasen update` | Install the newest release of the CLI now, after a check of its checksum. At a terminal, `chasen` does it by itself once a day, after a command that works. Never in CI. `CHASEN_NO_UPDATE_CHECK=1` turns it off |
| `chasen report` | Something is wrong with Chasen? Open a new issue on GitHub, with your version and your system filled in. It sends nothing by itself |
| `chasen deploy` | Build the image of the current git commit, push it, and deploy it. Or deploy a static website. In a folder with an `image:` in `chasen.yml` and no `Dockerfile`: deploy the newest image, with no build. `--tag <tag>` deploys an image that is already in the registry. `--domain <domain>` gives a new app its domain. `--private` keeps the app off the internet: see [Private apps](private.md). |
| `chasen deploy <image> [<user>@<host>]` | Deploy an image that another repository releases from any directory, with nothing of the directory: `chasen deploy ghcr.io/acme/chat --domain chat.example.com`. The app is named after the image, or `-a <app>`. The server pulls `latest`, or the tag of the image. Run it again to update: the app keeps its settings. `--auto-update` makes the server deploy the newest image of the tag each night at 05:30 UTC (a public image only), `--no-auto-update` stops it. `--private[=<address>]` keeps the app off the internet. `<user>@<host>` can come after it. |
| `chasen check` | Test the current git commit against the standard. Changes nothing live |
| `chasen restart` | Start the app again with the env and secrets of `chasen.yml`, from the image it has |
| `chasen rollback [version]` | Start a version that the server kept again, in seconds: no build, no pull. With no version, the one before; a second rollback goes forward again. `chasen status` lists the kept versions, and `R` on the screen picks one. The data stays as it is: `chasen restore` brings back a backup |
| `chasen secrets edit` | Change the secrets of the app in your editor. They stay in the repository, encrypted |
| `chasen secrets` | List the names of the secrets. `chasen secrets show` prints the values too |
| `chasen status` | Show the version, the state, the URLs (or the address of a private app), the last backup, the replica, and the disk: the data, the backups on the server, and the image that runs |
| `chasen logs` | Follow the app logs. `chasen logs jobs` follows the jobs container |
| `chasen enable <addon> [domain]` | Run fusionaly, formlander, or lognorth from its image |
| `chasen -a <app> <command>` | Run a command for an addon, or for an app of another directory |
| `chasen --server <server> <command>` | Run a command on another server you are logged in to: `root@203.0.113.5`, or a base domain like `example.com`. It wins over `server:` in `chasen.yml`. For `deploy`, a server that this computer does not know yet is set up and logged in to first |
| `chasen run <command>` | Run one command in the container of the app, with its env and its storage: `chasen run bin/rails db:migrate`. The output comes back as it is written, and the exit code is the one of the command. The history keeps it. See "Run a command in the app" below |
| `chasen ssh` | Open a shell in the container of the app. See "A shell in the app" below |
| `chasen history [id]` | Show the deploys and changes of the app, or the output of one |
| `chasen domains [add\|rm <domain>]` | List or change the domains |
| `chasen backup` | Make a snapshot now |
| `chasen backups` | List the backups, where each one is, and its size on the server |
| `chasen verify` | Prove that the copies restore: the live replica and the newest snapshot are restored next to the real databases, checked, and removed |
| `chasen download [backup]` | Save the databases of a backup in the current directory, as a `tar.gz` file. The default is the newest backup |
| `chasen restore [backup\|live]` | Restore a backup |
| `chasen remove` | Stop the app. Keeps the data and the backups |
| `chasen list` | List all apps on the server |
| `chasen load` | Show how busy the server is: the load, the memory in use, and the disk of the apps |
| `chasen overview` | Each app of the server: its state, its version, and the change that runs now. `--json`: every server that you are logged in to, with its apps and alerts, for a desktop. `--watch`: the same JSON again, one line once a minute and every 5 seconds while a deploy runs, on connections that stay open |
| `chasen alerts` | Show what is wrong with the server, or puts it at risk. See "Alerts" below. `--waybar`: the alerts of every server, for [the top bar of Omarchy](omarchy.md) |
| `chasen bucket` | Show where the backups of the server go. With `--endpoint`, `--name`, and `--access-key-id`: send them to an S3 bucket too |

On the server, as root:

| Command | What it does |
|---|---|
| `chasen-server setup --domain <domain>` | Start the proxy and the API, and the timer of the nightly update |
| `chasen-server bucket ...` | Set the S3 bucket for offsite backups. From your computer it is `chasen bucket ...` |
| `chasen-server update` | Install the newest release now, and the proxy it names. The timer does this each night, in the quiet hour of the server |
| `chasen-server proxy` | Show the version of the proxy, and the one of this release of Chasen |
| `chasen-server proxy update` | Start the proxy of this release. New visitors wait a few seconds, with no error; the routes and the certificates stay |
| `chasen-server check` | The same as `chasen alerts`, on the server |
| `chasen-server settings quiet_alerts <name,name>` | Turn alerts off by their name: `firewall` when the firewall of your provider does that job. An empty value turns them all on again |
| `chasen-server settings domain none` | Remove the base domain. The API no longer answers at `https://api.<domain>`, and the apps keep their domains. `chasen-server setup --domain <domain>` gives it back |
| `chasen-server list` | List all apps |
| `chasen-server quiet-hour` | Print the hour of the day with the fewest requests in the last week, for work that takes the server away |
| `chasen-server backup` | Back up every app now |
| `chasen-server auto-update [app]` | Deploy the newest image of each app deployed with `--auto-update`, now, or of one app. The API does it each night at 05:30 UTC |
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

A server that runs its apps with matcha can move to Chasen one app at a time, with no downtime and no copy of data. Chasen began as matcha: it uses the same proxy, the same names for containers, and the same directories for the data. So the app does not move. Only its record does.

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

Run `chasen` with no command, in a terminal, to open the screen of your apps. It is for watching and running your servers.

On the left is each server you are logged in to, with its apps under it. The arrow keys go down the list: a server, then its apps, then the next server.

**A server shows its map.** The screen opens on it:

```
 servers         │ map   shop-server                                                     $ chasen overview
                 │
▸shop-server     │ amd64 · 4 cores · 7.6 GB
   ● blog        │ internet ━•━━▶ proxy ━┳━•━━▶ ● blog   a1b2c3d   1 db  ───▶ backup 12 min ago  ───▶ backups-eu
   ● shop        │                       ┃        blog.example.com      ·•·▶ live replica       ···▶ backups-eu
 lab             │                       ┗━━•━▶ ● shop   3f9a2c1   2 db  ───▶ backup 12 min ago  ───▶ backups-eu
   ○ wiki        │                                shop.example.com      ··•▶ live replica       ·•·▶ backups-eu
                 │                                └ jobs  ● Up 3 hours
                 │ you ──── ssh ────▶ API   SSH only: not on the web
```

The first line is the machine. Under it, the traffic comes from the internet to the proxy, and from the proxy to each app, with its version and its domains. On the right, the data of the app goes to the hourly backup on the server, and from there to the bucket. The live replica sends each change to the bucket, about a second behind. A server with no bucket says "this server only". The last line says where the API answers: through SSH only, or on the internet as a branch of the proxy.

A line moves while its path is open: the traffic to an app that answers, a live replica, a backup that runs. It does not count requests: the server does not know them. An app that is down has a red cross on its line. A replica that is behind is red. The map asks each server every 10 seconds, with the same three commands however many apps it has: `chasen overview`, `chasen load`, and, once a minute, `chasen alerts`.

The map only shows. The keys stay on the left: choose an app there to act on it. Under the list, the corner shows how busy the server is.

**An app shows its tabs:** its state, its history, its backups, its domains, and its logs. `esc` on the list goes back to the map of its server.

**The history names each deploy by its commit:** `deploy 3f9a2c1 Fix login redirect`. `o` on a deploy opens the commit on GitHub, GitLab, or the forge of the git origin. `↺` marks the deploys whose image the server keeps, and `R` on one starts it again in seconds.

**The commits tab deploys.** Open the screen in the directory of an app, and that app gets a sixth tab: the commits of the current branch, each with what it is on the server: `● runs now`, `↺ kept`, `deployed before`, or `not deployed`. `d` deploys the chosen commit, after a `y`:

- The newest commit builds here, pushes, and deploys, like `chasen deploy`.
- An older one deploys its image from the registry, like `chasen deploy --tag <hash>`. Nothing is built: an image that is not in the registry fails, and the way is to check out that commit and deploy it.
- A kept one rolls back, in seconds.

The deploy runs `chasen` in that directory, so it goes to the same server and shows the same output as in a terminal. The screen never changes your working tree. Other apps, and the same app on another server, have no commits tab: their repository is somewhere else.

| Key | What it does |
|---|---|
| esc (on the list) | Go back to the map of the server |
| left, right (or `h`, `l`) | Go to a side: the apps on the left, or the tab on the right |
| up, down (or `k`, `j`) | Move in the side you are on: the next app, the next row, or the next lines |
| tab, shift+tab | The next tab, the tab before. `1` to `6` go to a tab |
| enter | Open the row: the output of a history entry, or the restore of a backup |
| `:` | Run a command of the CLI for the chosen app: `:restore live`, `:domains add shop.com`, `:remove`. A command that changes something asks first |
| the mouse | A click chooses an app, a tab, or a row. A click on the chosen row opens it. The wheel scrolls. Hold shift to select text |
| `/` | Narrow the rows, or the logs, to what you type. The logs keep coming, and only the lines with the text show. Esc takes the filter away |
| `d` | On the commits tab: deploy the chosen commit |
| `r` | Restart the app, from the same image |
| `b` | Back up the app now |
| `a`, `x` | On the domains tab: add a domain, remove the chosen domain |
| `R` | Roll back: choose one of the versions that the server keeps. On the history or the commits tab: the chosen one |
| `o` | Open the app in the browser. On the history or the commits tab: the page of the chosen commit |
| `s` | List the servers that you are logged in to, and go to one |
| `!` | Show the alerts of the server, with their fix |
| `t` | Go to the next theme of colors |
| `g` | Load everything again |
| `?` | Show the keys |
| `q` | Close |

**A deploy shows up while it runs.** Start `chasen deploy` in another terminal, or let CI do it. Within 10 seconds the app gets a spinner in the list, and its overview shows "Running now" with the output as it comes. Enter on the entry in the history follows it to its end.

**The screen shows its commands.** Each tab has the line of the CLI that prints it, like `chasen -a shop backups`, and each action shows the line that does the same. So the screen also teaches the CLI.

**When an action fails,** `!` opens a new issue in the browser with the command and the end of its output. You read it and send it; the screen sends nothing by itself.

Every action that changes something asks first, with a `y` to go on: a restart, a rollback, a backup, a restore, a domain added or removed, and each command of the `:` line that is not only a look. Each action is a command of this page, so the screen can do nothing that the commands cannot. Without a terminal, or without a login, `chasen` prints its usage.

**The alerts of the server** are in the top line, and `!` shows them with their fix. They refresh every minute.

**The colors follow your theme.** `t` goes to the next theme, and the screen remembers it:

- `chasen`: the amber of Chasen. The default.
- `monochrome`: no color, only bold and dim.
- `terminal`: the 16 colors of your terminal, so the theme of your terminal applies. On [Omarchy](https://omarchy.org), each theme sets those colors, so the screen changes with it.

`CHASEN_THEME=monochrome` (or `chasen`, `terminal`) wins over the choice. The screen has no color when `NO_COLOR` is set.

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
| The API answers on the internet: the server has a base domain | warning | `api` |
| The proxy does not run, and apps need it | error | `proxy` |
| The proxy is another version than the one of this release | warning | `proxy` |
| The server does not install its security updates by itself (Debian and Ubuntu) | warning | `updates` |
| An update waits for a reboot for more than a day | warning | `reboot` |
| The nightly update of `chasen-server` is off | warning | `auto_update` |

The server looks when you ask: nothing runs in the background. Chasen does not fix these for you: the operating system is yours.

### Keep SSH off the internet

A firewall that lets in only SSH, 80, and 443 is good. Better: SSH only from your own private network, so the internet sees ports 80 and 443 and nothing else. Chasen needs nothing for it: the address of a server is an SSH host, so `chasen deploy deploy@shop-server` works with the names of the network.

| Network | What it is |
|---|---|
| [Tailscale](https://tailscale.com) | The easiest: install it on the server and your computer, and log in. Free for personal use. The apps are open source; the service that connects them is not |
| [Headscale](https://github.com/juanfont/headscale) | An open source server for the Tailscale apps, that you run yourself |
| [NetBird](https://netbird.io) | An open source private network, as a service or on your own server |
| [WireGuard](https://www.wireguard.com) | No service at all: it is in the Linux kernel. One file on the server and one on your computer. Best for one server and a few computers |

When the network works, let SSH in only from it. With ufw and Tailscale: `ufw allow in on tailscale0 to any port 22 && ufw delete allow OpenSSH`. Keep a way back before you close the door: the console of your provider. When an alert does not apply, turn it off by its name on the server, for example when the firewall of your provider protects the server: `chasen-server settings quiet_alerts firewall`.

### Keep the API off the internet

A server with a base domain answers at `https://api.<domain>`, on port 443, to anyone who finds it. A login there needs the token, but a bot can try. Without a base domain, the API has no address on the web: the CLI reaches it through SSH. With SSH only from your private network, nothing of Chasen is on the internet. The internet sees your apps on ports 80 and 443, and nothing else.

- **Set up the server with no base domain:** `chasen-server setup`. Each app brings its domain at its first deploy: `chasen deploy --domain shop.example.com`.
- **Deploy from CI through SSH.** The job joins the private network first, then deploys like your computer does. [Through SSH](github-actions.md#through-ssh) has the workflow.
- **A server that has a base domain:** run `chasen-server settings domain none` on it. The API leaves the web, and the apps keep their domains. A computer or CI that logged in at `https://api.<domain>` comes through SSH from then on: `chasen add server root@shop-server`.
- **When you want the API on the web**, for CI with a token or a login with a browser, keep the base domain and turn the alert off: `chasen-server settings quiet_alerts api`.

## Several servers

You can be logged in to several servers at the same time.

```bash
chasen add server root@203.0.113.5   # through SSH
chasen add server example.org        # through its address on the web
chasen servers                       # numbered. The star marks where commands go
chasen use 2                         # change it: the number, or a part of the address
chasen use                           # or choose from the list
```

In the screen (`chasen` with no command), the list on the left has every server with its apps under it. A server shows its map. The arrow keys go from the last app of one server to the next server, and on to its apps.

An app can name its server in `chasen.yml`, so `chasen deploy` always goes to the right one:

```yaml
name: shop
server: root@203.0.113.5   # or a base domain like example.org
```

- `chasen logout` makes the server forget the login.
- In CI, set `CHASEN_URL` and `CHASEN_TOKEN` (the server token) instead.

### The two ways to a server

| | Through SSH | On the web |
|---|---|---|
| You type | `chasen deploy --server root@203.0.113.5`, or `chasen add server root@203.0.113.5` for a login with no deploy | `chasen add server example.com` |
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
- `chasen rollback` starts an image that the server kept: one of the last 5 versions. For an older version, deploy its commit again.
