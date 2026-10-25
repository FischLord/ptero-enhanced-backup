# PEB Gateway (S3, maintenance, admin API)

A small Go service that implements the part of the S3 API that Pterodactyl (the Panel via aws-sdk-php, and Wings) uses for the native `s3` backup driver. Internally every backup ends up deduplicated in a restic repository. Architecture and background: `../docs/architecture.md`.

## What it does

- Path-style S3 with a single bucket, key `<server-uuid>/<backup-uuid>.tar.gz`
- SigV4 verification via header and via presigned URL (key, signature, clock skew 15 min, expiry); errors are returned as S3 XML
- `CreateMultipartUpload`, `UploadPart` (presigned PUT), `ListParts`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `GetObject`/`HEAD` (presigned GET), `DeleteObject`, plus an unauthenticated `/healthz`
- Ingest: parts are written to `<data>/spool/<uploadId>/<n>` and processed strictly in order (even while later parts are still uploading): gunzip, then in parallel a tar index (SQLite) and `restic backup --stdin`. Processed parts are deleted immediately. `Complete` answers immediately; restic keeps running asynchronously.
- Restore/download: `restic dump` through a gzip writer (BestSpeed). While processing is still running, `GET` and `HEAD` wait for up to 10 min.
- Delete: the backup moves to the trash for `PEB_TRASH_RETENTION` (default 7 days); the nightly `trash-purge` job then runs `restic forget` (no `prune`, the following prune frees the space). With `PEB_TRASH_RETENTION=0` the snapshot is forgotten at once. See [Trash and deleted servers](#trash-and-deleted-servers).
- SQLite (pure Go) with uploads, parts, backups (sizes, `data_added`, `total_bytes_processed`, duration, errors), file index, maintenance jobs and repository size history
- **Maintenance window** (daily, local time from `TZ`): `restic unlock` (stale locks only), `prune`, `check` (when due), then repository statistics. While `prune` and `check` run, no new ingests or restores start; running ones are waited for (at most 1 h, otherwise the job is skipped for today as `skipped: busy`). Uploads keep going into the spool, and `Complete` still answers immediately. All jobs are recorded in SQLite.
- **Storage per server**: the statistics run also measures, per server, the space its snapshots occupy in the repository (`restic stats --mode raw-data --tag server:<uuid>`, compressed and deduplicated), one restic call per server, one after another (all servers nightly; after a series of backups only the affected servers, once, 2 minutes after the last backup). Shared data counts for each server, so the sum can exceed the repository size. Exposed as `stored_bytes` / `stored_at` in `/servers`.
- **Admin API** under `/admin/v1` (bearer token, JSON): status, savings, history, servers, backups, file browser, single-file download, maintenance. Contract: `../docs/admin-api.md`
- **ntfy notification** for failed or abandoned backups and maintenance jobs

## Configuration (environment variables, egg variables)

| Variable | Meaning | Default |
|---|---|---|
| `PEB_LISTEN` | listen address | `0.0.0.0:$SERVER_PORT`, otherwise `:9000` |
| `PEB_ACCESS_KEY`, `PEB_SECRET_KEY` | S3 credentials (required) | |
| `PEB_REGION` | must match the region in the Panel (`AWS_DEFAULT_REGION`) | `us-east-1` |
| `PEB_BUCKET` | bucket name (required) | |
| `PEB_DATA_DIR` | SQLite, spool, restic cache, restic temp files (`tmp/`) | `/home/container/data` |
| `RESTIC_REPOSITORY` | repository (created on demand) | `<data>/repo` |
| `RESTIC_PASSWORD` or `RESTIC_PASSWORD_FILE` | password (one of the two is required) | |
| `PEB_RESTIC_BIN` | restic executable | `restic` |
| `PEB_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `PEB_PUBLIC_URL` | public address used for `Location` in the Complete result (otherwise `X-Forwarded-*`, otherwise the request) | |
| `PEB_MAX_PART_BYTES` | largest allowed part (requests without `Content-Length` or above the limit are rejected) | 1 GiB |
| `PEB_MAX_SPOOL_BYTES` | spool quota; when it is full, `UploadPart` waits for free space (see [Full spool](#full-spool-backpressure)) | 8 GiB |
| `PEB_SPOOL_WAIT_TIMEOUT` | how long one `UploadPart` waits for free spool space before the upload is failed (max `110m`: Wings allows a part request 2 h in total) | `60m` (`0` = do not wait, a full spool fails the upload at once) |
| `PEB_MAX_CONCURRENT_INGESTS` | concurrent `restic backup` runs; further uploads are still accepted and wait | 2 |
| `PEB_MAX_CONCURRENT_DUMPS` | concurrent restores/downloads; on overflow `503 SlowDown` after a wait | 2 |
| `PEB_MAX_INDEX_FILES` | upper limit of the file index per backup; beyond it `index_truncated` | 500000 |
| `PEB_INDEX_HASH` | `0` switches off the SHA-256 per file in the index (saves CPU during ingest) | on |
| `PEB_INGEST_TIMEOUT` | deadline for processing after `Complete` (e.g. `6h`), then abort and `failed` | `6h` |
| `PEB_ADMIN_TOKEN` | bearer token of the admin API, at least 32 characters (otherwise startup aborts). Empty = admin API off (404). Never commit it. | |
| `PEB_METRICS_TOKEN` | optional own bearer token for `GET /metrics`, at least 32 characters. Empty = `PEB_ADMIN_TOKEN` is used; without either token `/metrics` answers 404 (see "Metrics"). | |
| `TZ` | time zone for the maintenance window and daily values (`Europe/Berlin` in the egg; time zone data is embedded in the binary) | UTC |
| `PEB_MAINTENANCE_TIME` | start of the daily maintenance window (`HH:MM`, local time) | `02:00` |
| `PEB_PRUNE_ENABLED` | `prune` in the maintenance window (`true`/`false`); still possible manually via the API | `true` |
| `PEB_CHECK_INTERVAL` | interval between `restic check` runs (duration, `0` = never) | `168h` |
| `PEB_CHECK_READ_DATA_SUBSET` | additional data to read during the check, e.g. `5%`; empty = metadata only | |
| `PEB_MAINTENANCE_TIMEOUT` | deadline per maintenance job; afterwards restic is terminated, the job counts as failed and the lock is released | `3h` |
| `PEB_MAINTENANCE_BUSY_WAIT` | how long maintenance waits for running ingests/restores before `skipped: busy` | `15m` |
| `PEB_DOWNLOAD_TIMEOUT` | deadline for one restore/download (S3 GET and single-file download), so a stuck client cannot block a restore slot forever | `2h` |
| `PEB_REPO_CAPACITY_BYTES` | optional capacity of the target for the "full in N days" forecast; only needed for non-local repositories, for local ones the measured space (`statfs`) wins | |
| `PEB_NTFY_URL` | optional: full ntfy topic URL | |
| `PEB_NTFY_TOKEN` | optional: ntfy token (bearer) | |
| `PEB_SECONDARY_REPO` | optional: second restic repository for the [offsite copy](#offsite-copy); empty = off. Must differ from `RESTIC_REPOSITORY`. | |
| `PEB_SECONDARY_PASSWORD` | password of the offsite repository | same as the main repository |
| `PEB_SECONDARY_DELAY` | quiet time after the last backup before the copy starts | `5m` |
| `PEB_SECONDARY_TIMEOUT` | deadline for one copy run (the first copy can be large) | `6h` |
| `PEB_PRUNE_SKIP_WARN` | warn (ntfy once, `maintenance.warnings` in the status) after this many prune jobs in a row were skipped as busy; `0` = never | `2` |
| `PEB_TRASH_RETENTION` | how long a backup deleted in the Panel stays in the trash (Go duration like `168h`; `0` = forget at once) | `168h` |
| `PEB_ORPHAN_RETENTION` | how long the backups of a server deleted in the Panel are kept (`0` = purged at the next maintenance window) | `720h` |
| `PEB_RECONCILE` | accept the Panel's server list at `POST /admin/v1/servers/reconcile`; set `0` if several Panels share this gateway | `true` |
| `PEB_TRASH_WARN_GB` | ntfy warning 24 h before the trash is emptied if at least this many GB of original data are affected; `0` = never | `10` |
| `PEB_SECONDARY_FORGET_GRACE` | how long a snapshot must be missing in the main repository before its offsite copy is forgotten; `0` = no grace | `48h` |
| `PEB_SECONDARY_FORGET_CONFIRM` | releases a stopped forget step, see [offsite copy](#offsite-copy); set it to the offsite repository ID, then remove it again | |
| `RCLONE_CONFIG` | path of the rclone config file for `rclone:` repositories (passed through to restic) | |

All sizes are in bytes.

### Full spool (backpressure)

Wings sends a part as one long request and, on a `5xx` answer, repeats it with the same, already partly read body, so a repeat can never succeed. The gateway therefore never answers an `UploadPart` with `5xx`:

- If the spool is full, the request is not answered yet. The gateway stops reading the body, TCP flow control slows Wings down, and the part is stored as soon as consumption by restic has freed space (up to `PEB_SPOOL_WAIT_TIMEOUT`). There is no server-side read or write timeout that would cut such a request off. A reverse proxy in front of the gateway must not either: its timeouts (for example nginx `proxy_read_timeout`) must be larger than `PEB_SPOOL_WAIT_TIMEOUT`, otherwise the proxy answers `502`/`504`, which Wings repeats with a spent body. With nginx, request buffering (`proxy_request_buffering`) delays the wait until the proxy has received the whole part.
- The part the running ingest is waiting for next is always admitted, even if that overbooks the quota by up to one part per ingest slot. Without this, running ingests could wait for a part that itself waits for space held by queued uploads.
- If no space is freed in time (or a single part is larger than the whole spool), the upload is failed: status `failed` with a clear reason, spool released, no snapshot. The body is read to its end first and the answer is `409 OperationAborted`, which Wings treats as final instead of retrying.
- Internal errors while storing a part (for example a full disk) are handled the same way. Requests that cannot be served for client reasons are `4xx` as well, including a body that is shorter than its `Content-Length` (never acknowledged) and parts of an upload that has already failed.
- Re-uploading a part number replaces the earlier version as long as restic has not started on it; the last complete version wins, and `CompleteMultipartUpload` only accepts the ETag of that version. A different re-upload of a part that was already consumed is rejected with `409`.

Keep the spool at least as large as two parts and the deadline well below the two hours Wings allows per part request.

Further `RESTIC_*`, `AWS_*` and `RCLONE_*` variables are passed through to restic itself.

Panel side: `APP_BACKUP_DRIVER=s3`, `AWS_ENDPOINT=<gateway URL>`, `AWS_USE_PATH_STYLE_ENDPOINT=true`, `AWS_DEFAULT_REGION`, `AWS_BACKUPS_BUCKET`, keys matching the above, `BACKUP_MAX_PART_SIZE` e.g. 256 MB. The gateway must be reachable under exactly the host name the Panel signs (behind a proxy, pass the `Host` header through unchanged).

## Maintenance and admin API

- `prune` runs with `--max-unused 5%`. This is gentle on the target: small leftovers stay instead of rewriting packs on every run. The "freed bytes" of a job come from the `total prune` line of the restic output (approximation, may be missing).
- All restic calls go through a shared gate: ingest, restore, `forget` (delete), statistics. During `prune`/`check` they wait. A `DeleteObject` in that time answers with `503 SlowDown` after at most 2 minutes so that Panel/Wings retry it.
- In restic 0.18, `restic check` always locks the repository exclusively (verified), so the check also pauses ingests.
- For local repositories (path without `rest:`, `sftp:` etc.) the real size of the directory is measured (on every statistics run, which also runs 2 minutes after the last backup of a series). With very many files on NFS this can take a while.
- Only one maintenance run happens at a time. A second trigger (`POST /admin/v1/maintenance/run`) gets 409.
- Error messages in the job history and in ntfy are deliberately generic ("prune failed, see gateway log"). Details are in the log.
- After a series of backups the repository size and the affected servers' storage are refreshed once, 2 minutes after the last backup (at most 10 minutes after the first one); never during maintenance (the nightly run measures everything).
- The admin API shares port and process with the S3 API (prefix `/admin/v1`). Therefore `PEB_BUCKET=admin` is rejected. Put it behind HTTPS (proxy) because the token travels in a header.
- Single-file download streams `restic dump` of the tar and reads up to the requested entry. For a file at the end of a large backup this takes as long as a restore, and it occupies a restore slot (`PEB_MAX_CONCURRENT_DUMPS`).

- Restic password safety: `GET`/`PUT /admin/v1/settings/password-ack` stores whether an admin confirmed that the password lies in a second place. `POST /admin/v1/keys` adds a second restic key with a random password (shown exactly once, never stored), `GET /admin/v1/keys` lists keys, `DELETE /admin/v1/keys/{id}` removes one (never the key in use). See `docs/admin-api.md`.

Example:

```
curl -H "Authorization: Bearer $PEB_ADMIN_TOKEN" http://gateway:9000/admin/v1/status
curl -X POST -H "Authorization: Bearer $PEB_ADMIN_TOKEN" -d '{"jobs":["prune","stats"]}' http://gateway:9000/admin/v1/maintenance/run
```

## Trash and deleted servers

**Trash.** When the Panel deletes a backup (`DeleteObject`), the gateway does not forget the snapshot. The backup gets the status `trashed` with `trashed_at` and `purge_after = now + PEB_TRASH_RETENTION`. The Panel no longer sees it (`GET` on its key is `404`), but it is still in the repository. The nightly job `trash-purge` runs first in the maintenance window, forgets the expired backups and leaves the space to the `prune` that follows. A snapshot that fails to forget stays in the trash and is tried again the next night (the job is then marked failed and a notification is sent). Until then the admin can restore a backup or delete it for good through the admin API (`GET /trash`, `POST /trash/{uuid}/restore`, `DELETE /trash/{uuid}`) or the extension's Trash tab. A restored backup is a normal gateway backup again that the admin can browse and download; it cannot be hooked back into the Panel, because the Panel deleted its own record. Trashed backups still occupy repository space, so deleting in the Panel frees space only after the retention. Set `PEB_TRASH_RETENTION=0` to forget at once. The offsite copy mirrors a deletion only after the snapshot was forgotten in the main repository plus `PEB_SECONDARY_FORGET_GRACE`.

**Deleted servers.** The Panel removes the backup rows of a deleted server by database cascade and never calls the S3 backend (Panel issue #2564), so the gateway would keep the snapshots forever. The extension therefore reports deleted servers: immediately through the Panel's server-operation event (`POST /admin/v1/servers/{uuid}/deleted`) and by comparing the Panel's server list with the gateway's hourly and when the dashboard is opened (`POST /admin/v1/servers/reconcile`). The gateway moves all backups of such a server to the trash with `purge_after = deleted_at + PEB_ORPHAN_RETENTION` (default 30 days) and sends an ntfy message. If the server UUID shows up again (a false alarm) the mark is lifted and the backups return. This comparison assumes one Panel per gateway (a second Panel's servers would be marked as deleted); turn it off with `PEB_RECONCILE=0` otherwise. Safety nets: an empty server list from the Panel, or a list that shares no UUID with the servers the gateway knows, marks nothing. Without the extension the gateway cannot learn about a deleted server; you can call the endpoint yourself, see [`../docs/admin-api.md`](../docs/admin-api.md).

## Metrics

`GET /metrics` serves Prometheus metrics (text format 0.0.4) on the same port as the S3 and admin APIs. It is never open: it needs `Authorization: Bearer <token>`. If `PEB_METRICS_TOKEN` (at least 32 characters) is set, only that token is accepted on `/metrics`, so the scraper does not need the admin token. If it is empty, `PEB_ADMIN_TOKEN` is used. With neither set the endpoint answers `404`. Therefore `PEB_BUCKET=metrics` is rejected. Put it behind HTTPS because the token travels in a header.

Metrics from the database are cached for 30 seconds; scrape every 30 s to 1 min. The repository probe in a scrape waits at most 5 s and then reports `peb_repo_reachable 0`, so the default `scrape_timeout` (10 s) is enough; do not set it lower than 6 s. Metrics that have not been measured yet (for example the repository size before the first statistics run, or the offsite copy when it is not configured) are left out instead of reported as 0, so a graph shows a gap rather than a false zero. Servers appear as `server="<uuid>"` labels. Sums such as `peb_backup_data_added_bytes_total` are taken over the database and can fall when backups are deleted, so they are typed `gauge`; use `peb_backups_last_24h` and the per-server series for rates.

Main series: `peb_backups_total{status}`, `peb_backups_last_24h{status}`, `peb_backup_last_{duration_seconds,data_added_bytes,timestamp_seconds}{server}`, `peb_repo_{size,capacity}_bytes`, `peb_repo_reachable`, `peb_server_stored_bytes{server}`, `peb_spool_{used,limit}_bytes`, `peb_ingest_{running,queued}`, `peb_dumps_active`, `peb_maintenance_last_{success_timestamp_seconds,result}{type}`, `peb_maintenance_job_running`, `peb_secondary_*`, `peb_build_info{version}`, `peb_uptime_seconds`.

Scrape configuration:

```yaml
scrape_configs:
  - job_name: peb-gateway
    scheme: https
    metrics_path: /metrics
    scrape_interval: 60s
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/peb-token   # file holds only the token
    static_configs:
      - targets: ["gateway.example.org"]
```

A ready-made Grafana dashboard is in [`contrib/grafana/`](contrib/grafana/).

## Offsite copy

With `PEB_SECONDARY_REPO` set, the gateway keeps a second restic repository in sync with the main one. Without it nothing changes.

- **Create:** if the repository does not exist, it is created with `restic init --copy-chunker-params --from-repo <main>`, so deduplication matches. An existing repository must be reachable and have the same chunker parameters; otherwise the status shows an error and nothing is copied (the gateway keeps running).
- **Copy:** `restic copy --from-repo <main>` runs `PEB_SECONDARY_DELAY` after the last finished backup (each new backup moves the deadline) and once more in the nightly window, after prune/check/stats. restic skips snapshots that are already there. Never two copy runs at the same time.
- **Gate:** a copy reads the main repository like a backup does: ingests and restores continue. Prune and check of the main repository wait for a running copy (and are skipped as `busy` if it takes longer than `PEB_MAINTENANCE_BUSY_WAIT`). The first copy of a large repository can therefore delay one night's prune.
- **Mirror deletions (nightly only):** deleting a backup in the panel is mirrored to the secondary after the grace period (`PEB_SECONDARY_FORGET_GRACE`, 48 hours by default): a copy is forgotten once its original has been missing in the main repository for that long. Then `prune` (if `PEB_PRUNE_ENABLED`) and `check` run on the secondary. Safety rules, all of which stop the step without forgetting anything (error in the status, notification, job `forget-secondary` failed):
  - Only copies made by this gateway are ever forgotten (it keeps a record of them; copies from before the record existed are taken over once if they belong to a backup the gateway knows). Other snapshots on the secondary are never touched.
  - One run forgets at most 10% of the secondary's snapshots (at least 1) and never more than 20.
  - The gateway remembers the repository IDs of both repositories. If either changed (someone swapped a repository), it does not forget.
  - It does not forget if the main snapshot list cannot be read, or is empty while the secondary holds copies.

  A stopped step stays stopped, and `secondary.forget_blocked` is `true`, until you release it: set `PEB_SECONDARY_FORGET_CONFIRM` to the offsite repository ID (the message names it; it only works for exactly that repository) and restart, or call `POST /admin/v1/offsite/forget-confirm` (valid once). The release lifts the limits and the ID check for the next run; remove the variable afterwards, the gateway logs a reminder.
- **Status:** `secondary` block in `GET /admin/v1/status` and `/summary`, jobs `copy`, `forget-secondary`, `prune-secondary`, `check-secondary` in `GET /admin/v1/maintenance`; `{"jobs":["copy"]}` starts a copy run. Failures go to ntfy (at most once per 12 hours while the error persists). Repository addresses are shown without credentials.
- **Temp space:** restic writes temporary pack files to `$TMPDIR`. Wings mounts `/tmp` in every container as a tmpfs of only 100 MB (its default `tmpfs_size`), which a copy overruns within seconds (`no space left on device`). The gateway therefore sets `TMPDIR=<PEB_DATA_DIR>/tmp` for all restic runs (created at startup, mode 0700; leftover `restic-temp-pack-*` files there are removed at startup). A `TMPDIR` you set yourself is kept. rclone, started by restic, inherits this environment. Make sure the data volume has some free space for it (a few packs, 16 MiB each by default).
- Prune and check on the secondary read through the remote; on Google Drive expect slow runs and API quotas.

### Google Drive via rclone

The image contains rclone. The repository address is `rclone:<remote>:<path>`.

1. Create your own OAuth client ID (Google Cloud Console, enable the Drive API, OAuth consent screen, client type "Desktop app"). rclone's shared client ID is heavily rate limited. Keep the OAuth app in production mode, otherwise the token expires after 7 days.
2. On a machine with a browser and rclone: `rclone config`, new remote `gdrive`, type `drive`, enter client ID and secret, scope `drive` (or `drive.file`). When asked for the token on a headless machine, run `rclone authorize "drive" "<client_id>" "<client_secret>"` on the machine with the browser and paste the printed token.
3. Copy the resulting `rclone.conf` into the gateway server's volume, e.g. `/home/container/rclone.conf`, and keep it private (it holds the token). rclone refreshes the token and writes it back, so the file must be writable by the `container` user.
4. Set `RCLONE_CONFIG=/home/container/rclone.conf`, `PEB_SECONDARY_REPO=rclone:gdrive:peb` and optionally `PEB_SECONDARY_PASSWORD`, then restart.

### Restoring from the secondary by hand

The secondary is a normal restic repository. Without the gateway:

```
export RCLONE_CONFIG=/path/to/rclone.conf
export RESTIC_REPOSITORY=rclone:gdrive:peb
export RESTIC_PASSWORD=...            # the secondary password (default: the main password)
restic snapshots --tag server:<server-uuid>
restic dump <snapshot-id> /<server-uuid>.tar > backup.tar        # the tar stream (Wings packs it as tar.gz; gzip it if Wings must restore it)
restic restore <snapshot-id> --target /tmp/restore                              # or all files
```

To make the secondary the main repository again, point `RESTIC_REPOSITORY` at it (and set `RESTIC_PASSWORD` accordingly); remove `PEB_SECONDARY_*` or create a new secondary.

## Tests

Go and restic are only needed inside the container:

```
make test      # go test -race, go vet, gofmt check (builds Dockerfile.dev first)
make fuzz      # native Go fuzzing of SigV4 verification, presigned URLs and the tar index (FUZZTIME=60s per target, FUZZMIN=5s caps input minimisation)
make build     # static binary at bin/peb-gateway
make image     # production image peb-gateway:dev (not pushed)
```

The integration tests (`internal/s3api`) simulate the Panel (aws-sdk-go-v2, presign) and Wings (plain HTTP PUTs with pgzip/gzip) and need a `restic` in `PATH`. Without restic they are skipped. The same applies to the restic tests in `internal/admin` (single-file download, real maintenance run with `prune` and `check`). Scheduling (`internal/maintenance`) runs with an injected clock; restic is a fake there.

## Image and egg

The `Dockerfile` builds a static binary and packages it with restic, rclone, tini and `ca-certificates` into `debian:trixie-slim` (user `container`, home and working directory `/home/container`). The entrypoint follows the Yolks convention: it replaces `{{VAR}}` in `$STARTUP` with the environment variables and runs the result. Start command in the egg: `/usr/local/bin/peb-gateway`. Without `STARTUP` the gateway starts directly.

## Known limitations

- If the gateway restarts during an upload, running uploads are lost (marked as failed, spool cleared). Wings has to restart the backup. The gateway sends one ntfy message per such backup at startup.
- A Docker or Wings restart during a backup aborts it. Wings does not retry, so that backup is missing (no data is lost, and a half-finished snapshot is never stored). The gateway marks the upload as failed and sends an ntfy message once the upload is dropped: at the next start, or after 6 hours without activity. Do not update the Docker packages automatically (for example `apt-mark hold docker-ce docker-ce-cli containerd.io`), or run updates outside the backup schedules.
- If restic fails after `Complete`, the Panel has already stored the backup as successful. The gateway only marks it internally as `failed`, and a later `GET` returns 404. The dashboard has to show this.
- `DeleteObject` on a backup that is still being processed waits up to 1 min, then 409. Applied to an upload that was never completed, it aborts it (204).
- One main repository and at most one offsite copy. The offsite copy does not follow retention changes made outside the gateway if the main snapshot list is empty (safety stop), and a changed chunker setup requires a new, empty secondary repository.
- **`restic forget` does not free space.** The space comes back with the next `prune` in the maintenance window (or immediately via the admin API).
- If the gateway is down at the time of the window, the window is skipped and not made up for. Only `check` catches up, because it becomes due based on the interval since its last run.
- Deleted backups disappear from history and totals because the gateway only knows the status `deleted`. Backups in the trash are not counted either, but occupy repository space until they are purged, so the savings figure is a little lower during the retention.
- The trash needs the extension (or a call to the admin API) to learn about a deleted server; without it only deletions of single backups reach the gateway.
- The restored archive is gzip-compressed anew. Its checksum therefore differs from the checksum the Panel stored at backup time.
- Newer aws-sdk-php versions may send "flexible checksum" parameters (`x-amz-checksum-*`, `x-amz-sdk-checksum-algorithm`). Panel 1.15.1 and Panel 2.0.0-beta.2 (aws-sdk-php 3.394.1) work with the S3 driver as tested. Re-check after SDK updates; the headers are only harmless if they are signed and can be ignored by the gateway.
- If the spool quota is full because many waiting uploads occupy space, even the currently running upload cannot store further parts and waits (up to `PEB_SPOOL_WAIT_TIMEOUT`, then it is failed). Choose the quota generously or raise `PEB_MAX_CONCURRENT_INGESTS`.
- No range requests, no virtual-host style, no aws-chunked upload (`STREAMING-*`), no multi-bucket.
- Part numbers must start at 1 without gaps (which is what Wings does).
- The file index is kept in memory until the backup ends (at most `PEB_MAX_INDEX_FILES` entries) and written in blocks of 5000 rows. If writing fails, the snapshot is kept and the backup is marked complete with `index_truncated`.
- Measured with a real Panel and Wings: an unchanged 4.85 GB game-server archive uploaded again added about 0.24 MB to the repository. A game update that replaced most of the game files stored about 94 % of the new archive as new data, which is expected for changed data. A large server with changing world data (about 80 GB) is not measured yet.
- File hash: while the tar stream is read for the index, the gateway computes the SHA-256 of every regular file and stores it in the index (32 bytes per file, roughly 33 bytes more in SQLite). The admin API shows it as `sha256` in the file browser and uses it for `GET /admin/v1/backups/{uuid}/diff` to compare two backups. Backups indexed before this feature have no hash; the diff then falls back to the file size. Hashing costs CPU in the ingest path (about 1 GB/s with SHA extensions, so far less than restic itself); set `PEB_INDEX_HASH=0` to turn it off. The database column is added automatically and older gateway versions ignore it.
