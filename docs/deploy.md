# Deploy

```bash
cd myapp
chasen deploy
```

The deploy pulls the image, makes a backup, starts the new container, and moves the traffic when `/up` answers. If the new version does not become healthy in time, the old version keeps the traffic.

A deploy runs to its end on the server, also when your connection drops.

## The domain of an app

The first deploy of an app gives it its domain:

```bash
chasen deploy --domain shop.example.com
```

The first deploy to a server names the server too, and that is the whole setup of the server:

```bash
chasen deploy root@203.0.113.5 --domain shop.example.com
```

Point a DNS record for that name to the server. The certificate comes on the first HTTPS request after DNS resolves. After the first deploy the app keeps its domains, and the command is `chasen deploy`. `chasen domains add` gives it more.

A server with a base domain (`chasen-server setup --domain example.com`, with a wildcard DNS record) needs no flag: a new app gets `<app>.example.com`.

## The image

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
- **In CI it is the same command.** [Deploy from GitHub Actions](github-actions.md) has the workflow, the two secrets it needs, and what to do when it fails. With it, `git push` is the deploy.
- **The login of the registry** is used for the push, and it goes with the deploy for the pull. The server does not keep it. In GitHub Actions it is the token of the job, so there is no long-lived token to store.
- **`chasen deploy --tag <tag>`** deploys an image that is already in the registry, and builds nothing. This is the rollback (`--tag <older commit>`), and the way to deploy an image that another system built.
- **The architecture.** The build is for `linux/amd64`, like most servers. For another server, set `DOCKER_DEFAULT_PLATFORM`.

## An image that another repository releases

Some apps are not yours to build: a product that ships as an image, like an analytics tool or a form backend. You only run it. Keep a folder for it with a `chasen.yml` that names the image, and no `Dockerfile`:

```yaml
# analytics/chasen.yml
image: karloscodes/fusionaly
health: /_health
volumes: [/app/storage]
```

```bash
chasen deploy                 # the newest image
chasen deploy --tag 2.7.6     # one version
```

- **Nothing is built,** and the folder needs no git commit. The name of the app is the name of the folder.
- **With no tag, the server pulls `latest` and names the version itself:** the version that the image has in its label (`org.opencontainers.image.version`), or the start of its id. So `chasen status` and `chasen history` say what runs, not "latest".
- **The settings of your server stay in your folder,** not in the repository of the product.


A directory with an `index.html` and no `Dockerfile` deploys as a static website. Chasen serves the files of the commit with Caddy. A site that needs a build step (Astro, Hugo) is an image like any app: it has a `Dockerfile` like any app.

## Addons

An addon is a ready product that Chasen runs from its image. There is no source and no build. It gets a domain, HTTPS, and the same backups as your apps.

```bash
chasen enable lognorth                    # https://lognorth.example.com
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

## History

```bash
chasen history
# ID  WHEN (UTC)           ACTION                      RESULT
# 5   2026-10-01 12:03:21  deploy 6cff7df              failed
# 4   2026-10-01 12:03:02  restore                     succeeded
# 3   2026-10-01 12:02:44  domains add shop.com        succeeded
chasen history 5      # the full output of that deploy
```

The server records each deploy, restore, domain change, and removal, with its output.

## The review before a deploy

`chasen deploy` and `chasen check` review the app before they send anything. The review has two parts:

1. **`chasen.yml`**, when the directory has one. Before the build.
2. **The image**, after the build and before the push. Chasen reads what the image says about itself, the same way the server does.

Each finding says what is wrong, what to write, and which page explains it.

- **An error** is something that cannot work. The deploy stops. Nothing is pushed, and nothing changes on the server.
- **A warning** is something that often ends in a failed deploy or in lost data, and that can also be right. The deploy goes on.

| Finding | Level |
|---|---|
| An unknown key in `chasen.yml` | error |
| An app name that is not lowercase letters, digits, and hyphens | error |
| A port, a health path, a health timeout, or a volume that is not valid | error |
| `env:` or `secrets:` has a name that Chasen sets itself (`PORT`, `BASE_URL`, and the others of [the standard](../STANDARD.md#4-environment)). `SECRET_KEY_BASE` in `secrets:` is right: then the key is yours | error |
| `registry.password` holds a token, not the name of a secret | error |
| The image declares several ports, none is 80, and `chasen.yml` has no `port:` | error |
| The image has no command (`CMD` or `ENTRYPOINT`) | error |
| The `Dockerfile` has no `EXPOSE`, and `chasen.yml` has no `port:` | warning |
| The `Dockerfile` has no `VOLUME`, and `chasen.yml` has no `volumes:` | warning, at the first deploy of the app |
| A value in `env:` has the name of a secret (`…_TOKEN`, `…_PASSWORD`) | warning |
| A name is in `env:` and in `secrets:` | warning |
| `image:` has a tag and the directory has a `Dockerfile`: nothing is built | warning |

A `Dockerfile` that gets no finding says three things:

```dockerfile
EXPOSE 3000              # the port of the app. Chasen also gives it as $PORT
VOLUME /app/storage      # where the app keeps its data. Chasen keeps it and backs it up
CMD ["/app/server"]      # how the app starts
```

The review reads files. It does not start the app. To test a running container against every rule, with no traffic, run `chasen check`.

## chasen.yml

The file is optional for a website, and for an app whose git origin is on GitHub. Without `name:`, the app name is the name of the directory. Without `image:`, the image is `ghcr.io/<owner>/<repository>` of the git origin.

```yaml
name: myapp
env:
  LOG_LEVEL: info

# Overrides of the standard. Leave them out when the defaults fit.
port: 3000
health: /_health
health_timeout: 90
volumes: [/app/storage]

# For an app whose data needs no backup, like a demo that makes its data again at each start.
backup: false
```

## Secrets

A secret is a value that must not be in git as plain text, and not in the image: an API key, a password. Chasen keeps the secrets of an app the way Rails keeps its credentials: one encrypted file in the repository, and one key outside it.

```bash
chasen secrets edit
```

The command opens the secrets in your editor (`$VISUAL`, then `$EDITOR`). An editor with a window works with its plain name: for VS Code, Cursor, Zed, and Sublime Text, Chasen adds the option that makes the command wait until you close the file (`EDITOR=code` is enough). The secrets are lines of `NAME=value`:

```
STRIPE_KEY=sk_live_...
SMTP_PASSWORD=...
```

When you close the editor, Chasen encrypts them into `chasen.secrets.enc`. Commit that file. Every secret in it goes to the app at the next deploy, as an environment variable: there is no list to keep in `chasen.yml`.

The first `chasen secrets edit` makes the key and prints it:

| File | What it is | In git |
|---|---|---|
| `chasen.secrets.enc` | the secrets, encrypted | yes |
| `chasen.key` | the key that opens them | no: Chasen adds it to `.gitignore` |

Save the key in a password manager. If you lose it, nobody can read the secrets. On another computer, put the key back in `chasen.key`. In CI, give it as `CHASEN_KEY`.

One key at the top of a repository opens the secrets of every app in it: Chasen looks for `chasen.key` in the directory of the app, then in each directory above it.

```bash
chasen secrets         # the names
chasen secrets show    # the names and the values
chasen restart         # give the app the secrets of now, with no build
```

**The secret key of the app.** At the first deploy of a new app that has a secrets file, Chasen makes `SECRET_KEY_BASE` and saves it in that file. So the key that signs the sessions of the app is in your repository too, and a new server gives the app the same one. Commit the file after the first deploy.

Secrets travel with the deploy on purpose. Nothing that matters lives only on the server, so a new server needs one `chasen deploy` to get the configuration, the secrets, and the data back. On the server, the values are in a database that only root can read.

### Secrets from another tool

You have a secret manager already? Keep it. `secrets:` in `chasen.yml` lists the names, and `secrets_command` is any command that prints `KEY=VALUE` lines:

```yaml
secrets: [STRIPE_KEY, SMTP_PASSWORD]
secrets_command: op inject -i .env.tpl
```

| Tool | `secrets_command` |
|---|---|
| 1Password | `op inject -i .env.tpl` |
| fnox | `fnox export` |
| sops | `sops -d secrets.enc.env` |

Chasen takes each name from the output of the command, then from the environment, then from `chasen.secrets.enc`. So a value in the environment wins over the file: CI can replace one secret without the key. A missing secret stops the deploy before it changes anything.
