# Secrets

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

## Secrets from another tool

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

## Change a secret, or remove one

```bash
chasen secrets edit       # change the line, or delete it
git commit -am "Rotate the Stripe key"
chasen restart            # the app gets the secrets of now, with no build
```

`chasen restart` sends the secrets of now with the settings of the app, from the same image. The next `chasen deploy` does the same. A secret that is not in the file any more is not in the container any more after that.

## On another computer, and in CI

The file is in git, so a clone has it. The key is not: give it to the other computer yourself, through your password manager, and put it in `chasen.key`, or set `CHASEN_KEY` in the environment.

In GitHub Actions, add the content of `chasen.key` as the repository secret `CHASEN_KEY`, and pass it in `env:` of the deploy step. [Deploy from GitHub Actions](github-actions.md) has the whole workflow.

## When the key is lost

Nobody can read `chasen.secrets.enc` without its key: not you, not Chasen. Your app keeps running, because the server has the values of the last deploy. Make a new file:

```bash
git rm chasen.secrets.enc
chasen secrets edit       # makes a new chasen.key, and opens an empty file
```

Type the values again: take each one from the service that issued it. Then commit the new file and run `chasen restart`.

The secret key of the app (`SECRET_KEY_BASE`) is not lost: the server keeps the one the app has when a deploy brings none. To keep it in the file again, copy it from the app: `chasen run printenv SECRET_KEY_BASE`.

## When the key leaks

Someone with the key can read every version of `chasen.secrets.enc` in your git history. A new key does not change that. So change the values themselves:

1. At each service, make a new API key or password, and turn the old one off.
2. Make a new file with a new key: remove `chasen.key`, run `git rm chasen.secrets.enc`, then `chasen secrets edit`. Put the new values in it.
3. Commit it, run `chasen restart`, and give the new key to the computers and the CI that need it.

## Where the secrets are

| Where | What |
|---|---|
| Your repository | `chasen.secrets.enc`, encrypted with AES-256-GCM |
| Your computer, your password manager, CI | the key: `chasen.key`, or `CHASEN_KEY` |
| The server | the values of the last deploy, in `/etc/chasen/server.sqlite3`, which only root can read |
| The container | environment variables |

The server is never the only copy: there is no command that stores a secret on the server alone. So a new server needs one `chasen deploy` to get every secret back.
