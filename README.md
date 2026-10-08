# Chasen

Enter your app. Run `chasen deploy`. It is live.

A chasen is the bamboo whisk that prepares matcha. Chasen is the tool you hold; [matcha](https://github.com/karloscodes/matcha) is the engine underneath.

Chasen deploys the Docker image of an app with a SQLite database to one server. You get HTTPS, deploys without downtime, custom domains, and backups you can trust. `chasen deploy` builds the image on your computer or in CI and sends it to the server through SSH: no registry, no token, nothing builds on the server. There is no YAML pipeline of ours, and the server needs no name in DNS.

It is free and open source, and it runs on a server you own. The site is [chasenhq.com](https://chasenhq.com), and [the docs](https://chasenhq.com/docs/) start with a step-by-step guide.

```bash
curl -fsSL https://chasenhq.com/cli | sh
cd myapp
chasen deploy --server root@203.0.113.5 --domain myapp.example.com
# the first time, this installs Chasen on the server, through SSH
# Deployed myapp 3f9a2c1
#   https://myapp.example.com
```

An app that someone else made deploys from its image, from any directory. Run the same line again to update it:

```bash
chasen deploy ghcr.io/acme/chat --domain chat.example.com
```

## How it works

```
your computer                               your server
chasen deploy                               chasen-server, one binary, in a container
  builds the image of your commit             pulls the image through SSH, backs up the
  sends it through SSH, no registry ──────▶   databases, starts the new version next to
                                              the old one, moves the traffic when /up answers
                                              hourly checked backups, a live replica to S3,
                                              the history of every deploy, alerts
```

- `chasen` is the CLI on your computer. It is all you run.
- It reaches the server through your own `ssh`: your keys, your `~/.ssh/config`. A server with a base domain also answers on the web at `https://api.<domain>`, for CI and for a login with a browser. Both ways speak [one protocol](docs/protocol.md).
- `chasen deploy` builds the image where it runs, your computer or CI, and the server pulls it from there through SSH. No registry and no token, unless you want one: `ghcr.io` and Docker Hub work too. A server never builds: a build would take the memory and the CPU of the live apps.
- Under it are [kamal-proxy](https://github.com/basecamp/kamal-proxy) for HTTPS and deploys without downtime, and [Litestream](https://litestream.io) for the live replica, both inside `chasen-server`. The server needs Docker, and installs it when it has none.

## The server part

Most deploy tools run Docker commands through SSH and leave. Chasen puts one small program there, `chasen-server`, because an app with its data in a SQLite file needs someone at home:

- **Backups that run without you.** A checked snapshot of every database each hour, kept for six months, and a live replica that is a second behind. A backup before each deploy. `chasen verify` restores them next to the real files to prove they work.
- **One deploy at a time.** The server locks an app while it changes. Two terminals, or a terminal and CI, cannot deploy over each other.
- **A memory of what happened.** Every deploy, restart, and restore is in the history, with its output, also while it runs. A deploy from CI shows up live on your screen.
- **Alerts.** It tells you when an app is down, a backup is late, the disk fills up, or SSH still takes passwords.
- **It updates itself** each night, and goes back to the old version when the new one does not answer.

It runs in a container, with the Docker socket, and changes nothing else on the machine.

## The screen

`chasen` with no command opens the screen of your server: your apps and their state, the history, the backups, the domains, and the logs, live. The top line shows the load of the server and its alerts.

The keys do what the CLI does: `r` restarts, `b` backs up, `enter` on a backup restores it, `:` runs any command for the chosen app and lists them as you type, `!` shows the alerts, and `s` goes to another server. Each action shows the command line that does the same, so the screen also teaches the CLI. It has no deploy key: a deploy belongs to the directory of the app.

On [Omarchy](https://omarchy.org), it takes the colors of your theme and changes with it.

## Between Kamal and ONCE

Chasen is a mix of two ideas from 37signals.

- From [Kamal](https://kamal-deploy.org): build the image of your app, put it on a server you own, and swap containers without downtime with kamal-proxy, all from your computer.
- From [ONCE](https://once.com): one server, the data in SQLite, and an app that installs with one line and updates itself.

| | Kamal | ONCE | Chasen |
|---|---|---|---|
| What you run | Your own app | A product someone made, like Campfire | Your own apps, and the images that others release |
| How it gets there | `kamal deploy` from your computer, through a registry | One install command on the server | `chasen deploy` from your computer, through SSH, with no registry |
| Updates | When you deploy | By itself | When you deploy, or each night with `--auto-update` |

What neither has: one small program that stays on the server for checked backups, a live replica of each database, the history of every deploy, and alerts. And one server holds many apps.

## How Chasen differs from Kamal

[Kamal](https://kamal-deploy.org) and Chasen share an idea and a proxy: build an image, put it on a server you own, swap containers with kamal-proxy. They are for different jobs.

| | Kamal | Chasen |
|---|---|---|
| Made for | A team that runs an app on several servers, with roles, accessories, and its own database servers | One person with one server and many small apps, each with its data in SQLite |
| Configuration | `config/deploy.yml` with the servers, the registry, the proxy, and the accessories | None for an app on GitHub that follows [the standard](STANDARD.md). `chasen.yml` only for what differs |
| On the server | The containers and a few files. Nothing of Kamal runs between deploys | `chasen-server` runs all the time: backups, the live replica, history, alerts |
| Data | Yours to back up | Hourly checked snapshots and a live replica to S3, built in. `chasen restore` and `chasen download` |
| Watching it | `kamal app logs`, `kamal app details` | The screen: every app, its history, backups, logs, and the alerts of the server |
| Many apps on one server | Each app has its own `deploy.yml` and its own deploy | One server holds them all. `chasen list` shows them, and the screen moves between them |

When your app needs several servers or Postgres, use Kamal. When it is one app or ten on a single machine, and the data is a SQLite file you cannot lose, use Chasen.

## Documents

The docs are on [chasenhq.com/docs](https://chasenhq.com/docs/). Their source is here:

| Document | What it covers |
|---|---|
| [Get started](docs/getting-started.md) | From an empty server to a live app |
| [Deploy](docs/deploy.md) | The image, websites, addons, `chasen.yml`, and secrets |
| [The app standard](STANDARD.md) | The rules an app follows: the port, `/up`, the storage, migrations |
| [Backups and restore](docs/backups.md) | Snapshots, the live replica, a restore, and a new server |
| [Domains](docs/domains.md) | Custom domains, and Cloudflare in front |
| [Deploy from GitHub Actions](docs/github-actions.md) | `git push` as the deploy |
| [Commands, files, and limits](docs/reference.md) | Every command, the files on the server, and what Chasen does not do |
| [Chasen on Omarchy](docs/omarchy.md) | The launcher, the module of the top bar, and the theme |
| [The server protocol](docs/protocol.md) | The HTTP API between the CLI and the server |

[`example/`](example/) is the smallest app that follows the standard.

## Development

```bash
mise run test     # go vet and the unit tests. No Docker
mise run e2e      # real Docker, the proxy on ports 80 and 443, an S3 store, and a private registry. Needs sudo
```

The two programs share one contract: [`protocol`](protocol/protocol.go) for the commands and [`oauth`](oauth/oauth.go) for the login.

### Releases

A tag `v*` makes a release: GitHub Actions builds the CLI for macOS and Linux and the server for Linux, and attaches them to the release with `checksums.txt`. The install scripts download the newest release.

## Contributing

Report a problem with `chasen report`. Pull requests are small, pass all the tests, and show that they work: [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[Apache 2.0](LICENSE)
