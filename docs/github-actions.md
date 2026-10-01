# Deploy from GitHub Actions

With this setup, a push to `main` deploys your app. The workflow runs the same `chasen deploy` that you run on your computer: it builds the image of the commit, pushes it to `ghcr.io`, and tells your server to pull it.

You need a server that runs `chasen-server` (or a Chasen cloud account), and an app that deploys from your computer.

## 1. Nothing to configure for the image

The repository is on GitHub, so the image goes to `ghcr.io/<owner>/<repository>`, and the workflow gives Chasen the token of the job for the registry. You need no `chasen.yml` for this.

If `chasen.yml` names another `image:` and a `registry:`, pass the password of that registry in `env:` of the deploy step, under the name that `registry.password` has.

## 2. Add the two secrets

The workflow needs to know where your server is and how to log in.

| Secret | Value |
|---|---|
| `CHASEN_URL` | `https://api.apps.example.com` for your own server (`api.` + its base domain). `https://cloud.chasenhq.com` for the cloud |
| `CHASEN_TOKEN` | The token of the server (it is in `/etc/chasen/config.yml` on the server). For the cloud: the key of your account |

```bash
gh secret set CHASEN_URL --body https://api.apps.example.com
gh secret set CHASEN_TOKEN          # asks for the value, so it stays out of your shell history
```

Secrets of the app itself (`secrets:` in `chasen.yml`) work the same way: add each one as a repository secret, and pass it in `env:` of the deploy step.

## 3. Add the workflow

Save this as `.github/workflows/deploy.yml`:

```yaml
name: Deploy
on:
  push:
    branches: [main]

permissions:
  contents: read
  packages: write        # lets the job push the image to ghcr.io

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL https://chasenhq.com/cli | sh
      - run: chasen deploy
        env:
          CHASEN_URL: ${{ secrets.CHASEN_URL }}
          CHASEN_TOKEN: ${{ secrets.CHASEN_TOKEN }}
          GHCR_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

Commit and push. The run shows the build, the push, and the output of the deploy, and its last lines are the URLs of the app.

## How the private image works

- The job gets a token from GitHub (`GITHUB_TOKEN`) that lives as long as the job. `packages: write` lets it push to the registry of the repository.
- `chasen deploy` uses the token to push the image. Then it sends the token to your server with the deploy, and the server uses it to pull the image.
- The server does not keep a login to the registry. So there is no long-lived token to store, on the server or in GitHub.
- The first push creates the package `ghcr.io/<owner>/<repository>`. It is private, and it belongs to the repository.

## Run the tests first

Make the deploy wait for another job:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: bin/test

  deploy:
    needs: test
    # ...the steps from above
```

## Deploy by hand, or roll back

Add `workflow_dispatch:` under `on:` to get a "Run workflow" button.

To go back to an older version, deploy its image again. It is still in the registry, so nothing is built:

```bash
chasen deploy --tag <the full hash of the older commit>
```

## A website with no Dockerfile

A directory with an `index.html` and no `Dockerfile` needs no image and no registry. Leave `image:` and `registry:` out of `chasen.yml`, and remove `packages: write` and `GHCR_TOKEN` from the workflow.

## When it fails

| The run says | Cause | Fix |
|---|---|---|
| `not logged in` | `CHASEN_URL` or `CHASEN_TOKEN` is empty | Set both secrets. A workflow of a fork does not get secrets |
| `the server does not accept the token` | `CHASEN_TOKEN` is not the token of this server | Copy it again from `/etc/chasen/config.yml` |
| `the registry refused the login`, or `no login for ghcr.io` | The job cannot push | Add `packages: write` under `permissions:`, and `GHCR_TOKEN: ${{ secrets.GITHUB_TOKEN }}` in `env:` of the deploy step |
| `denied` at the push | The package exists and belongs to another repository | In the settings of the package on GitHub, give this repository write access |
| `cannot pull ...` | The server cannot reach the registry, or the image is for another architecture | The build is for `linux/amd64`. For an ARM server, set `DOCKER_DEFAULT_PLATFORM: linux/arm64` in `env:` |
| The deploy fails after `Starting` | The app did not answer `/up` in time | The old version still has the traffic. Run `chasen logs`, or `chasen check` on your computer |
