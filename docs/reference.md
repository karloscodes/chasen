# Commands, files, and limits

## Commands

| Command | What it does |
|---|---|
| `chasen login` | Log in to the Chasen cloud, with a browser |
| `chasen add server <domain>` | Use your own server instead, and log in to it |
| `chasen servers` | List the servers you are logged in to. The star marks the current one |
| `chasen use <server>` | Make another server the current one: a name from the list, or `cloud` |
| `chasen logout` | Forget the login, here and on the server |
| `chasen update` | Install the newest release of the CLI, after a check of its checksum. `chasen` tells you when there is one |
| `chasen report` | Something is wrong with Chasen? Open a new issue on GitHub, with your version and your system filled in. It sends nothing by itself |
| `chasen deploy` | Build the image of the current git commit, push it, and deploy it. Or deploy a static website. `--tag <tag>` deploys an image that is already in the registry. In the cloud: `--on <id>` or `--new[=type@location]` picks the server of a new app |
| `chasen check` | Test the current git commit against the standard. Changes nothing live |
| `chasen restart` | Start the app again with the env and secrets of `chasen.yml`, from the image it has |
| `chasen status` | Show the version, the state, the URLs, the last backup, and the replica |
| `chasen logs` | Follow the app logs |
| `chasen enable <addon> [domain]` | Run fusionaly, formlander, or lognorth from its image |
| `chasen -a <app> <command>` | Run a command for an addon, or for an app of another directory |
| `chasen history [id]` | Show the deploys and changes of the app, or the output of one |
| `chasen domains [add\|rm <domain>]` | List or change the domains |
| `chasen backup` | Make a snapshot now |
| `chasen backups` | List the backups |
| `chasen restore [backup\|live]` | Restore a backup |
| `chasen remove` | Stop the app. Keeps the data and the backups |
| `chasen list` | List all apps on the server |

On the server, as root:

| Command | What it does |
|---|---|
| `chasen-server setup --domain <domain>` | Start the proxy and the API, and the timer of the nightly update |
| `chasen-server bucket ...` | Set the S3 bucket for offsite backups |
| `chasen-server update` | Install the newest release now. The timer does this each night |
| `chasen-server check` | Report what the security of the server lacks |
| `chasen-server list` | List all apps |
| `chasen-server backup` | Back up every app now |

## The screen

Run `chasen` with no command, in a terminal, to open the screen of your apps. It shows the apps of the current server on the left. On the right it shows one app: its state, its history, its backups, its domains, and its logs.

| Key | What it does |
|---|---|
| up, down (or `k`, `j`) | The next app, the next row, or the next lines |
| left, right (or `h`, `l`) | The next tab. `1` to `5` go to a tab |
| tab | Go from the apps to the tab, and back |
| enter | Open the row: the output of a history entry, or the restore of a backup |
| `d` | Deploy the app of the current directory |
| `r` | Restart the app, from the same image |
| `b` | Back up the app now |
| `a`, `x` | On the domains tab: add a domain, remove the chosen domain |
| `o` | Open the app in the browser |
| `s` | Go to another server that you are logged in to |
| `g` | Load everything again |
| `?` | Show the keys |
| `q` | Close |

A restart, a restore, a deploy, and the removal of a domain ask first. Each action is a command of this page, so the screen can do nothing that the commands cannot. Without a terminal, or without a login, `chasen` prints its usage.

The screen has no color when `NO_COLOR` is set.

To try the screen without a server, run `chasen demo`. It has three made-up apps and a server that lives inside the program: a deploy, a backup, and a restore all work, and nothing is real.

## Several servers

You can be logged in to several servers and to the cloud at the same time.

```bash
chasen add server example.com
chasen add server example.org
chasen servers                # the star marks where commands go
chasen use example.com   # change it
```

An app can name its server in `chasen.yml`, so `chasen deploy` always goes to the right one:

```yaml
name: shop
server: example.com      # or: cloud
```

- `chasen logout` makes the server forget the login.
- In CI, set `CHASEN_URL` and `CHASEN_TOKEN` (the server token) instead.

## Files on the server

```
/usr/local/bin/chasen-server         the binary; the API container runs it
/usr/local/bin/chasen-server.previous  the binary before the last update
/etc/chasen/server.sqlite3           the database of the server: its settings (domain, token, bucket),
                                     the env and secrets of each app, the logins, and the activity feed
/etc/chasen/apps.yml                 the apps (matcha format)
/var/matcha/<app>/storage/           the storage of the app, /storage in the container
/var/matcha/<app>/backups/<time>/    snapshots
/var/matcha/proxy/                   certificates
```

`docker logs chasen-server` shows the API, the hourly backups, and the live replica.

## Limits

- The log of each container is capped at 3 files of 10 MB. `chasen logs` shows the newest lines; ship the logs elsewhere (the LogNorth addon) to keep more.
- One owner for each server. Every login to a server can deploy every app on that server.
- An app stays on its server. No command moves it to another one.
- One container for each app. 512 MB of memory for each container (a matcha default).
- SQLite only. Files in the storage that are not SQLite databases persist, but they have no backup.
- A server never builds an image. `chasen deploy` builds it where it runs: your computer, or CI.
- No rollback command. Deploy the previous commit again: `chasen deploy --tag <commit>`.
