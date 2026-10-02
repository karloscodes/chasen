# Chasen

Enter your app. Run `chasen deploy`. It is live.

A chasen is the bamboo whisk that prepares matcha. Chasen is the tool you hold; [matcha](https://github.com/karloscodes/matcha) is the engine underneath.

Chasen deploys the Docker image of an app with a SQLite database to one server. You get HTTPS, deploys without downtime, custom domains, and backups you can trust. `chasen deploy` builds the image on your computer or in CI and pushes it to a registry. The server pulls it: nothing builds on the server. There is no YAML pipeline of ours, and the server needs no name in DNS: the CLI reaches it through SSH.

It is free and open source, and it runs on a server you own. The site is [chasenhq.com](https://chasenhq.com), and [the docs](https://chasenhq.com/docs/) start with a step-by-step guide.

```bash
curl -fsSL https://chasenhq.com/cli | sh
cd myapp
chasen deploy root@203.0.113.5 --domain myapp.example.com
# the first time, this installs Chasen on the server, through SSH
# Deployed myapp 3f9a2c1
#   https://myapp.example.com
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
| [The server protocol](docs/protocol.md) | The HTTP API between the CLI and the server |

[`example/`](example/) is the smallest app that follows the standard.

## The cloud

Chasen cloud is this same software on a server that we create for you at Hetzner: `chasen login`, then `chasen deploy`. It is a separate service and it is not in this repository. The CLI talks to it with the same protocol as to your own server. See [chasenhq.com/cloud](https://chasenhq.com/cloud/).

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
