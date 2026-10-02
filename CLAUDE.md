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
- The server keeps apps in `apps.yml` (the format matcha reads). Everything else of its own is in SQLite (`/etc/chasen/server.sqlite3`): its settings (domain, token, bucket, `auto_update`, `heartbeat_url`), the settings of each app, the logins, and the activity feed. There is no config file. A server from before copies `config.yml` and `env/<app>.json` once and leaves the files for a rollback.
- A bucket is optional. A server without one is complete: backups stay on its disk. No message calls that an error.
- The deploy is imperative. No desired-state file, no reconcile loop.
- A server updates itself: `setup` installs a systemd timer that runs `chasen-server update` each night (`update.go`). It takes the newest GitHub release, checks the checksum, and goes back to the previous binary when the API does not answer. So a tag `v*` reaches every server within a day: tag only what passed the end-to-end test. `chasen-server settings auto_update off` turns it off.
- `chasen.yml` is not a docker-compose file. An unknown key is an error.
- The CLI does not update itself. Once a day, for a person at a terminal, it looks for a newer release and prints one line (`update.go`). `chasen update` installs it, with the checksum check. Never in CI, never for a `dev` build.
- The screen (`chasen` with no command) uses no TUI library: `golang.org/x/term` and escape codes. It shows the output of protocol commands and runs protocol commands, nothing else. A new thing on the screen needs its command first. It cleans every line of server output before it draws it (`clean`), so a server cannot send escape codes to the terminal. `chasen demo` (`demo.go`) is the same screen on a made-up server inside the program: use it to work on the screen with no server and no login (`mise run dev`). When a command changes its output, change the demo too.
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
