# Backups and restore

A single server with SQLite needs backups that you can trust. Chasen has two layers.

**Snapshots.** Each hour, and before each deploy, Chasen makes a snapshot of every SQLite database in the storage of the app:

- `VACUUM INTO` gives a consistent copy while the app writes.
- `PRAGMA integrity_check` must pass, or the backup fails. A failed backup before a deploy stops the deploy.
- Chasen compresses the snapshot and copies it to the S3 bucket.
- Retention keeps the newest snapshot of each of the last 24 hours, 7 days, 4 weeks, and 6 months. The same rule runs on the server and in the bucket.

**Live replica.** Litestream streams every change to the S3 bucket, about one second behind. This is the copy that loses almost nothing when the server dies.

Set the bucket from your computer. Any S3-compatible store works: S3, Cloudflare R2, Backblaze B2, Hetzner.

```bash
chasen bucket --endpoint https://fsn1.your-objectstorage.com --region fsn1 \
  --name chasen-backups --access-key-id <id>
# Secret access key:
chasen bucket          # show the bucket
```

The server creates the bucket when it does not exist, then writes and deletes a test object. It saves the settings only when all of that works, and then the live replica starts. The secret access key is not an argument: you type it, or it comes from `S3_SECRET_ACCESS_KEY`. The same command exists on the server, as `chasen-server bucket`.

For a monitor that alerts when backups stop, run `chasen-server settings heartbeat_url <url>` on the server. The server calls the URL after each hourly backup that worked.

## What the bucket holds, and how it grows

```bash
chasen bucket
# Bucket chasen-backups at https://fsn1.your-objectstorage.com (region fsn1)
#
# APP   SNAPSHOTS             LIVE REPLICA  TOTAL
# blog  2.1 MB in 4 backups   3.0 MB        5.1 MB
# shop  40.0 MB in 2 backups  90.0 MB       130.0 MB
# Total: 135.1 MB in 310 objects
#
# The live replica is files of changes. What it holds now:
#   changes, as they happen    24 files   290 KB   kept 5 minutes
#   merged each 30 seconds     250 files  12.0 MB
#   merged each 5 minutes      25 files   6.0 MB
#   merged each hour           2 files    30.0 MB
#   full copies, one each day  1 file     45.0 MB  kept 1 day
# Its oldest file is 3 hours old. ...
```

Both layers have a limit. Neither one grows forever.

**The snapshots** are full copies, compressed. Retention keeps at most 41 for each app: 24 hours, 7 days, 4 weeks, 6 months. So the snapshots of an app take about 41 times the compressed size of its databases, and no more.

**The live replica** is not a copy that is written again and again. Litestream writes the pages that changed:

1. Each change of the database becomes a small file, about one each second while the app writes. Those files stay 5 minutes.
2. Every 30 seconds the small files are merged into one. Every 5 minutes those are merged, and every hour again. A page that changed 100 times in an hour is in the hourly file one time.
3. Once a day Litestream writes a full copy of the database.
4. After each full copy, it deletes the full copies that are older than one day, and every file of changes from before the oldest full copy that it keeps.

So the replica holds one or two full copies and the changes of about two days. Its size follows how much of the database changes in a day, not how long the app has run: after two days it stops growing. `chasen bucket` shows the age of the oldest file, so you can see the cleanup work.

Three things make a replica larger than you expect:

- **An app that writes the same pages over and over.** A database of 40 MB that writes 4 GB of changes in a day has a replica of several GB. Fix the app: write in transactions, not one commit for each row.
- **A restart of the server** (an update, a restore) adds one full copy. It goes away with the next daily cleanup.
- **Files of an app that is gone.** Chasen does not delete the files of an app that you removed, or that you set to `backup: false`. Delete its folder in the bucket by hand.

**An app with no backups.** Some data is not worth a copy: a demo that makes its data again at each start. Say so in the `chasen.yml` of that app, and deploy it:

```yaml
backup: false
```

Chasen then makes no snapshot of the app, keeps no live replica of it, and does not restore it from the bucket on a new server. `chasen status` shows `Backup: off`. The backups it made before stay until their retention ends. `chasen backup` still makes one when you ask for it.

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

1. Add the new server: `chasen add server root@<its address>`. With a base domain, run `chasen-server setup --domain <domain>` on it too.
2. Give it the same bucket: `chasen bucket`, with the same values as before.
3. Point the DNS records of your apps at the new server.
4. Run `chasen deploy --domain <domain of the app>` for each app, and `chasen enable` for each addon. When the server has no data for the app and the bucket has a copy, the deploy restores it first: the live replica, or the newest snapshot if the replica fails.
5. Add the custom domains again (`chasen domains add`). They were on the old server.

The new server makes a new secret key for each app, unless the deploy brings one: an app with `SECRET_KEY_BASE` in its `secrets:` gets its old key back. Without it, people log in again, and data that the app encrypted with the old key stays unreadable. See [the standard](../STANDARD.md#4-environment).

An app name owns its data. A new app that reuses the name of an old app gets the data of the old app.
