# Backups and restore

Chasen keeps two kinds of copy of every SQLite database of your apps. You set nothing up for them.

| | When | Kept | Where |
|---|---|---|---|
| **Snapshots** | Every hour, and before each deploy | 6 months: every snapshot of the last day, like the one before each deploy, then one for each of the last 7 days, 4 weeks, and 6 months | On the server, and in your bucket |
| **Live replica** | Every change, about one second later | The newest state | In your bucket |

The snapshots are your history: go back to how the data was yesterday. The live replica is for the day the server dies: you lose about one second of data.

Every snapshot is checked before it counts. A snapshot that fails the check before a deploy stops the deploy.

## Add a bucket

Without a bucket, the snapshots stay on the server, and a dead server takes them with it. Any S3-compatible store works: S3, Cloudflare R2, Backblaze B2, Hetzner.

```bash
chasen bucket --endpoint https://fsn1.your-objectstorage.com --region fsn1 \
  --name chasen-backups --access-key-id <id>
# Secret access key:
```

The server creates the bucket when it does not exist, and writes a test file to it. It saves the settings only when that works. Then the snapshots go to the bucket too, and the live replica starts.

## Restore

```bash
chasen backups                    # list the snapshots and the live replica
chasen restore                    # the newest snapshot
chasen restore 20261001T120000Z   # one snapshot
chasen restore live               # the newest state, from the live replica
```

A restore checks the copy first, stops the app, puts the copy in place, and starts the app. The databases it replaces go to `/var/matcha/<app>/pre-restore-<time>/` on the server. It deletes nothing.

## Know that the copies work

```bash
chasen verify
# Checking the copies of shop. Nothing changes.
#   ok    the live replica restores storage/db.sqlite3 (112.4 MB), and it passes the integrity check
#   ok    the snapshot 20261002T190000Z restores storage/db.sqlite3 (112.4 MB), and it passes the integrity check
# The copies restore.
```

`chasen verify` restores the copies next to the real databases, checks them, and removes them. The app keeps running. Run it after you add a bucket, and now and then after that.

Between two runs, the server watches by itself. When a snapshot is more than two hours old, or the live replica misses changes for ten minutes, `chasen alerts` and the screen say so. For a monitor of your own, give the server a URL to call after each hour that worked: `chasen-server settings heartbeat_url <url>`. When the calls stop, your monitor tells you.

## Take your data with you

```bash
chasen download                    # the newest snapshot
chasen download 20261001T120000Z   # one snapshot
```

You get one file, `shop-20261001T120000Z.tar.gz`, with each database of the app, ready to open:

```bash
tar -xzf shop-20261001T120000Z.tar.gz
sqlite3 storage/db.sqlite3
```

The data is yours. Look at it on your computer, keep a copy of your own, or take it to another server. Run `chasen backup` first when you want the state of this moment.

## When the server is gone

1. Get a new server, and give it the same bucket: `chasen deploy --server root@<its address> --domain <domain of the app>` sets it up, then `chasen bucket` with the same values as before.
2. Point the DNS records of your apps at the new server.
3. Run `chasen deploy` for each app. The new server has no data for the app and the bucket has a copy, so the deploy restores it first: the live replica, or the newest snapshot.
4. Add the custom domains again with `chasen domains add`, and run `chasen enable` for each addon.

Keep the secret key of each app in its secrets file (`chasen secrets edit` does this for a new app). Without it, the new server makes a new key: people log in again, and data that the app encrypted with the old key stays unreadable.

## How big the bucket gets

Neither copy grows forever.

- **The snapshots** take about 41 times the compressed size of the databases of an app, at most.
- **The live replica** holds one full copy of each database, and the changes of the last one to two days. Its size follows how much the app writes in a day, not how long it has run.

`chasen bucket` shows what the bucket holds for each app, and when the replica copied its last change.

Two things make a replica larger than you expect:

- **An app that writes the same rows over and over**, one commit for each row. A database of 40 MB that writes 4 GB of changes in a day has a replica of several GB. Write in transactions.
- **An app that you removed.** Chasen leaves its files in the bucket. Delete its folder there by hand.

## An app with no backups

Some data is not worth a copy, like a demo that makes its data again at each start. Say so in its `chasen.yml`, and deploy:

```yaml
backup: false
```

The app then gets no snapshots and no live replica, and a new server does not restore it. `chasen status` shows `Backup: off`. `chasen backup` still makes a snapshot when you ask.

An app name owns its data: a new app with the name of an old one gets the data of the old one.
