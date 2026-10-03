# Deploy

```bash
cd myapp
chasen deploy
```

The deploy pulls the image, makes a backup, starts the new container, and moves the traffic when `/up` answers. If the new version does not become healthy in time, the old version keeps the traffic, and the deploy says why from what the new container left:

```
The new version did not answer 200 on /up at port 8080 within 30 seconds. The old version keeps the traffic.
It listens on port 3000, not on 8080. Add EXPOSE 3000 to the Dockerfile, or port: 3000 to chasen.yml.

Its last lines:
  Listening on http://0.0.0.0:3000
```

It names the port the app listens on, an exit code, or a container that ran out of memory, and shows the last 40 lines of its output.

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

`chasen deploy` builds the image of the current git commit with your Docker, for the CPU of the server, and the server pulls it. The tag is the full hash of the commit. Where the image goes between the two depends on how the CLI reaches the server:

- **Through SSH (the default): from your computer.** The image goes to a small registry in Docker on your computer, `chasen-registry`, which listens only on `127.0.0.1:5555`. For the time of the deploy, SSH opens a port on the server that leads back to it, and the server pulls through that port. No registry on the internet, no login, no token. The next deploy sends only the layers that changed. The SSH server must allow port forwarding, which is its default (`AllowTcpForwarding yes`).
- **On the web (CI with a token, the cloud): through a registry.** A repository on GitHub needs no setting: when the git origin is `github.com/you/shop`, the image goes to `ghcr.io/you/shop`. Chasen uses the login that is already on your computer: `docker login ghcr.io` (with your GitHub name and a classic token that has the `write:packages` scope), or the `gh` CLI (`gh auth login -s write:packages`). In CI it is `GHCR_TOKEN` or `GITHUB_TOKEN`.

To use a registry also through SSH, or another registry, `chasen.yml` names the image, without a tag:

```yaml
name: shop
image: you/shop           # Docker Hub. Or registry.example.com/you/shop
registry:                 # the login of the registry, when docker login is not enough
  username: you
  password: REGISTRY_TOKEN   # the name of a secret, not its value
```

- **The build gets the files of the commit**, not your working directory. So the image is what its tag says.
- **In CI it is the same command.** [Deploy from GitHub Actions](github-actions.md) has the workflow, the two secrets it needs, and what to do when it fails. With it, `git push` is the deploy.
- **The login of the registry** is used for the push, and it goes with the deploy for the pull. The server does not keep it. In GitHub Actions it is the token of the job, so there is no long-lived token to store.
- **`chasen deploy --tag <tag>`** deploys an image that is already in the registry, also the one on your computer, and builds nothing. This is the way to deploy an image that another system built.
- **The CPU type.** The build is for the CPU of the server, which the server tells the CLI: `amd64` or `arm64`. When your computer has another one, the build runs under emulation and takes longer. `DOCKER_DEFAULT_PLATFORM` wins over it.

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

## Roll back

A version that does not answer `/up` never gets traffic: the old version keeps running, and the deploy fails. So a rollback is for a version that starts and has a bug.

```bash
chasen rollback
# Starting shop 3f9a2c1 again, the version before 6cff7df
# Rolled back shop to 3f9a2c1. The data is as it was: chasen restore brings back a backup.
```

It takes seconds: the server keeps the images of the last 5 versions, like Kamal, so nothing is built or pulled. Run it again to go forward to the newer version.

**The data stays as it is.** When the bad version changed the database, bring back the backup that Chasen made just before its deploy:

```bash
chasen backups                    # the newest first
chasen restore 20261001T120000Z
```

**An older version** than the one before: check out its commit and deploy it. The build comes from the cache of Docker, so it is fast:

```bash
git checkout 727846a
chasen deploy
```

When CI pushes your images to a registry, deploy the image that is there, with no build: `chasen deploy --tag <the full hash of the commit>`.

## Background jobs

Jobs run in a container of their own, like the job role of Kamal. Name its command in `chasen.yml`:

```yaml
jobs: bin/jobs          # Rails 8: Solid Queue on its own
```

The jobs container runs the image of the app with that command, and with the same env, secrets, volumes, and memory limit. It has no domain and no proxy. Chasen keeps the queue, the cron jobs, and nothing else: your app needs no code for Chasen.

**The web and the jobs deploy as one unit.** A deploy, a restart, and a rollback first move the web to the new version, as always: the new version runs its migrations before its jobs start. The old jobs container keeps working meanwhile. Then the new jobs container starts, and it must run for 10 seconds:

- When it does, the old jobs container gets `SIGTERM`, and 30 seconds to finish its job, like Kamal.
- When it stops, the web goes back to the version before, and the old jobs container never stopped: both are old again, and the deploy fails with the last lines of the jobs.

A restore stops the jobs before it touches the databases, and starts them again after. `chasen status` shows the jobs container, `chasen logs jobs` follows it, and `chasen alerts` says when it does not run.

Three rules for the jobs themselves:

1. **The queue is in a database of the app, in the storage.** Solid Queue for Rails 8, River or goqite for Go, or a `jobs` table and a loop. Rails 8 keeps the queue in a file of its own, `storage/production_queue.sqlite3`: that is fine, because Chasen backs up every SQLite file in the storage, with its live replica. A restore brings back the data and the queue from the same backup, so no job points to a row that is gone.
2. **A job can run twice.** On `SIGTERM`, stop taking new jobs, and finish the one that runs if it takes less than 30 seconds. A job that is cut off goes back to the queue and runs again. During a deploy, the old and the new jobs container run together for a few seconds: a real queue locks each job in the database, and a plain timer can run twice.
3. **For Rails, take the jobs out of Puma.** Remove `SOLID_QUEUE_IN_PUMA` from the env, so the web container does not run them too.

| You need | Use |
|---|---|
| Work in the background | `jobs:` in `chasen.yml` |
| A command on a schedule | `cron:` in `chasen.yml`, below, or the scheduler of your queue |
| A command one time, by hand | `chasen run bin/rails jobs:retry_all` |

## Cron jobs

A cron job is a command that the server runs in the container of your app, on a schedule. Put it in `chasen.yml`:

```yaml
cron:
  - schedule: "0 4 * * *"          # each day at 04:00 UTC
    run: bin/rails demo:reset
  - schedule: "*/15 * * * *"       # every 15 minutes
    run: sh -c 'bin/sync >> /storage/sync.log'
```

Then run `chasen deploy`, or `chasen restart` to change only the schedule. The jobs travel with the settings of the app, like `env:`.

- **The schedule** has the 5 fields of cron: minute, hour, day of the month, month, and day of the week. The time is UTC. `@hourly`, `@daily`, `@weekly`, and `@monthly` work too.
- **The command** runs like `chasen run`: in the container that runs now, with the env and the storage of the app. There is no shell: for pipes, `>>`, or `$VARIABLES`, write `sh -c '...'`.
- **The history** keeps each run as `cron <command>`, with its output and whether it succeeded: `chasen history`. It keeps the newest 20 runs of each job.
- **A job that still runs** does not start again: that run is skipped.
- **A restart of the API**, like the nightly update, skips no run. After the server was off for more than 10 minutes, the runs that it missed do not run.

An app has 20 jobs at most. For work that is not on a schedule, see [Background jobs](#background-jobs).

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
| A port, a health path, a health timeout, a volume, or a memory value that is not valid | error |
| `env:` or `secrets:` has a name that Chasen sets itself (`PORT`, `BASE_URL`, and the others of [the standard](../STANDARD.md#4-environment)). `SECRET_KEY_BASE` in `secrets:` is right: then the key is yours | error |
| `registry.password` holds a token, not the name of a secret | error |
| The image declares several ports, none is 80, and `chasen.yml` has no `port:` | error |
| The image has no command (`CMD` or `ENTRYPOINT`) | error |
| A Rails app has credentials and no `RAILS_MASTER_KEY`: no key file on this computer, and none in the secrets | warning |
| A value in `env:` has the name of a secret (`…_TOKEN`, `…_PASSWORD`) | warning |
| A name is in `env:` and in `secrets:` | warning |
| `image:` has a tag and the directory has a `Dockerfile`: nothing is built | warning |

A `Dockerfile` can say three things. Only the command is required: without `EXPOSE`, the traffic goes to port 8080 and the app gets `PORT=8080`, and without `VOLUME`, the storage is `/storage`. When the app does something else, the failed deploy says so, with the line to add.

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
memory: 1g              # the most memory the app may use. The default is 512m

# For an app whose data needs no backup, like a demo that makes its data again at each start.
backup: false

# The command of the jobs container. See Background jobs.
jobs: bin/jobs

# Commands on a schedule, in UTC. See Cron jobs.
cron:
  - schedule: "0 4 * * *"
    run: bin/rails demo:reset
```

## Secrets

The secrets of an app, like an API key or a password, are in one encrypted file in your repository, `chasen.secrets.enc`, and `chasen secrets edit` changes them. Every secret goes to the app at each deploy, as an environment variable. [Secrets](secrets.md) explains the file, the key, CI, and what to do when a key is lost or leaks.
