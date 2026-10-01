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

For a monitor that alerts when backups stop, add `heartbeat_url` under `backup:` in `/etc/chasen/config.yml`.

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

## A new server

When a server is gone, these steps bring everything back:

1. Set up the new server with the same domain and the same bucket (`setup`, then `bucket`).
2. Point the wildcard DNS record at the new server.
3. Run `chasen add server <domain>` again: the new server has a new token.
4. Run `chasen deploy` for each app, and `chasen enable` for each addon. When the server has no data for the app and the bucket has a copy, the deploy restores it first: the live replica, or the newest snapshot if the replica fails.
5. Add the custom domains again (`chasen domains add`). They were on the old server.

An app name owns its data. A new app that reuses the name of an old app gets the data of the old app.
