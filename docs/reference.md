# Commands, files, and limits

## Commands

| Command | What it does |
|---|---|
| `chasen login` | Log in to the Chasen cloud, with a browser |
| `chasen add server <domain>` | Use your own server instead, and log in to it |
| `chasen servers` | List the servers you are logged in to. The star marks the current one |
| `chasen use <server>` | Make another server the current one: a name from the list, or `cloud` |
| `chasen logout` | Forget the login, here and on the server |
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
/etc/chasen/config.yml               base domain, server token, and backup bucket
/etc/chasen/server.sqlite3           the database of the server: logins and the activity feed
/etc/chasen/apps.yml                 the apps (matcha format)
/etc/chasen/env/<app>.json           env and secrets of each app
/var/matcha/<app>/storage/           the storage of the app, /storage in the container
/var/matcha/<app>/backups/<time>/    snapshots
/var/matcha/proxy/                   certificates
```

`docker logs chasen-server` shows the API, the hourly backups, and the live replica.

## Limits

- One owner for each server. Every login to a server can deploy every app on that server.
- An app stays on its server. No command moves it to another one.
- One container for each app. 512 MB of memory for each container (a matcha default).
- SQLite only. Files in the storage that are not SQLite databases persist, but they have no backup.
- A server never builds an image. `chasen deploy` builds it where it runs: your computer, or CI.
- No rollback command. Deploy the previous commit again: `chasen deploy --tag <commit>`.
