# Get started

Chasen puts your app on one server that you own. `chasen` is the CLI on your computer. `chasen-server` is one binary on the server. This page takes you from an empty server to a live app.

## What you need

- **A server** with Ubuntu or Debian, root access, and ports 80 and 443 open. A small one is enough to start.
- **A domain** for the apps of the server, with a wildcard DNS record: `*.apps.example.com` points to the address of the server. Each app then gets `<app>.apps.example.com`.
- **Docker on your computer.** `chasen deploy` builds the image of your app there.
- **An image registry.** A repository on GitHub already has one, `ghcr.io`. Docker Hub and others work too.
- **An app with a `Dockerfile`**, in a git repository. A plain website with an `index.html` needs no Dockerfile, no Docker, and no registry.

## 1. Set up the server

Run this on the server, as root:

```bash
curl -fsSL https://chasenhq.com/server | sh
chasen-server setup --domain apps.example.com
```

The first line downloads one binary and checks its checksum. The second line installs Docker when it is missing, starts the proxy and the API, and prints the token of the server:

```
On your machine, run:
  chasen add server apps.example.com
Server token (the login page asks for it): 3f9a...
```

Keep the token. It is also in `/etc/chasen/config.yml`.

## 2. Set the backup bucket

Without a bucket, backups stay on the server, and a dead server takes them with it. Any S3-compatible store works: Cloudflare R2, Backblaze B2, Hetzner, or S3.

```bash
chasen-server bucket --endpoint https://<your-store> --name chasen-backups --access-key-id <id>
# Secret access key: ...
```

The command creates the bucket when it does not exist, and tests it before it saves the settings.

## 3. Install the CLI and log in

On your computer (macOS or Linux):

```bash
curl -fsSL https://chasenhq.com/cli | sh
chasen add server apps.example.com
# Open this page to log in:
#   https://api.apps.example.com/oauth/device?user_code=BCDF-GHJK
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
#   https://shop.apps.example.com
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
- Each deploy prints one `Could not pull ...` warning and waits about 6 seconds. It is harmless.

## Update the server

Run both lines of step 1 again. Keep the server as new as the CLI.
