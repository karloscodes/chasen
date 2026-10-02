# Backups and restore

A single server with SQLite needs backups that you can trust. Chasen has two layers.

**Snapshots.** Each hour, and before each deploy, Chasen makes a snapshot of every SQLite database in the storage of the app:

- `VACUUM INTO` gives a consistent copy while the app writes.
- `PRAGMA integrity_check` must pass, or the backup fails. A failed backup before a deploy stops the deploy.
- Chasen compresses the snapshot and copies it to the S3 bucket.
- Retention keeps the newest snapshot of each of the last 24 hours, 7 days, 4 weeks, and 6 months. The same rule runs on the server and in the bucket.

**Live replica.** Litestream streams every change to the S3 bucket, about one second behind. This is the copy that loses almost nothing when the server dies.

Set the bucket on the server. Any S3-compatible store works: S3, Cloudflare R2, Backblaze B2, Hetzner.

```bash
chasen-server bucket --endpoint https://fsn1.your-objectstorage.com --region fsn1 \
  --name chasen-backups --access-key-id <id>
# Secret access key: ...
chasen-server bucket          # show the bucket
```

The command creates the bucket when it does not exist, then writes and deletes a test object. It saves the settings only when all of that works. Set `S3_SECRET_ACCESS_KEY` to skip the question.

For a monitor that alerts when backups stop, run `chasen-server settings heartbeat_url <url>` on the server. The server calls the URL after each hourly backup that worked.

Without a bucket, the snapshots stay on the server and there is no live replica. `setup` warns you about this.

Chasen calls `heartbeat_url` each hour, only when every snapshot worked and the live replica runs. Point a monitor at it, so that you know when backups stop.

## Restore

```bash
chasen backups                    # list the snapshots and the live replica
chasen restore                    # the newest snapshot
chasen restore 20261001T120000Z   # one snapshot
chasen restore live               # the newest state in the live replica
```

A restore checks the backup first, stops the app, swaps the databases, and starts the app. It moves the previous databases to `/var/matcha/<app>/pre-restore-<time>/`. It deletes nothing.

## Take a backup with you

```bash
chasen backup                      # first, when you want the state of this moment
chasen download                    # the newest backup
chasen download 20261001T120000Z   # one backup
```

`chasen download` saves one file in the current directory: `shop-20261001T120000Z.tar.gz`. It holds each SQLite database of the backup, ready to open, with its path in the app: `storage/db.sqlite3`.

```bash
tar -xzf shop-20261001T120000Z.tar.gz
sqlite3 storage/db.sqlite3
```

The data is yours. This is how you look at it on your computer, keep a copy of your own, or move to another server. It does not replace a file that exists.

## A new server

When a server is gone, these steps bring everything back:

1. Set up the new server with the same domain and the same bucket (`setup`, then `bucket`).
2. Point the wildcard DNS record at the new server.
3. Run `chasen add server <domain>` again: the new server has a new token.
4. Run `chasen deploy` for each app, and `chasen enable` for each addon. When the server has no data for the app and the bucket has a copy, the deploy restores it first: the live replica, or the newest snapshot if the replica fails.
5. Add the custom domains again (`chasen domains add`). They were on the old server.

The new server makes a new secret key for each app, unless the deploy brings one: an app with `SECRET_KEY_BASE` in its `secrets:` gets its old key back. Without it, people log in again, and data that the app encrypted with the old key stays unreadable. See [the standard](../STANDARD.md#4-environment).

An app name owns its data. A new app that reuses the name of an old app gets the data of the old app.
