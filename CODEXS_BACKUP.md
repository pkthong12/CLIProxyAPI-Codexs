# Codexs Backup Runbook

## Scope

The scheduled backup protects Apipool configuration, CLIProxy auth files,
CPAMP data, TKProxy data, TLS material, and the Docker deployment files.
Application source and static builds remain recoverable from Git and are not
included.

SQLite databases are backed up through SQLite's `.backup` command. Do not copy
the live database, WAL, and SHM files directly.

## Secret Configuration

Create the directory and two root-only files on the VPS:

```bash
sudo install -d -m 700 /etc/apipool-backup
sudoedit /etc/apipool-backup/restic.env
sudoedit /etc/apipool-backup/restic-password
sudo chmod 600 /etc/apipool-backup/restic.env /etc/apipool-backup/restic-password
```

`/etc/apipool-backup/restic.env` must contain the S3-compatible B2 settings:

```bash
AWS_ACCESS_KEY_ID='YOUR_B2_KEY_ID'
AWS_SECRET_ACCESS_KEY='YOUR_B2_APPLICATION_KEY'
AWS_DEFAULT_REGION='YOUR_B2_REGION'
RESTIC_REPOSITORY='s3:https://YOUR_B2_ENDPOINT/YOUR_BUCKET/restic/apipool'
RESTIC_PASSWORD_FILE='/etc/apipool-backup/restic-password'
```

`/etc/apipool-backup/restic-password` contains only the Restic repository
password on one line. Do not store either file in Git or paste their values in
chat.

## Initialisation

After configuring the secret files, initialise the repository and run the
first backup:

```bash
sudo -i
set -a
source /etc/apipool-backup/restic.env
set +a
restic init
systemctl start apipool-restic-backup.service
restic snapshots
```

Enable the schedules only after the first backup succeeds:

```bash
sudo systemctl enable --now apipool-restic-backup.timer apipool-restic-check.timer
```

## Restore Test

Restore into a disposable directory first:

```bash
sudo -i
set -a
source /etc/apipool-backup/restic.env
set +a
mkdir -p /var/lib/apipool-restore-test
restic restore latest --target /var/lib/apipool-restore-test
sqlite3 /var/lib/apipool-restore-test/var/lib/apipool-backup/snapshot.*/cpa-manager-plus/usage.sqlite 'PRAGMA integrity_check;'
sqlite3 /var/lib/apipool-restore-test/var/lib/apipool-backup/snapshot.*/tkproxy-manager/tkproxy-manager.db 'PRAGMA integrity_check;'
```

Do not overwrite live data until the restore test completes. For a real
database restore, stop only the affected Docker service, restore its staged
SQLite snapshot and data key, then start that service again.

## Operations

Run an ad-hoc pre-deploy backup:

```bash
sudo systemctl start apipool-restic-backup.service
```

Inspect the latest backup and scheduled jobs:

```bash
sudo -i
set -a
source /etc/apipool-backup/restic.env
set +a
restic snapshots
systemctl list-timers 'apipool-restic-*'
```
