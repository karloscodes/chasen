# Chasen

Deploy the Docker image of an app with a SQLite database to one server. Two programs, one repository. Open source, Apache 2.0.

| Program | Directory | Job |
|---|---|---|
| `chasen` | `cmd/chasen` | The CLI on the developer's machine: `client.go` (options, dispatch), `login.go`, `deploy.go`, `registry.go`, `placement.go`, and the screen of `chasen` with no command: `tui.go` (state, keys, loop) and `tui_view.go` (drawing) |
| `chasen-server` | `cmd/chasen-server` | Runs on each server: API, image pulls, backups, live replica, activity feed |

Shared code:

- `protocol`: the contract between the CLI and the server. The command list, the exit marker, and the API client live here and nowhere else. The Chasen cloud (a separate, private repository) imports this package and answers the same API, so the CLI does not know which one it talks to.
- `oauth`: the OAuth 2.0 device login. The server mounts it, and the cloud does too.

Both packages are public API of the module: another repository imports them. Do not break them without a new version.

## Commands

```bash
mise run test     # go vet + unit tests, no Docker
mise run e2e      # full test with real Docker, the proxy, an S3 store, and a private registry. Needs sudo
mise run smol     # a disposable test server in a smolvm machine
```

Run `mise run e2e` after a change to the protocol, the deploy, the backups, or the login. It takes about 4 minutes. It skips itself when a `matcha-proxy` or `chasen-server` container exists, and it fails when another job replaces the proxy during the run.

## Decisions (do not reopen without the user)

- No SSH in the deploy. The CLI talks to an HTTPS API. The protocol is plain HTTP with text output, not gRPC.
- No build on the server. An app is an image in a registry: `ghcr.io/<owner>/<repository>` of the git origin on GitHub, or `image:` in `chasen.yml`; the tag is the git commit. The registry login comes from `registry:` in `chasen.yml`, then `GHCR_TOKEN`/`GITHUB_TOKEN` (CI), then what `docker login` saved, then `gh auth token`. `gh` is optional: most people have git and Docker, not gh. `chasen deploy` builds it where it runs (the developer's computer, or CI), pushes it, and the server pulls it, with the registry login sent like a secret; `--tag` deploys an existing image with no build. The one exception is a static website (an `index.html`, no `Dockerfile`): the server copies its files into a Caddy image.
- `chasen add server <domain>` is for a self-hosted server. `chasen login` goes to the cloud. The CLI keeps several logins: `chasen servers`, `chasen use`, and `server:` in `chasen.yml`.
- One owner for each server. No users or permissions on a server.
- The matcha engine (`github.com/karloscodes/matcha`) does the proxy, HTTPS, and the container swap. Chasen imports it.
- Litestream and SQLite are inside `chasen-server`. It must build with `CGO_ENABLED=0`: the binary runs in an Alpine container.
- The server keeps apps in `apps.yml` (the format matcha reads). Everything else of its own is in SQLite (`/etc/chasen/server.sqlite3`): its settings (domain, token, bucket, `auto_update`, `heartbeat_url`), the settings of each app, the logins, and the activity feed. There is no config file. A server from before copies `config.yml` and `env/<app>.json` once and leaves the files for a rollback. That one-time copy of `config.yml` is also an interface: a script can write the file with a domain and a token, then run `setup`. Programs outside this repository use it. Do not remove it.
- A bucket is optional. A server without one is complete: backups stay on its disk. No message calls that an error.
- The deploy is imperative. No desired-state file, no reconcile loop.
- A server updates itself: `setup` installs a systemd timer that runs `chasen-server update` each night (`update.go`). It takes the newest GitHub release, checks the checksum, and goes back to the previous binary when the API does not answer. So a tag `v*` reaches every server within a day: tag only what passed the end-to-end test. `chasen-server settings auto_update off` turns it off.
- `chasen.yml` is not a docker-compose file. An unknown key is an error.
- `chasen-server adopt <app>` takes over an app that matcha runs on the same server (`adopt.go`): the record moves from the file of matcha to `apps.yml`, as it is, and nothing restarts. It works because Chasen and matcha share the proxy, the container names, and the data directories. An adopted app is like an addon: its image is not one that Chasen made, so `restart` starts it from its record, with no env of the standard added. `--undo` gives it back.
- `chasen-server quiet-hour` prints the hour of the day with the fewest requests, from the log of the proxy (`quiet.go`). It is a local command, not a protocol command. Chasen does not write the reboot hour of a server: the operating system is the owner's.
- **This repository is public, and the cloud is not.** Say here what a customer of the cloud sees: it exists, it runs this same software, what it costs. Say nothing about how it works inside: not in code, comments, docs, this file, or a commit message. A reboot that fails no request is a feature of the cloud: no recipe for it here. When a change here is for the cloud, give the general reason ("for a server behind a proxy"), not the cloud's reason. `bin/public` checks the files and the new commit messages for names from the inside of the cloud, and CI runs it. It knows names, not ideas: the rule is yours to keep.
- `chasen run <command>` runs one command in the container of the app (`serverRun`): the words go to `docker exec` as they are, with no shell and no input, and the history keeps the run. It is not a console.
- `chasen ssh` is the console (user, 2 October 2026): a shell in the container of the app, with a terminal. It is not SSH and it does not reach the server: a WebSocket to the API (`protocol/shell.go`), and the API asks the Docker socket for the terminal (`cmd/chasen-server/shell.go`). "No SSH in the deploy" stays true. The WebSocket code is `golang.org/x/net/websocket`, a module the build had already.
- `chasen download` saves the databases of one backup as a `tar.gz` file (`download.go`). It is the answer to "it is my data", for self-hosting and for the cloud. `ssh` and `download` are `GET` requests, not commands: they are not in `protocol.Commands`, and the cloud passes them on by themselves.
- The CLI does not update itself. Once a day, for a person at a terminal, it looks for a newer release and prints one line (`update.go`). `chasen update` installs it, with the checksum check. Never in CI, never for a `dev` build.
- The screen (`chasen` with no command) is for watching and running a server, not for deploys. A deploy needs the directory of an app (its commit, its Dockerfile, its `chasen.yml`), and the screen is about the whole server. Do not add a deploy key.
- The server saves the output of a command to its history while the command runs (once a second, `saveActivity`), so `chasen history <id>` and the screen can follow a deploy that is on its way.
- The screen uses no TUI library: `golang.org/x/term` and escape codes. It shows the output of protocol commands and runs protocol commands, nothing else. A new thing on the screen needs its command first. It cleans every line of server output before it draws it (`clean`), so a server cannot send escape codes to the terminal.
- To work on the CLI and its screen, use `mise run dev` (`bin/dev`). It starts the mock server (`internal/mock`, run by `tools/mock`): a process that answers the real protocol over HTTP with made-up apps and runs no Docker. Then it runs `chasen` from source against it, in a small website directory. `mise run dev -- status` runs one command, and `mise run dev -- deploy` deploys through the real CLI to the mock. When a command of the server changes its output, change the mock too.
- Addons (`chasen enable fusionaly|formlander|lognorth`) run products from their images.
- Backups cover every volume of an app (`/var/matcha/<app>/*`).

## Rules for changes

- A new command goes in `protocol` first, then in `cmd/chasen-server/server.go`. The CLI passes every protocol command through.
- Tests use real SQLite, real Docker, a real S3 store, and a real private registry (`registry:2` with a password). No mocks.
- The standard is in `STANDARD.md` and in `cmd/chasen-server/standard.go`. `chasen check` (`check.go`) tests an app against it. A rule that changes must change in the document, the code, and the check.
- The documents in `docs/` and `STANDARD.md` are also the docs of chasenhq.com: the site copies them. Write them for a reader who has no access to this repository.
- The server pulls an image under its real name and keeps it under the local name `chasen.invalid/<app>:<version>`. It tells matcha not to pull (`SkipPull`). One workaround waits for a change in matcha: domains are joined with a comma in one field.
- The settings of an app travel in the body of `deploy`, `check`, and `restart`: one line of JSON, then the files of a website. There is no `env` command.
- The README is the pitch and a table of contents. The manual is `docs/`. Do not put manual text back into the README.
- In examples, the base domain of a server is `example.com` (not `apps.example.com`), and a second server is `example.org`.

## Releases

A tag `v*` runs `.github/workflows/release.yml`: it builds the CLI (macOS and Linux) and the server (Linux), and attaches them to a GitHub release with `checksums.txt`. The install scripts `chasenhq.com/cli` and `chasenhq.com/server` download the newest release.
