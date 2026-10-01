# Chasen

Enter your app. Run `chasen deploy`. It is live.

A chasen is the bamboo whisk that prepares matcha. Chasen is the tool you hold; [matcha](https://github.com/karloscodes/matcha) is the engine underneath.

Chasen deploys the Docker image of an app with a SQLite database to one server. You get HTTPS, deploys without downtime, custom domains, and backups you can trust. `chasen deploy` builds the image on your computer or in CI and pushes it to a registry. The server pulls it: nothing builds on the server. There is no YAML pipeline of ours and no SSH in the deploy.

It is free and open source, and it runs on a server you own. The site is [chasenhq.com](https://chasenhq.com), and [the docs](https://chasenhq.com/docs/) start with a step-by-step guide.

```bash
# on the server
curl -fsSL https://chasenhq.com/server | sh
chasen-server setup --domain apps.example.com

# on your computer
curl -fsSL https://chasenhq.com/cli | sh
chasen add server apps.example.com
cd myapp
chasen deploy
# Deployed myapp 3f9a2c1
#   https://myapp.apps.example.com
```

No server yet? [Chasen cloud](https://chasenhq.com/cloud/) creates one for you at Hetzner and runs this same software on it.

## How it works

```
your machine                          the server
chasen  ── HTTPS + token ──▶  chasen-server (API, in a container behind the proxy)
                                ├─ docker pull         the image of your commit, from ghcr.io or Docker Hub
                                ├─ matcha              proxy, HTTPS, deploy without downtime
                                ├─ backups             checked snapshots each hour
                                └─ Litestream          live replica to an S3 bucket
```

- `chasen` is the client on your machine. `chasen-server` runs on the server.
- The client calls the HTTPS API of the server at `https://api.<base domain>`. `chasen login` is an OAuth 2.0 device login: you approve it in a browser, and the client gets a token of its own. The API streams the output of each command back, so a deploy shows each step as it runs.
- `chasen deploy` builds the image of the commit where it runs (your computer, or CI) and pushes it to a registry. The server pulls it, also from a private registry. A server never builds an app: a build would take the memory and the CPU of the live apps.
- [matcha](https://github.com/karloscodes/matcha) is the deploy engine. It runs kamal-proxy, gets the Let's Encrypt certificates, and swaps containers without downtime. Chasen apps and matcha apps share one proxy on the same server.
- [Litestream](https://litestream.io) is inside `chasen-server`. The server needs Docker and nothing else.

## The standard

Chasen manages the server. Your app follows a few rules. Each rule has a default, and `chasen.yml` can change it. [`STANDARD.md`](STANDARD.md) has the full text.

| Rule | Default | Override in `chasen.yml` |
|---|---|---|
| The app is a Docker image in a registry | `ghcr.io/<owner>/<repository>` of the git origin on GitHub, with the git commit as its tag | `image:` and `registry:` in `chasen.yml`. `--tag <tag>`. A directory with an `index.html` and no `Dockerfile` is a static website, and needs no image |
| It serves HTTP on one port | The port the image declares with `EXPOSE`, else 8080. Also in `$PORT` | `port: 3000` |
| The health path returns `200` when the app is ready | `/up`, within 30 seconds | `health: /_health`, `health_timeout: 90` |
| What must persist is in its storage | The paths the image declares with `VOLUME`, else `/storage` | `volumes: [/app/storage]` |
| Its databases are SQLite files in the storage | Any name, any number of files | |
| It migrates its database when it starts | Before the health path answers | |
| It stops within 10 seconds of `SIGTERM` | | |
| The app is in git | Chasen deploys the current commit | |

Chasen sets these environment variables in the container. An app cannot override them:

| Variable | Value |
|---|---|
| `PORT` | The port of the app |
| `BASE_URL` | `https://` + the first custom domain, or the default domain |
| `STORAGE_DIR` | The first storage path |
| `DATABASE_PATH` | `$STORAGE_DIR/db.sqlite3`, a suggestion for an app with one database |
| `SECRET_KEY_BASE`, `PRIVATE_KEY` | A random secret, made once for each app |
| `APP_VERSION` | The git commit |
| `APP_ENV` | `production` |

Test an app against the rules before it gets traffic:

```bash
chasen check
# Checking shop 3f9a2c1 against the standard
#   ok    the port is 8080 (the default, the image has no EXPOSE)
#   ok    GET /up returned 200 after 1s
#   ok    the storage /storage is writable
#   ok    no database outside the storage
#   ok    a restart is safe: healthy again after 1s
#   ok    the app stops on SIGTERM (200ms)
# All checks passed.
```

`chasen check` pulls the image of the commit and runs it next to the live app, with no traffic and an empty storage. It changes nothing that is live.

[`example/`](example/) is the smallest app that follows the standard.

## Set up the server

You need a Linux server (Debian or Ubuntu) with root access, and ports 80 and 443 open. Point a wildcard DNS record `*.apps.example.com` to the server first.

```bash
# On the server, as root
curl -fsSL https://chasenhq.com/server | sh
chasen-server setup --domain apps.example.com
chasen-server check          # checks SSH, the firewall, and the security updates
```

The install script downloads the binary for the server's architecture and checks its checksum. To update a server later, run both lines again.

`setup` starts the proxy and the API, and prints the login command and the server token:

```
On your machine, run:
  chasen add server apps.example.com
Server token (the login page asks for it): 3f9a...
```

The server token is in `/etc/chasen/config.yml`. To change it, edit the file and run `setup` again.

## Log in

```bash
curl -fsSL https://chasenhq.com/cli | sh      # macOS and Linux

chasen add server apps.example.com    # your own server
chasen login                          # or the Chasen cloud
# Open this page to log in:
#   https://api.apps.example.com/oauth/device?user_code=BCDF-GHJK
# Code: BCDF-GHJK
```

Both commands are an OAuth 2.0 device login. Open the page, check the code, and type your key: the key of your cloud account, or the server token of your own server. This works on a machine without a browser: open the page on any other device.

The logins are in `~/.config/chasen/credentials.json`. When a saved login stops working, the next command starts a new login by itself.

### Several servers

You can be logged in to several servers and to the cloud at the same time.

```bash
chasen add server apps.example.com
chasen add server other.example.com
chasen servers                # the star marks where commands go
chasen use apps.example.com   # change it
```

An app can name its server in `chasen.yml`, so `chasen deploy` always goes to the right one:

```yaml
name: shop
server: apps.example.com      # or: cloud
```

- `chasen logout` makes the server forget the login.
- In CI, set `CHASEN_URL` and `CHASEN_TOKEN` (the server token) instead.

## Deploy

```bash
cd myapp
chasen deploy
```

The deploy pulls the image, makes a backup, starts the new container, and moves the traffic when `/up` answers. If the new version does not become healthy in time, the old version keeps the traffic.

A deploy runs to its end on the server, also when your connection drops.

### The image

An app is an image in a registry.

**A repository on GitHub needs no setting.** When the git origin is `github.com/you/shop`, the image goes to `ghcr.io/you/shop`, and Chasen uses the login that is already on your computer: `docker login ghcr.io` (with your GitHub name and a token that has the `write:packages` scope), or the `gh` CLI (`gh auth login -s write:packages`). In CI it is `GHCR_TOKEN` or `GITHUB_TOKEN`. For every other registry, `docker login` is enough too.

For another registry, or another name, `chasen.yml` says it, without a tag:

```yaml
name: shop
image: you/shop           # Docker Hub. Or registry.example.com/you/shop
registry:                 # the login of the registry
  username: you
  password: REGISTRY_TOKEN   # the name of a secret, not its value
```

`chasen deploy` then does three things: it builds the image of the current git commit with your Docker, pushes it to the registry, and tells the server to pull it. The tag is the full hash of the commit.

- **The build gets the files of the commit**, not your working directory. So the image is what its tag says.
- **In CI it is the same command.** [Deploy from GitHub Actions](docs/github-actions.md) has the workflow, the two secrets it needs, and what to do when it fails. With it, `git push` is the deploy.
- **The login of the registry** is used for the push, and it goes with the deploy for the pull. The server does not keep it. In GitHub Actions it is the token of the job, so there is no long-lived token to store.
- **`chasen deploy --tag <tag>`** deploys an image that is already in the registry, and builds nothing. This is the rollback (`--tag <older commit>`), and the way to deploy an image that another system built.
- **The architecture.** The build is for `linux/amd64`, like most servers. For another server, set `DOCKER_DEFAULT_PLATFORM`.

### Websites

A directory with an `index.html` and no `Dockerfile` deploys as a static website. Chasen serves the files of the commit with Caddy. A site that needs a build step (Astro, Hugo) is an image like any app: it has a `Dockerfile` like any app.

### Addons

An addon is a ready product that Chasen runs from its image. There is no source and no build. It gets a domain, HTTPS, and the same backups as your apps.

```bash
chasen enable lognorth                    # https://lognorth.apps.example.com
chasen enable fusionaly stats.example.com # with a domain of your own
chasen -a lognorth status                 # -a names the app: an addon has no directory
chasen -a lognorth logs
```

| Addon | What it is |
|---|---|
| `fusionaly` | Privacy-first web analytics |
| `formlander` | Form backend for static sites |
| `lognorth` | Logs, errors, alerts, and uptime |

Run `chasen enable <addon>` again to update it to the newest image. An addon has one domain.

### History

```bash
chasen history
# ID  WHEN (UTC)           ACTION                      RESULT
# 5   2026-10-01 12:03:21  deploy 6cff7df              failed
# 4   2026-10-01 12:03:02  restore                     succeeded
# 3   2026-10-01 12:02:44  domains add shop.com        succeeded
chasen history 5      # the full output of that deploy
```

The server records each deploy, restore, domain change, and removal, with its output.

## chasen.yml

The file is optional for a website, and for an app whose git origin is on GitHub. Without `name:`, the app name is the name of the directory. Without `image:`, the image is `ghcr.io/<owner>/<repository>` of the git origin.

```yaml
name: myapp
env:
  LOG_LEVEL: info
secrets: [STRIPE_KEY, SMTP_PASSWORD]
secrets_command: fnox export

# Overrides of the standard. Leave them out when the defaults fit.
port: 3000
health: /_health
health_timeout: 90
volumes: [/app/storage]
```

## Secrets

Secret values never go into git or into the image. `secrets:` lists the names. At each deploy, Chasen reads the values on your machine and sends them to the server over HTTPS. A missing secret stops the deploy before it changes anything.

Chasen reads each value from the output of `secrets_command`, then from the environment. `secrets_command` is any command that prints `KEY=VALUE` lines:

| Tool | `secrets_command` |
|---|---|
| fnox | `fnox export` |
| 1Password | `op inject -i .env.tpl` |
| sops (encrypted file in git) | `sops -d secrets.enc.env` |
| A local file | `cat .env.production` |

Without `secrets_command`, wrap the deploy: `fnox exec -- chasen deploy` or `op run --env-file=.env.tpl -- chasen deploy`.

On the server, the values are in files that only root can read.

To change a value or rotate a secret without a build:

```bash
chasen restart       # starts the app again with the env and secrets of chasen.yml
```

Secrets travel with the deploy on purpose. Nothing that matters lives only on the server, so a new server needs one `chasen deploy` to get the configuration, the secrets, and the data back.

## Custom domains

```bash
chasen domains add myapp.com
chasen domains add www.myapp.com
chasen domains            # list
chasen domains rm www.myapp.com
```

Point an A record for the domain to the server. The certificate comes on the first HTTPS request after DNS resolves. The change needs no build and has no downtime.

### Cloudflare in front (recommended, optional)

Put Cloudflare in front of your domains: turn the proxy on and set the SSL mode to **Full (strict)**. Chasen needs no setting for it.

- Visitors see Cloudflare's addresses, not the address of your server.
- Cloudflare caches images, scripts, and styles at its edge. Most requests of a traffic spike never reach the server. Pages that the app renders still come from the server, unless you add a cache rule.
- Chasen keeps its own Let's Encrypt certificate, so the connection stays encrypted from the visitor to the app.

## Backups

A single server with SQLite needs backups that you can trust. Chasen has two layers.

**Snapshots.** Each hour, and before each deploy, Chasen makes a snapshot of every SQLite database in the storage of the app:

- `VACUUM INTO` gives a consistent copy while the app writes.
- `PRAGMA integrity_check` must pass, or the backup fails. A failed backup before a deploy stops the deploy.
- Chasen compresses the snapshot and copies it to the S3 bucket.
- Retention keeps the newest snapshot of each of the last 24 hours, 7 days, 4 weeks, and 6 months. The same rule runs on the server and in the bucket.

**Live replica.** Litestream streams every change to the S3 bucket, about one second behind. This is the copy that loses almost nothing when the server dies.

Set the bucket on the server. Any S3-compatible store works: S3, Cloudflare R2, Backblaze B2, Hetzner.

```bash
chasen-server bucket --endpoint https://fsn1.your-objectstorage.com --region fsn1 \
  --name chasen-backups --access-key-id <id>
# Secret access key: ...
chasen-server bucket          # show the bucket
```

The command creates the bucket when it does not exist, then writes and deletes a test object. It saves the settings only when all of that works. Set `S3_SECRET_ACCESS_KEY` to skip the question.

For a monitor that alerts when backups stop, add `heartbeat_url` under `backup:` in `/etc/chasen/config.yml`.

Without a bucket, the snapshots stay on the server and there is no live replica. `setup` warns you about this.

Chasen calls `heartbeat_url` each hour, only when every snapshot worked and the live replica runs. Point a monitor at it, so that you know when backups stop.

### Restore

```bash
chasen backups                    # list the snapshots and the live replica
chasen restore                    # the newest snapshot
chasen restore 20261001T120000Z   # one snapshot
chasen restore live               # the newest state in the live replica
```

A restore checks the backup first, stops the app, swaps the databases, and starts the app. It moves the previous databases to `/var/matcha/<app>/pre-restore-<time>/`. It deletes nothing.

### A new server

When a server is gone, these steps bring everything back:

1. Set up the new server with the same domain and the same bucket (`setup`, then `bucket`).
2. Point the wildcard DNS record at the new server.
3. Run `chasen add server <domain>` again: the new server has a new token.
4. Run `chasen deploy` for each app, and `chasen enable` for each addon. When the server has no data for the app and the bucket has a copy, the deploy restores it first: the live replica, or the newest snapshot if the replica fails.
5. Add the custom domains again (`chasen domains add`). They were on the old server.

An app name owns its data. A new app that reuses the name of an old app gets the data of the old app.

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
- Each deploy prints one `Could not pull ...` warning and waits about 6 seconds. It is harmless: the server already pulled your image, and matcha then tries one more pull of the local name of that image, which no registry has.

## The cloud

Chasen cloud is this same software on a server that we create for you at Hetzner: `chasen login`, then `chasen deploy`. It is a separate service and it is not in this repository. The CLI talks to it with the same protocol as to your own server. See [chasenhq.com/cloud](https://chasenhq.com/cloud/).

## More documents

- [`STANDARD.md`](STANDARD.md): the rules an app follows.
- [Deploy from GitHub Actions](docs/github-actions.md).
- [The server protocol](docs/protocol.md): the HTTP API between the CLI, the server, and the cloud.

## Development

```bash
mise run test     # go vet and the unit tests. No Docker
mise run e2e      # real Docker, the proxy on ports 80 and 443, an S3 store, and a private registry. Needs sudo
mise run smol     # a disposable Chasen server in a smolvm machine
```

`mise run smol` makes a test server in a [smolvm](https://github.com/smol-machines/smolvm) machine and prints the two variables that point the CLI at it. It needs no sudo for its network and takes no port 80 or 443 on your computer. `bin/smol delete` removes it.

The two programs share one contract: [`protocol`](protocol/protocol.go) for the commands and [`oauth`](oauth/oauth.go) for the login.

### Releases

A tag `v*` makes a release: GitHub Actions builds the CLI for macOS and Linux and the server for Linux, and attaches them to the release with `checksums.txt`. The install scripts download the newest release.

## License

[Apache 2.0](LICENSE)
