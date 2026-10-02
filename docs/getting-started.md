# Get started

Chasen puts your app on one server that you own. `chasen` is the CLI on your computer. `chasen-server` is one binary on the server. This page takes you from an empty server to a live app.

## What you need

- **A server** with Ubuntu or Debian, root access, and ports 80 and 443 open. A small one is enough to start.
- **A domain** for the apps of the server, with a wildcard DNS record: `*.example.com` points to the address of the server. Each app then gets `<app>.example.com`.
- **Docker on your computer.** `chasen deploy` builds the image of your app there.
- **An image registry.** A repository on GitHub already has one, `ghcr.io`. Docker Hub and others work too.
- **An app with a `Dockerfile`**, in a git repository. A plain website with an `index.html` needs no Dockerfile, no Docker, and no registry.

## 1. Set up the server

Run this on the server, as root:

```bash
curl -fsSL https://chasenhq.com/server | sh
chasen-server setup --domain example.com
```

The first line downloads one binary and checks its checksum. The second line installs Docker when it is missing, starts the proxy and the API, and prints the token of the server:

```
On your machine, run:
  chasen add server example.com
Server token (the login page asks for it): 3f9a...
```

Keep the token. To see it again, run `chasen-server token` on the server.

## 2. Add a backup bucket (optional)

A server works without a bucket: the backups then stay on its disk, and a dead server takes them with it. Add a bucket when the data matters. Any S3-compatible store works: Cloudflare R2, Backblaze B2, Hetzner, or S3.

```bash
chasen-server bucket --endpoint https://<your-store> --name chasen-backups --access-key-id <id>
# Secret access key: ...
```

The command creates the bucket when it does not exist, and tests it before it saves the settings.

## 3. Install the CLI and log in

On your computer (macOS or Linux):

```bash
curl -fsSL https://chasenhq.com/cli | sh
chasen add server example.com
# Open this page to log in:
#   https://api.example.com/oauth/device?user_code=BCDF-GHJK
```

Open the page, check the code, and type the token of the server. The CLI then has a token of its own.

## 4. Log in to the registry

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

## 5. Deploy

```bash
cd shop
chasen deploy
# Building ghcr.io/you/shop:3f9a2c1d...
# Pushing ghcr.io/you/shop:3f9a2c1d...
# Pulling ghcr.io/you/shop:3f9a2c1d...
# Port 3000 (EXPOSE in the image). Health path /up. Storage /storage.
# shop: backup 20261001T120000Z (on the server and offsite)
# Starting shop 3f9a2c1
#
# Deployed shop 3f9a2c1
#   https://shop.example.com
```

`chasen deploy` builds the image of the current git commit, pushes it, and tells the server to pull it. The server backs up the databases, starts the new version next to the old one, and moves the traffic when `/up` answers. If the new version does not answer in 30 seconds, the old one keeps the traffic.

To test an app before it gets traffic, run `chasen check`.

## What comes next

| You want | Command |
|---|---|
| Your own domain | `chasen domains add shop.com` |
| See what runs | `chasen status`, `chasen logs`, `chasen history` |
| Change a setting or a secret | Edit `chasen.yml`, then `chasen restart` |
| Go back to an older version | `chasen deploy --tag <the full hash of the older commit>` |
| Restore the data | `chasen backups`, then `chasen restore` |
| Deploy on every `git push` | [Deploy from GitHub Actions](github-actions.md) |

## Limits to know

- One container for each app, with 512 MB of memory. No worker process yet.
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

To move the reboot to another hour, keep `"true"` and change `Automatic-Reboot-Time`. A server from the Chasen cloud has the reboot on at 04:00; the same file turns it off.
