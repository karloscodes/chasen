# The server protocol

`chasen` talks to `chasen-server` over HTTP. The cloud answers the same protocol and passes each request on to the server of the app, so a client does not know which one it talks to.

The requests reach the API of a server in one of two ways, and they are the same in both:

- **On the web:** `https://api.<base domain>`, through the proxy of the server.
- **Through SSH:** the client runs `ssh <server> chasen-server connect`. That command joins its input and its output to the API, so the client writes the same HTTP requests into `ssh` and reads the answers from it. Nothing else runs on the server. The login is `chasen-server login`, which makes a token for the client and prints it: who can run a command on the server as root owns it.

The whole contract is in one Go file: [`protocol/protocol.go`](../protocol/protocol.go). The three programs import it. This document describes version 1 of it.

## Why it is plain HTTP

What crosses the wire is a command and its output. HTTP does that natively: a request body in, a stream of text out. So there is no schema compiler, no generated code, and you can call the API with `curl`. It also passes the proxy of the server and Cloudflare with no extra setting.

## A command

```
POST /v1/<command>?arg=<first>&arg=<second>
Authorization: Bearer <token>
```

- **The command** is one word from the list below.
- **The arguments** are `arg` query parameters, in order. The first one is the app, for every command except `list` and `load`.
- **The request body** is the input of the command. Most commands have none.
- **The response** is `text/plain`. It is the output of the command, sent line by line as the command runs.
- **The last line** is a zero byte, then `chasen-exit `, then the exit code of the command: `\x00chasen-exit 0`. A response that ends without this line means the connection broke.

```bash
curl -N -X POST "https://api.example.com/v1/status?arg=shop" \
  -H "Authorization: Bearer $CHASEN_TOKEN"
```

## Status codes

| Code | Meaning |
|---|---|
| `200` | The command started. Its result is the exit code in the last line, not the status code |
| `401` | The token is not valid. The client starts a new login |
| `404`, `409`, `502`, `503` | The cloud only: no such server, a server choice is needed, or the server does not answer. The body is one line of text |

A command that fails still answers `200`: the server already sent the output when it knows the result.

## The commands

| Command | Arguments | Body | What it does |
|---|---|---|---|
| `list` | | | The apps of the server |
| `load` | | | The load, the memory, and the disk of the server |
| `run` | app, then the words of the command | | Runs the command in the container of the app. The exit code is the one of the command |
| `deploy` | `<app> <version>` | The settings. For a website: then its files | Pull the image of the settings (or wrap the files of a website), back up, start, and swap |
| `check` | `<app> <version>` | Like `deploy` | Run the image next to the live app and test it against the standard. It keeps nothing |
| `enable` | `<addon> [domain]` | | Run an addon from its image |
| `restart` | `<app>` | The settings, or nothing | Start the app again from the image it has, with these settings |
| `status` | `<app>` | | The version, the state, the URLs, and the backups |
| `logs` | `<app>` | | Follow the logs. It stops when the client goes away |
| `history` | `<app> [id]` | | The activity feed, or the output of one entry |
| `domains` | `<app> [add\|rm <domain>]` | | List or change the domains |
| `backup` | `<app>` | | Make a snapshot now |
| `backups` | `<app>` | | List the backups |
| `restore` | `<app> [backup\|live]` | | Restore a backup |
| `remove` | `<app>` | | Stop the app. The data stays |
| `logout` | | | Make the API forget the token of the request |

Every command except `logs` runs to its end on the server, also when the client goes away. So a lost connection never leaves a deploy half done.

## A command about the server

One command is about the server and not about an app: `bucket`. With no arguments it prints where the backups go. With `--endpoint`, `--name`, `--access-key-id`, and `--region` as arguments, it sets the bucket; the secret access key is the first line of the request body. It is in the list `ServerCommands`, apart from the commands of the apps. A server answers it. A service that runs servers for its users, like the cloud, does not pass it on: there the bucket is not the user's to change.

## Two requests that are not commands

A shell and a file do not fit "text out, exit code last". They are `GET` requests with the same token.

| Request | What it is |
|---|---|
| `GET /v1/ssh?arg=<app>&cols=80&rows=24` | A WebSocket: a shell in the container of the app. A binary message carries the bytes of the terminal, in both directions. A text message is `resize <cols> <rows>` from the client, or `exit <code>` or `error <message>` from the server, which is then the last message |
| `GET /v1/download?arg=<app>&arg=<backup>` | The databases of one backup, as a `tar.gz` file. Without a backup: the newest one. The `Content-Disposition` header has the name of the file: `<app>-<backup>.tar.gz` |

The shell is a WebSocket because a WebSocket passes every proxy that can be in front of a server. A status that is not `101` means no shell: an older server, or a token it refuses.

## The body of a deploy

`deploy`, `check`, and `restart` take the settings of the app in the request body: one line of JSON.

```json
{"image": "ghcr.io/you/shop:3f9a2c1d5e8b7a6094c3f2e1d0b9a8c7d6e5f4a3", "registry": {"username": "you", "password": "..."}, "env": {"LOG_LEVEL": "info", "STRIPE_KEY": "..."}, "port": 3000, "health": "/up", "health_timeout": 30, "volumes": ["/app/storage"]}
```

- Only `env` is always there. A zero or missing value means "use the default of the standard".
- The server uses `registry` for one pull and does not keep it.
- A website has no `image`. Its files follow the line of the settings, as a `tar.gz` archive. The server puts them in a Caddy image; it is the only image a server makes.
- `restart` with an empty body keeps the settings that the server has.

```bash
POST /v1/deploy?arg=shop&arg=3f9a2c1
```

The second argument is the version that the app shows (`APP_VERSION`).

## The login

The login is the OAuth 2.0 device flow (RFC 8628), in [`oauth`](../oauth/oauth.go).

| Request | What it does |
|---|---|
| `POST /oauth/device_authorization` | Start a login. The answer has a device code, a user code, and the page to open |
| `GET /oauth/device?user_code=...` | The page. The person types the token of the server (or the key of a cloud account) |
| `POST /oauth/token` | The client asks until the login is approved, and gets a token of its own |

In CI there is no login: the token of the server (or the key of the account) is the bearer token.

## What only the cloud answers

| Request | What it does |
|---|---|
| `GET /v1/placement?app=<app>` | Which servers the account has, and what a new one costs. JSON |
| Header `Chasen-Server: <id>` | Send the command to this server of the account |
| Header `Chasen-Server: new`, `new:cx33`, `new:@ash`, `new:cx33@ash` | Create a server for the app: the cheapest one, or of this type, or in this location |

A plain server answers `404` to `/v1/placement`, and the client then asks no question.

## Limits

- A command is one word from the list. The server refuses every other word, and the client sends the word as one path segment.
- The login page accepts 20 wrong keys or codes in a minute, for all visitors together, and 1000 logins can wait at one time. After that it answers `429`.
- The API reads the headers of a request for 10 seconds at most. It has no limit on the time of a response: a deploy streams for minutes.
- The client sends a token over plain `http://` only to this computer or to a private network address.

## Compatibility

- The path says `/v1`. A change that breaks a client gets a new number.
- A new command is a new word in the list. An old server answers it with an error line and exit code 1.
- A new field in the settings is ignored by an old server. So a new client with an old server can lose a setting without an error: keep the server as new as the client.
- Version 0.2 moved the settings into the body of `deploy`, `check`, and `restart`, and removed the `env` command. A 0.1 client does not work with a 0.2 server.
- `GET /up` answers `200` when the API runs. It needs no token.
