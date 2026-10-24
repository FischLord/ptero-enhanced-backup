# Architecture

## Goal

Incremental, deduplicated backups for Pterodactyl, without server owners having to learn anything new and without modifying the Panel or Wings. Operators get an admin dashboard with targets, status and savings.

## Core idea: an S3 gateway with restic behind it

Pterodactyl can send backups natively to S3 storage (`APP_BACKUP_DRIVER=s3`, address freely configurable via `AWS_ENDPOINT`). PEB provides exactly that S3 endpoint. To the Panel and Wings it looks like an ordinary bucket. Internally every backup lands deduplicated in restic repositories.

```
Server owner --(native backup tab, unchanged)--> Panel
                                                   | presigned URLs (S3 SigV4)
                                                   v
Wings --tar.gz in parts--> PEB gateway --restic backup--> Primary target (e.g. REST server on a NAS)
   ^                        |  SQLite: index, metrics        |
   +--presigned GET---------+                                +--restic copy (async)--> Secondary targets (rclone, S3, SFTP ...)
                            ^
Admin --Pterodactyl extension (dashboard, restore)--+ (admin API with token)
```

## Components

### 1. Gateway (`gateway/`, Go, one binary in a Docker image)

**Runs as a Pterodactyl server.** The gateway runs as its own server with its own egg (`egg/`) on a Wings node and belongs to the instance admin. Nobody needs SSH or hand-written Docker commands.

| Topic | Implementation |
|---|---|
| Installation | Import the egg and create the server like any other (pick a node, done) |
| Operation | Console, logs, start/stop and resource limits via the Panel; Wings restarts the server after a crash |
| Network | Listens on the allocation's `SERVER_PORT`. S3 API and admin API share the port with separate paths. |
| Data | SQLite, spool and restic cache live under `/home/container`. The server's disk limit sizes the space for staging uploads. |
| NAS | NFS or local paths via Pterodactyl's built-in "mounts" |
| Cloud targets | directly over the network (REST, S3, SFTP, rclone) |
| Configuration | Egg variables (port, admin token, S3 keys); everything else via the admin API |
| Protection | `backup_limit 0`, so the gateway does not back itself up. The server belongs to the admin; server owners do not see it. |
| Updates | new image, then restart or reinstall the server |
| Node failure | Repositories live on NAS or cloud. With the restic password the gateway can be set up again on another node. |

The gateway implements the part of the S3 API that Pterodactyl uses (verified against the code of Panel 1.x and Wings):

| Call | Caller | Purpose |
|---|---|---|
| `CreateMultipartUpload` | Panel (SDK) | start the backup upload, key `<server-uuid>/<backup-uuid>.tar.gz` |
| `UploadPart` (presigned PUT) | Wings | upload parts, the response must contain an `ETag` |
| `ListParts` | Panel (SDK) | only if Wings does not report parts |
| `CompleteMultipartUpload` / `AbortMultipartUpload` | Panel (SDK) | finish or abort |
| `GetObject` (presigned GET) | Wings (restore) **and browser** (download button) | deliver the archive |
| `DeleteObject` | Panel (SDK) | backup deleted |

**Backup flow:**
1. Parts are written to disk one by one (spool), gunzipped in order and passed as a tar stream to `restic backup --stdin`. With a small `BACKUP_MAX_PART_SIZE` (e.g. 256 MB) the spool stays small.
2. While streaming, the tar archive's file list is indexed in SQLite. This later powers the file browser and single-file restore without restic having to read the whole snapshot.
3. Snapshot tags: `server:<uuid>`, `backup:<uuid>`.
4. Copying to secondary targets is planned via `restic copy`, asynchronously, so a slow target does not slow down the backup itself.

**Restore/download flow:** `restic dump` streams the tar content; the gateway wraps it in gzip (fast level) and delivers it.

**Delete flow:** `restic forget` for the snapshot. `prune` runs collectively in the maintenance window because it locks the repository exclusively.

**Maintenance:** In the daily window (`PEB_MAINTENANCE_TIME`, default 02:00 local time) `restic unlock` (stale locks only), `prune`, `check` (weekly) and the size statistics run. Because `prune` locks the repository exclusively, no new ingests or restores start in the meantime. Uploads keep going into the spool, `Complete` answers immediately, and processing waits. Running work is waited for up to 15 min (`PEB_MAINTENANCE_BUSY_WAIT`); every job has a deadline (3 h), otherwise the job is skipped for the day (`skipped: busy`). Every job is stored in SQLite with its result, as is the history of the repository size. The interface for the extension is described in `admin-api.md`.

Why a tar stream instead of an unpacked file tree: no extra disk space is needed for unpacking, and a restore is simply `dump`. Restic splits data into chunks by content (content-defined chunking). Unchanged files in the tar stream therefore produce the same chunks and are deduplicated, also across servers.

### 2. Pterodactyl extension (`extension/`, Panel 2.0 only)

The UI is built as an official extension for Pterodactyl 2.0. The 2.0 extension system extends the Panel without changing its code (pages, tabs, table columns, API endpoints, own tables and settings). The S3 backup driver is unchanged in 2.0, so the gateway works with 1.x and 2.0 Panels.

Admin area only; it does not change any Pterodactyl files or tables.

Technology (see https://docs.pterodactyl.io/v2/extensions):

| Part | Implementation |
|---|---|
| Package | `extension.json` (id, version, `requires.panel`, `ui.screens[]`), distributed as `.pteroext` (max. 50 MB) |
| Backend | Laravel service provider; routes in `routes/admin.php` -> `/api/admin/extensions/{id}`; settings via `registerSettings`, secrets with `secret()` |
| Frontend | React with Vite and Tailwind via `@pterodactyl/sdk`; admin pages as screens with `area: "admin"` |

Implemented (see `extension/README.md`): dashboard (savings, health, 30-day history with forecast, server table with warnings), backups per server, file browser with single-file download (passed through the Panel), maintenance history with "Run now", and a "Backups" tab on a server's admin page. Settings: gateway URL, admin token, price, currency, warning threshold.

Planned: target management UI, setup wizard, notifications.

**Retention is owned by the Panel.** The Free edition uses the Panel's rotation (`backup_limit`; only scheduled backups rotate). Deletion always goes through the Panel (`DeleteObject` to the gateway, then the Panel row is removed). The gateway never forgets snapshots by itself (`prune` cleans up afterwards in the maintenance window). Locked backups are never rotated away.

The extension is only a UI. All data comes through the gateway's admin API. If a Pterodactyl update breaks the extension, backups keep running anyway.

### 3. Metrics

Basis: `backups.bytes` in the Panel (or the size of the received archive) is the "without PEB" value. Restic reports `data_added` and `total_bytes_processed` per snapshot and `stats --mode raw-data` per repository. The gateway stores one row per backup with size, added data, duration, target and result.

## Known limitations

- **Only storage and targets are incremental.** Wings reads and packs the whole server every time and sends all of it to the gateway. Recommendation: set `system.backups.compression_level: none` in Wings. This saves CPU, and the gateway has less to unpack.
- **Wings stores the archive locally first**, so it temporarily needs space the size of the server.
- **The S3 setting applies to the whole Panel**, so there is one gateway for all nodes. For nodes in other data centers, every backup crosses the network once in full.
- **The browser download button** calls the gateway directly. It therefore needs an externally reachable HTTPS address, because browsers block HTTP downloads started from HTTPS pages. Use a reverse proxy in front of the gateway. **This address must not sit behind an authentication proxy (SSO)**, otherwise Wings (restore) and downloads fail at the login. It is protected through the signed URLs.
- **Wings only accepts restore downloads with `Content-Type: application/gzip` or `application/x-gzip`.**
- **Wings blocks restore downloads from private addresses.** Allowlist entries are host name, IP or CIDR, without port. The gateway must be listed in `system.backups.restore_host_allowlist` if Wings reaches it via a LAN IP.
- **The restic password is the key to all backups.** Without it the data is lost, so keep a second safe copy.
- True increments directly on the node (without a full tar) would require a dedicated agent next to Wings and are not part of the current design.
