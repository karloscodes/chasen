# The Chasen standard

**Status: the rules below are built and tested, except where a section says "not built".** Not built: the `release:` command, and backups of files that are not SQLite databases.

An app that follows the standard, in a repository on GitHub, deploys with no configuration. Each rule has a default, and `chasen.yml` can change it. An app that breaks a rule does not get traffic: the deploy fails and the old version stays.

The defaults match [ONCE](https://github.com/basecamp/once) from 37signals where that costs nothing. So an app that runs on ONCE, and a new Rails 8 app with its default Dockerfile, deploys on Chasen with no changes.

## Summary

| # | Rule | Default | Override in `chasen.yml` |
|---|---|---|---|
| 1 | The app is a Docker image in a registry. `chasen deploy` builds and pushes it; the server pulls it | `ghcr.io/<owner>/<repository>` of the git origin, tagged with the git commit | `image:` and `registry:` for another registry. `--tag <tag>` for another tag |
| 2 | It serves HTTP on one port | the port the image declares with `EXPOSE`, else `8080`. Also given as `$PORT` | `port: 3000` |
| 3 | It answers the health path | `GET /up` returns `200` | `health: /_health` |
| 4 | It becomes healthy in time | 30 seconds | `health_timeout: 90` |
| 5 | It keeps what must persist in its storage | the paths the image declares with `VOLUME`, else `/storage` | `volumes: [/app/storage]` |
| 6 | Its databases are SQLite files in that storage | any name, any number | none |
| 7 | It migrates its database before it is healthy | at start | none yet (`release:` is not built) |
| 8 | It reads its configuration from the environment | see "Environment" | `env:` |
| 9 | It logs to stdout and stderr | | none |
| 10 | It stops within 10 seconds of `SIGTERM` | | none |

A directory with an `index.html` and no `Dockerfile` is a static website. It needs no image and no registry: Chasen copies its files into a Caddy image that follows all ten rules. This is the only image that a server makes.

## The Dockerfile is the contract

Stacks differ. A Rails image has a small proxy inside and listens on 80. A Go or Node app listens on 8080 or 3000 with no proxy. Chasen does not pick one number for all of them. It reads what the image already says:

| The image says | Chasen does |
|---|---|
| `EXPOSE 3000` | sends traffic to port 3000, and sets `PORT=3000` |
| `EXPOSE 80 443` | uses 80. When an image declares several ports, 80 wins. Otherwise `port:` is required |
| no `EXPOSE` | uses 8080 and sets `PORT=8080`. An app that listens on `$PORT` works |
| `VOLUME /app/storage` | keeps `/app/storage` across deploys and backs up its databases |
| no `VOLUME` | keeps `/storage` (also mounted at `/rails/storage`) |

`port:` and `volumes:` in `chasen.yml` win over the image. So an app has three ways to pass rule 2: declare `EXPOSE`, listen on `$PORT`, or set `port:`.

## 1. Health path

Chasen asks the health path to decide one thing: can this container take traffic now?

**What `200` must mean:**

- The process is up and listens on the port.
- The migrations are done (rule 7).
- The app can open its database and read from it.

**What the health path must not do:**

- It must not need a login, a cookie, or a certain `Host` header. Chasen calls it inside the server, over plain HTTP, by the name of the container.
- It must not redirect. A redirect to HTTPS or to a login page is a failure. In Rails, exclude `/up` from `force_ssl` and from host authorization.
- It must not call other services (email, payment, an API). Their outage must not block your deploy.
- It must not change anything, and it must answer in under one second.

**When Chasen asks:** once a second after the new container starts, until the first `200` or until the timeout. On the first `200` the traffic moves. On the timeout the new container is removed and the old one keeps the traffic.

## 2. Storage

Everything that must outlive a deploy is in the storage. Everything else is gone when the next version starts.

- **Path.** The paths the image declares with `VOLUME`. Without a declaration: `/storage`, and for Rails the same directory is also at `/rails/storage`. `$STORAGE_DIR` holds the first path.
- **Owner.** The directory belongs to the user of the image, so an image that does not run as root can write to it.
- **Databases.** Every SQLite file in the storage is a database of the app, whatever its name and wherever it is in the tree. Chasen finds them by their content.
- **Other files.** Uploads and other files persist across deploys. They have no backup yet (see "Backups").
- **Reserved names.** Chasen does not look inside directories named `backups`, or named `pre-restore-…`, or ending in `-litestream`. Do not keep a live database in a directory with such a name.
- **Not for the storage.** Caches, temporary files, and sessions that can be lost. Put them in `/tmp`.
- **Several volumes.** `volumes:` can list more than one path. The last part of each path must be different (`/app/storage` and `/app/logs`, not `/a/data` and `/b/data`).

## 3. Migrations

**When.** Before the app is healthy. There are two ways, and the app picks one:

| | Default: at start | Override: `release:` |
|---|---|---|
| How | The container migrates in its entrypoint, then starts the server | Chasen runs the command in a one-time container of the new image, then starts the app |
| A failed migration | The container never answers the health path. The deploy fails | The deploy stops before anything starts |
| Good for | Most apps. Rails does this by default (`db:prepare`) | Slow migrations, and apps that should not hold migration code in the entrypoint |

In both ways, Chasen makes a checked backup of every database before the migration runs.

**What a migration must ensure:**

1. **It is safe to run again.** A container can restart at any time. A migration that already ran must do nothing the second time.
2. **It is all or nothing.** Run each migration in a transaction. SQLite can roll back schema changes, so a failed migration leaves the database as it was.
3. **The old version keeps working.** During the swap, the old and the new container use the same database for a few seconds, and the old one still serves traffic. So a migration only adds: new tables, new columns with a default. Removing or renaming happens in a later deploy, when no running version needs the old shape.
4. **It fits in the health timeout.** A migration that needs minutes raises `health_timeout`, or uses `release:`, or moves its data in the background after the deploy.
5. **It waits for the lock.** Open the database with a busy timeout (5 seconds or more) and in WAL mode, so the old version and the migration do not fail each other.
6. **It only goes forward.** There is no down migration at deploy. The way back is `chasen restore`, which returns the databases to the backup made before the deploy. Writes made after that backup are lost, so decide fast.

## 4. Environment

**Chasen sets these in every container.** An app must not set them itself, and `chasen.yml` cannot change them:

| Variable | Value | Same as |
|---|---|---|
| `PORT` | the port of rule 2: from `port:`, from `EXPOSE`, or 8080 | Heroku |
| `BASE_URL` | `https://` + the main domain of the app | ONCE |
| `SECRET_KEY_BASE` | a random secret, made once for each app and kept across deploys | ONCE, Rails |
| `PRIVATE_KEY` | the same secret | matcha |
| `STORAGE_DIR` | the first storage path of rule 5 | |
| `DATABASE_PATH` | `$STORAGE_DIR/db.sqlite3`: a suggestion for an app with one database | |
| `APP_VERSION` | the git commit | |
| `APP_ENV` | `production` | |

**Rules for the app:**

- All configuration comes from environment variables. No config file that differs between your machine and the server.
- The variables exist when the container runs. They do not exist during the build of the image, so a build must not need a secret of the app. One value is there at build time: `chasen deploy` passes `APP_VERSION` as a build argument (`ARG APP_VERSION`).
- A missing variable that the app needs is an error at start, with the name of the variable in the message. Do not start with a silent default for a secret.
- A change of a variable takes effect at the next deploy, as a new container. Chasen never changes the environment of a running container.

**Names to use for common settings**, so apps and addons agree (the ONCE names): `SMTP_ADDRESS`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAILER_FROM_ADDRESS`.

Chasen removes names it sets today: `APP_HOST` and `APP_URL` (use `BASE_URL`), and `DATA_DIR` (use `STORAGE_DIR`).

## 5. Secrets

A secret is a variable whose value must not be in git or in the image.

**One fact first, true for every design:** a container gets its secrets as environment variables, and Docker stores those on the server. Whoever is root on the server can read them. No design below changes that. What a design can change is where else the secret is, and who must have it to deploy.

Three designs:

| | A. With the deploy (built today) | B. Held on the server | C. Sealed in git |
|---|---|---|---|
| How | `chasen.yml` lists the names. The deploy reads the values from a command (`fnox export`) or the environment and sends them | `chasen env set KEY=value` stores them on the server. A deploy sends none | The repository holds each secret encrypted with the public key of the server. Only the server can decrypt |
| Source of truth | Your secret store | The server | git, plus one private key |
| A new server after a loss | `chasen deploy`: config, secrets, and data come back in one command | The secrets are gone. You set each one again, or Chasen keeps a copy in the backup bucket | You put the private key on the new server first |
| Who needs the values to deploy | Everyone who deploys, and CI | Nobody | Nobody |
| Rotate a secret | Change it in the store, then deploy | One command, no deploy | Seal the new value, commit, deploy |
| Extra tool | A secret manager, or plain environment variables | None | None, but Chasen then holds keys and crypto code |
| Like | Kamal | Heroku | Rails credentials, sealed secrets |

**Decided on 1 October 2026: A, with two repairs.**

The server must stay disposable. The promise on the landing page is that a dead server costs one command. Only A keeps that promise without a second step, because nothing that matters lives only on the server. B makes the server the one place where the secrets are. C makes it the one place where the key is.

A also keeps Chasen out of the business of storing and encrypting secrets. The tools for that exist and are good.

The two repairs:

1. **`chasen restart`.** Send the current configuration and secrets and start the app again, from the image it has. Rotating a secret is then seconds, not a full deploy. This is built.
2. **One recommended tool, documented end to end: fnox.** It keeps secrets encrypted in the repository (age) or reads them from 1Password, and `fnox export` prints what Chasen reads. In CI, the values come from the CI's own secrets as environment variables. That path works today.

An earlier draft of this document recommended B. It is simpler on day one. It fails on the day the server dies.

## 6. Logs and signals

- **Logs.** Write them to stdout and stderr, one event per line. `chasen logs` shows them. Do not write log files into the storage.
- **Stop.** On `SIGTERM`, finish the open requests and exit within 10 seconds. After that the container is killed. An app that ignores `SIGTERM` adds 10 seconds to every deploy.

## Backups

- Every SQLite file in the storage gets hourly snapshots and the live replica.
- Other files in the storage persist, but they have no backup yet. This is the largest gap in the standard: ONCE backs up the whole volume.
- The app needs no backup hook. A snapshot of a SQLite file is consistent while the app writes.

## Limits an app must fit

- One container for each app. No second process type (a worker) yet.
- 512 MB of memory. The engine sets it, and it has no override yet.
- No Postgres, MySQL, or Redis from Chasen. The app can use a service that runs elsewhere.

## Enforcement

- **At deploy.** The image must be in the registry, the container must start, and the health path must answer in time. Otherwise the deploy fails and the old version keeps the traffic.
- **Before deploy: `chasen check`.** It pulls the image of the commit and starts it with no traffic and an empty storage. It reports each rule: the port answers, the health path returns `200` without a redirect, the storage is writable by the user of the image, no database file is outside the storage, a restart leaves the app healthy, and the app stops on `SIGTERM`. It changes nothing that is live.

## Open decisions

1. **`release:`** to run migrations before the swap. Not built. Add it when an app needs it.
2. **Backups of uploaded files.** Not built. Today only SQLite files are backed up.
