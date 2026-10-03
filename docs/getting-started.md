# Get started

Chasen puts your app on one server that you own. `chasen` is the CLI on your computer, and it is all you run. This page takes you from an empty server to a live app, in one command.

## What you need

- **A server** with Ubuntu or Debian that you can log in to with SSH, as root or as a user that runs `sudo` with no password. Ports 80 and 443 are open for your apps. A small server is enough to start.
- **A domain for your app**, with a DNS record that points to the server: `shop.example.com`. The server itself needs no name.
- **Docker on your computer.** `chasen deploy` builds the image of your app there.
- **An image registry.** A repository on GitHub already has one, `ghcr.io`. Docker Hub and others work too.
- **An app with a `Dockerfile`**, in a git repository. A plain website with an `index.html` needs no Dockerfile, no Docker, and no registry.

## 1. Install the CLI

On your computer (macOS or Linux):

```bash
curl -fsSL https://chasenhq.com/cli | sh
```

## 2. Log in to the registry

Your app needs a place for its image. When the git origin of the app is on GitHub, that place is `ghcr.io/<owner>/<repository>`. Chasen finds the name by itself. It needs a login to push there, and it uses the one that Docker already has:

```bash
docker login ghcr.io
# Username: your GitHub name
# Password: a GitHub token with the write:packages scope (github.com/settings/tokens)
```

If you use the GitHub CLI, this does the same with no token to copy: `gh auth login -s write:packages`.

That is all: an app on GitHub needs no `chasen.yml`.

For Docker Hub or another registry, run `docker login` for it, and name the image in `chasen.yml`, in the root of the app:

```yaml
image: you/shop          # where the image goes. No tag
```

Your app also follows [the standard](../STANDARD.md): it listens on the port of its `EXPOSE` line (or on `$PORT`), answers `GET /up` with `200`, and keeps its SQLite files in `/storage`.

## 3. Deploy

```bash
cd shop
chasen deploy root@203.0.113.5 --domain shop.example.com
# chasen-server is not on this server yet. Installing it.
# Setting up the server: Docker, the proxy, and the API. This can take a minute.
# The server is ready.
# Logged in to ssh://root@203.0.113.5
# Building ghcr.io/you/shop:3f9a2c1d...
# Pushing ghcr.io/you/shop:3f9a2c1d...
# Pulling ghcr.io/you/shop:3f9a2c1d...
# Port 3000 (EXPOSE in the image). Health path /up. Storage /storage.
# shop: backup 20261001T120000Z (on the server only)
# Starting shop 3f9a2c1
#
# Deployed shop 3f9a2c1
#   https://shop.example.com
```

This one command does everything. The first time, it also makes the machine a Chasen server:

1. It logs in with your `ssh`, the same way you do: your keys, your `~/.ssh/config`, and your known hosts apply.
2. It downloads `chasen-server`, one binary, checks its checksum, installs Docker when the server has none, and starts the proxy and the API. It changes nothing else on the server.
3. It gets a login for this computer.

Then it builds the image of the current git commit, pushes it, and tells the server to pull it. The server backs up the databases, starts the new version next to the old one, and moves the traffic when `/up` answers. If the new version does not answer in 30 seconds, the old one keeps the traffic.

**After the first time, the command is `chasen deploy`.** The computer knows the server, and the app has its domain. Every command goes to the server through SSH, so the server needs no name in DNS and no certificate of its own, and the only open ports are SSH and the ports 80 and 443 of your apps.

- **A second app** on the same server: `chasen deploy --domain blog.example.com` in its directory.
- **A second computer:** `chasen add server root@203.0.113.5` logs in with no deploy.
- **Several servers:** name the server in the command each time, or in `chasen.yml` (`server: root@203.0.113.5`).

To test an app before it gets traffic, run `chasen check`.

## 4. Add a backup bucket (optional)

A server works without a bucket: the backups then stay on its disk, and a dead server takes them with it. Add a bucket when the data matters. Any S3-compatible store works: Cloudflare R2, Backblaze B2, Hetzner, or S3.

```bash
chasen bucket --endpoint https://<your-store> --name chasen-backups --access-key-id <id>
# Secret access key:
```

The server creates the bucket when it does not exist, and tests it before it saves the settings. Then the snapshots go to the bucket too, and the live replica starts.

## What comes next

| You want | Command |
|---|---|
| One more domain for the app | `chasen domains add shop.com` |
| See what runs | `chasen status`, `chasen logs`, `chasen history` |
| Give the app a secret | `chasen secrets edit`, then `chasen restart` |
| Change a setting | Edit `chasen.yml`, then `chasen restart` |
| Go back to an older version | `chasen deploy --tag <the full hash of the older commit>` |
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
- The build is for `linux/amd64`. For an ARM server, set `DOCKER_DEFAULT_PLATFORM=linux/arm64`.
- `setup` does not harden the server. `chasen-server check` reports what is missing: SSH with keys only, a firewall, and security updates.

## Updates

**The server updates itself.** Each night, at a random minute between 03:00 and 05:00, a timer runs `chasen-server update`:

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
