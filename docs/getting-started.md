# Get started

Chasen puts your app on one server that you own. `chasen` is the CLI on your computer, and it is all you run. This page takes you from an empty server to a live app, in one command.

## What you need

- **A server** with Ubuntu or Debian that you can log in to with SSH, as root or as a user that runs `sudo` with no password. Ports 80 and 443 are open for your apps. A small server is enough to start.
- **A domain for your app**, with a DNS record that points to the server: `shop.example.com`. The server itself needs no name.
- **Docker on your computer.** `chasen deploy` builds the image of your app there, and sends it to the server through SSH. You need no registry and no token. If the image is already in a registry, you need no Docker either: see [An image in a registry](#an-image-in-a-registry).
- **An app with a `Dockerfile`**, in a git repository. A plain website with an `index.html` needs no Dockerfile and no Docker.

## 1. Install the CLI

On your computer (macOS or Linux):

```bash
curl -fsSL https://chasenhq.com/cli | sh
```

## 2. Deploy

```bash
cd shop
chasen deploy root@203.0.113.5 --domain shop.example.com
# chasen-server is not on this server yet. Installing it.
# Setting up the server.
#   Installing Docker. This takes a minute or two.
#   Starting the proxy.
#   Starting the API.
# The server is ready.
# Logged in to ssh://root@203.0.113.5
# Starting the registry of chasen on this computer. The image goes from here to the server, through SSH.
# Building shop 3f9a2c1
# Pulling the image from your computer, through SSH
# Port 3000 (EXPOSE in the image). Health path /up. Storage /storage.
# Starting shop 3f9a2c1
#
# Deployed shop 3f9a2c1
#   https://shop.example.com
#
# ✓ https://shop.example.com answers from here.
```

This one command does everything. The first time, it also makes the machine a Chasen server:

1. It logs in with your `ssh`, the same way you do: your keys, your `~/.ssh/config`, and your known hosts apply.
2. It downloads `chasen-server`, one binary, checks its checksum, installs Docker when the server has none, and starts the proxy and the API. It changes nothing else on the server.
3. It gets a login for this computer.

Then it builds the image of the current git commit with your Docker, for the CPU of the server. The image goes to a small registry on your computer (the container `chasen-registry`, which listens only on your computer), and the server pulls it from there through SSH. Nothing goes to the internet, and the next deploy sends only the layers that changed.

The server backs up the databases, starts the new version next to the old one, and moves the traffic when `/up` answers. If the new version does not answer in 30 seconds, the old one keeps the traffic, and the deploy says why. At the end, the CLI asks the address of the app from your computer and says whether it answers, or what to fix: DNS, a firewall, or the certificate.

**After the first time, the command is `chasen deploy`.** The computer knows the server, and the app has its domain. Every command goes to the server through SSH, so the server needs no name in DNS and no certificate of its own, and the only open ports are SSH and the ports 80 and 443 of your apps.

- **A second app** on the same server: `chasen deploy --domain blog.example.com` in its directory.
- **A second computer:** `chasen add server root@203.0.113.5` logs in with no deploy.
- **Several servers:** name the server in the command each time, or in `chasen.yml` (`server: root@203.0.113.5`).

To test an app before it gets traffic, run `chasen check`.

## 3. The defaults, and how to change them

An app needs no `chasen.yml` when it follows [the standard](../STANDARD.md). Each rule has a default. When your app does something else, write the difference in `chasen.yml`, in the root of the app:

| What | The default | In `chasen.yml` |
|---|---|---|
| The name of the app | the name of the directory | `name: shop` |
| The port the app listens on | the `EXPOSE` of the image, else 8080. The app also gets it as `$PORT` | `port: 3000` |
| The health check | `GET /up` answers `200` within 30 seconds | `health: /healthz` and `health_timeout: 90` |
| Where the data is | the `VOLUME` of the image, else `/storage` (also at `/rails/storage`) | `volumes: [/app/data]` |
| The memory | 512 MB | `memory: 1g` |
| Backups | hourly snapshots, and the live replica when the server has a bucket | `backup: false`, for data the app makes again at each start |
| The server | the current one: `chasen servers` lists them | `server: root@203.0.113.5` |
| The image | built here, and sent through SSH | `image: ghcr.io/you/shop`: see below |
| Settings and secrets | none | `env:` for settings. Secrets go in `chasen secrets edit` |

You do not need to guess: when a deploy fails, it says what the app did, and the line to write. [Deploy](deploy.md#chasenyml) has every key.

## 4. Add a backup bucket (optional)

A server works without a bucket: the backups then stay on its disk, and a dead server takes them with it. Add a bucket when the data matters. Any S3-compatible store works: Cloudflare R2, Backblaze B2, Hetzner, or S3.

```bash
chasen bucket --endpoint https://<your-store> --name chasen-backups --access-key-id <id>
# Secret access key:
```

The server creates the bucket when it does not exist, and tests it before it saves the settings. Then the snapshots go to the bucket too, and the live replica starts.

## The setup I recommend

Two free services make a server much harder to attack, and Chasen needs no setting for either of them:

1. **Cloudflare in front of your apps.** Turn the proxy on for the DNS records of your apps, and set SSL to Full (strict). Visitors see the addresses of Cloudflare, not the one of your server, and Cloudflare takes the load of a traffic spike. Then let ports 80 and 443 of the server take traffic only from Cloudflare. [Domains](domains.md) has the details.
2. **SSH only from your private network.** Install [Tailscale](https://tailscale.com) on the server and on your computer: it is the easiest. To run the network yourself, use [Headscale](https://github.com/juanfont/headscale) or [NetBird](https://netbird.io), which are open source, or plain [WireGuard](https://www.wireguard.com). Then close SSH to the internet. `chasen deploy` works the same, with the name of the server in the network: `chasen deploy root@shop-server`.

With both, the internet sees ports 80 and 443 through Cloudflare, and nothing else. `chasen alerts` tells you what is still open. [Keep SSH off the internet](reference.md#keep-ssh-off-the-internet) has the firewall lines.

## An image in a registry

The default needs no registry. Use one when:

- **The server is on the web**, with a base domain and a token: GitHub Actions, or the [cloud](https://chasenhq.com/cloud/). There is no SSH to send the image through, so the image goes to `ghcr.io/<owner>/<repository>`, from the git origin of the app on GitHub. [Deploy from GitHub Actions](github-actions.md) has the steps.
- **You want the image in a registry anyway.** Name it in `chasen.yml`, and Chasen pushes it there, also through SSH: `image: ghcr.io/you/shop`, or `image: you/shop` for Docker Hub. No tag: the tag is the commit.
- **The image is not yours to build**: an app that someone else releases, and you have no code of. Make a directory with only a `chasen.yml`, and no `Dockerfile`:

```yaml
name: analytics
image: someone/analytics   # deploys the newest one. For one version: chasen deploy --tag 2.7.6
port: 8080
volumes: [/app/storage]
```

`chasen deploy` in that directory pulls the image on the server. It builds nothing, so it needs no Docker on your computer.

**The login to the registry.** A public image needs none. For a private one, Chasen uses the login that Docker already has, and sends it with the deploy, so the server can pull:

```bash
docker login ghcr.io
# Username: your GitHub name
# Password: a classic GitHub token with the write:packages scope (github.com/settings/tokens)
```

If you use the GitHub CLI, this does the same with no token to copy: `gh auth login -s write:packages`. In CI, Chasen takes `GHCR_TOKEN` or `GITHUB_TOKEN`.

## What comes next

| You want | Command |
|---|---|
| A second address for the same app, like `www.shop.example.com` or an old name | `chasen domains add www.shop.example.com` |
| See what runs | `chasen status`, `chasen logs`, `chasen history` |
| Give the app a secret | `chasen secrets edit`, then `chasen restart` |
| Change a setting | Edit `chasen.yml`, then `chasen restart` |
| Go back to the version before | `chasen rollback`: in seconds, with the same data |
| Restore the data | `chasen backups`, then `chasen restore` |
| Deploy on every `git push` | [Deploy from GitHub Actions](github-actions.md) |

## A base domain, and an address on the web (optional)

Give the server a base domain when you want two more things:

- **A name for each new app with no flag.** With the base domain `example.com` and a wildcard DNS record (`*.example.com` points to the server), a new app gets `<app>.example.com`.
- **The API on the web**, at `https://api.example.com`. Then a computer needs no SSH access to the server: it logs in with a browser, and CI deploys with a token.

Run this on the server, as root:

```bash
chasen-server setup --domain example.com
```

Then, on a computer:

```bash
chasen add server example.com
# Open this page to log in:
#   https://api.example.com/oauth/device?user_code=BCDF-GHJK
```

Open the page, check the code, and type the token of the server (`chasen-server token` prints it). The CLI then has a token of its own. The SSH way keeps working next to it.

If the firewall of the server lets in only the addresses of Cloudflare, every name of the server must go through Cloudflare: turn the proxy on for the wildcard record too.

## Limits to know

- One container for each app, with 512 MB of memory unless `memory:` in `chasen.yml` says more. No worker process yet.
- SQLite only. Uploaded files persist across deploys, but they have no backup yet.
- The build is for the CPU of the server, `amd64` or `arm64`. When your computer has another one, the build runs under emulation and takes longer.
- `setup` does not harden the server. `chasen alerts` reports what is missing, with the fix: SSH with keys only, a firewall, and security updates.

## Updates

**Chasen updates itself on your server.** Each night, at a random minute between 03:00 and 05:00, a timer runs `chasen-server update`. It updates Chasen only, not the operating system:

- It compares the checksum of the newest release with the binary that runs. When they are the same, nothing happens.
- It keeps the old binary as `chasen-server.previous`, installs the new one, and starts the API again. Your apps do not restart.
- When the new API does not answer in 30 seconds, it puts the old binary back.
- It never interrupts a deploy: it waits for the next night.

```bash
chasen-server update                      # update now
systemctl list-timers chasen-update.timer # when the next run is
```

To turn the nightly update off, run `chasen-server settings auto_update off`. `chasen-server settings` shows what is set.

**The CLI tells you, and you update it.** Once a day `chasen` looks for a newer release. When there is one, it says so in one line after your command. Then run `chasen update`: it downloads the release, checks its checksum, and replaces itself. It does not change by itself, because it also runs in CI, where a program that changes between two commands is a surprise. In CI it does not look at all.

**The operating system is yours.** Chasen does not change how your server installs its own updates. On Ubuntu, turn on automatic security updates, and let the server reboot at night when an update needs it:

```bash
apt-get install -y unattended-upgrades
cat > /etc/apt/apt.conf.d/52chasen-reboot <<'CONF'
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
CONF
```

A reboot is safe: Docker starts at boot, and the proxy, the API, and your apps start again by themselves. It costs about one minute of downtime.

**You can turn the automatic reboot off.** Do this when one minute of downtime at night is not acceptable, and you want to choose the moment yourself:

```bash
echo 'Unattended-Upgrade::Automatic-Reboot "false";' > /etc/apt/apt.conf.d/52chasen-reboot
```

Ubuntu still installs each security update. A new kernel then waits on the disk: the server runs the old kernel, with its known holes, until you reboot. So check and reboot yourself:

```bash
cat /var/run/reboot-required   # the file exists when a reboot is waiting
reboot
```

To move the reboot to another hour, keep `"true"` and change `Automatic-Reboot-Time`.

**The server knows its quiet hour.** The proxy logs each request, so the server can count them. This command prints the hour of the day with the fewest requests to your apps in the last week, in the time zone of the server:

```bash
chasen-server quiet-hour   # 03:00
```

Put that hour in `Automatic-Reboot-Time`. The command needs one full day of log before it answers.

A server from the Chasen cloud has the reboot on. It starts at about 04:00 in its own region, and moves to its quiet hour each week. The same file turns it off.
