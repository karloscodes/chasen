# Contributing to Chasen

Thank you for your help. Chasen is small on purpose, and these rules keep it so.

## Report a problem

Run `chasen report`. It opens a new issue with your version and your system filled in. Add the command that you ran, what you saw, and what you expected. For a failed deploy, add the output of `chasen history <id>`.

Do not put tokens, keys, or secrets in an issue. `chasen report` adds none.

## Open an issue before a pull request

For a bug fix with a clear cause, a pull request is welcome at once. For a feature or a change of behavior, open an issue first and wait for a yes. Chasen leaves many things out on purpose, so a feature that is not agreed first may be closed.

## Keep a pull request small

One pull request does one thing. A fix and a refactor are two pull requests. When a change is large, split it into steps that each work and each pass the tests. A small pull request gets a review sooner.

## Run all the tests

```bash
mise run test     # go vet and the unit tests, no Docker
mise run e2e      # the end-to-end test: real Docker, the proxy, an S3 store, and a registry. Needs sudo, about 4 minutes
```

Both must pass. CI runs both again on your pull request. A change of behavior comes with a test of that behavior: real SQLite, real Docker, and no mocks.

## Show that it works

Add evidence to the pull request, so a reviewer can check the change without running it:

- **A change of the screen** (`chasen` with no command): a screenshot or a short video, before and after.
- **A change of a command:** its output in the terminal, before and after.
- **A change of the server:** the output of the end-to-end test, or of the commands on a test server. `mise run smol` makes a test server in a small VM.

## Follow the house rules

- **No new dependency** without a reason in the pull request. The standard library comes first.
- **The docs change with the code.** The files in `docs/` and `STANDARD.md` are the manual on chasenhq.com. Write them in short sentences, for a reader who has no access to this repository.
- **A rule of the standard changes in three places:** `STANDARD.md`, `protocol/standard.go`, and the check in `cmd/chasen-server/check.go`.
- **`bin/public` must pass.** CI runs it on every commit.

## License

By contributing, you agree that your contribution is under the [Apache 2.0](LICENSE) license of the project.
