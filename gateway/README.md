# PEB Gateway (S3, maintenance, admin API)

A small Go service that implements the part of the S3 API that Pterodactyl (the Panel via aws-sdk-php, and Wings) uses for the native `s3` backup driver. Internally every backup ends up deduplicated in a restic repository. Architecture and background: `../docs/architecture.md`.

## What it does

- Path-style S3 with a single bucket, key `<server-uuid>/<backup-uuid>.tar.gz`
- SigV4 verification via header and via presigned URL (key, signature, clock skew 15 min, expiry); errors are returned as S3 XML
- `CreateMultipartUpload`, `UploadPart` (presigned PUT), `ListParts`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `GetObject`/`HEAD` (presigned GET), `DeleteObject`, plus an unauthenticated `/healthz`
- Ingest: parts are written to `<data>/spool/<uploadId>/<n>` and processed strictly in order (even while later parts are still uploading): gunzip, then in parallel a tar index (SQLite) and `restic backup --stdin`. Processed parts are deleted immediately. `Complete` answers immediately; restic keeps running asynchronously.
- Restore/download: `restic dump` through a gzip writer (BestSpeed). While processing is still running, `GET` and `HEAD` wait for up to 10 min.
- Delete: `restic forget` (no `prune`)
- SQLite (pure Go) with uploads, parts, backups (sizes, `data_added`, `total_bytes_processed`, duration, errors), file index, maintenance jobs and repository size history
- **Maintenance window** (daily, local time from `TZ`): `restic unlock` (stale locks only), `prune`, `check` (when due), then repository statistics. While `prune` and `check` run, no new ingests or restores start; running ones are waited for (at most 1 h, otherwise the job is skipped for today as `skipped: busy`). Uploads keep going into the spool, and `Complete` still answers immediately. All jobs are recorded in SQLite.
- **Admin API** under `/admin/v1` (bearer token, JSON): status, savings, history, servers, backups, file browser, single-file download, maintenance. Contract: `../docs/admin-api.md`
- **ntfy notification** for failed backups and maintenance jobs

## Configuration (environment variables, egg variables)

| Variable | Meaning | Default |
|---|---|---|
| `PEB_LISTEN` | listen address | `0.0.0.0:$SERVER_PORT`, otherwise `:9000` |
| `PEB_ACCESS_KEY`, `PEB_SECRET_KEY` | S3 credentials (required) | |
| `PEB_REGION` | must match the region in the Panel (`AWS_DEFAULT_REGION`) | `us-east-1` |
| `PEB_BUCKET` | bucket name (required) | |
| `PEB_DATA_DIR` | SQLite, spool, restic cache | `/home/container/data` |
| `RESTIC_REPOSITORY` | repository (created on demand) | `<data>/repo` |
| `RESTIC_PASSWORD` or `RESTIC_PASSWORD_FILE` | password (one of the two is required) | |
| `PEB_RESTIC_BIN` | restic executable | `restic` |
| `PEB_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `PEB_PUBLIC_URL` | public address used for `Location` in the Complete result (otherwise `X-Forwarded-*`, otherwise the request) | |
| `PEB_MAX_PART_BYTES` | largest allowed part (requests without `Content-Length` or above the limit are rejected) | 1 GiB |
| `PEB_MAX_SPOOL_BYTES` | spool quota; above it `503 SlowDown` (Wings retries) | 8 GiB |
| `PEB_MAX_CONCURRENT_INGESTS` | concurrent `restic backup` runs; further uploads are still accepted and wait | 2 |
| `PEB_MAX_CONCURRENT_DUMPS` | concurrent restores/downloads; on overflow `503 SlowDown` after a wait | 2 |
| `PEB_MAX_INDEX_FILES` | upper limit of the file index per backup; beyond it `index_truncated` | 500000 |
| `PEB_INGEST_TIMEOUT` | deadline for processing after `Complete` (e.g. `6h`), then abort and `failed` | `6h` |
| `PEB_ADMIN_TOKEN` | bearer token of the admin API, at least 32 characters (otherwise startup aborts). Empty = admin API off (404). Never commit it. | |
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

All sizes are in bytes.

Further `RESTIC_*`, `AWS_*` and `RCLONE_*` variables are passed through to restic itself.

Panel side: `APP_BACKUP_DRIVER=s3`, `AWS_ENDPOINT=<gateway URL>`, `AWS_USE_PATH_STYLE_ENDPOINT=true`, `AWS_DEFAULT_REGION`, `AWS_BACKUPS_BUCKET`, keys matching the above, `BACKUP_MAX_PART_SIZE` e.g. 256 MB. The gateway must be reachable under exactly the host name the Panel signs (behind a proxy, pass the `Host` header through unchanged).

## Maintenance and admin API

- `prune` runs with `--max-unused 5%`. This is gentle on the target: small leftovers stay instead of rewriting packs on every run. The "freed bytes" of a job come from the `total prune` line of the restic output (approximation, may be missing).
- All restic calls go through a shared gate: ingest, restore, `forget` (delete), statistics. During `prune`/`check` they wait. A `DeleteObject` in that time answers with `503 SlowDown` after at most 2 minutes so that Panel/Wings retry it.
- In restic 0.18, `restic check` always locks the repository exclusively (verified), so the check also pauses ingests.
- For local repositories (path without `rest:`, `sftp:` etc.) the real size of the directory is measured (on every statistics run, which happens at most once every 10 min after backups). With very many files on NFS this can take a while.
- Only one maintenance run happens at a time. A second trigger (`POST /admin/v1/maintenance/run`) gets 409.
- Error messages in the job history and in ntfy are deliberately generic ("prune failed, see gateway log"). Details are in the log.
- After every successful backup the repository size is refreshed, at most every 10 minutes and never during maintenance.
- The admin API shares port and process with the S3 API (prefix `/admin/v1`). Therefore `PEB_BUCKET=admin` is rejected. Put it behind HTTPS (proxy) because the token travels in a header.
- Single-file download streams `restic dump` of the tar and reads up to the requested entry. For a file at the end of a large backup this takes as long as a restore, and it occupies a restore slot (`PEB_MAX_CONCURRENT_DUMPS`).

Example:

```
curl -H "Authorization: Bearer $PEB_ADMIN_TOKEN" http://gateway:9000/admin/v1/status
curl -X POST -H "Authorization: Bearer $PEB_ADMIN_TOKEN" -d '{"jobs":["prune","stats"]}' http://gateway:9000/admin/v1/maintenance/run
```

## Tests

Go and restic are only needed inside the container:

```
make test      # go test -race, go vet, gofmt check (builds Dockerfile.dev first)
make build     # static binary at bin/peb-gateway
make image     # production image peb-gateway:dev (not pushed)
```

The integration tests (`internal/s3api`) simulate the Panel (aws-sdk-go-v2, presign) and Wings (plain HTTP PUTs with pgzip/gzip) and need a `restic` in `PATH`. Without restic they are skipped. The same applies to the restic tests in `internal/admin` (single-file download, real maintenance run with `prune` and `check`). Scheduling (`internal/maintenance`) runs with an injected clock; restic is a fake there.

## Image and egg

The `Dockerfile` builds a static binary and packages it with restic, rclone, tini and `ca-certificates` into `debian:trixie-slim` (user `container`, home and working directory `/home/container`). The entrypoint follows the Yolks convention: it replaces `{{VAR}}` in `$STARTUP` with the environment variables and runs the result. Start command in the egg: `/usr/local/bin/peb-gateway`. Without `STARTUP` the gateway starts directly.

## Known limitations

- If the gateway restarts during an upload, running uploads are lost (marked as failed, spool cleared). Wings has to restart the backup.
- If restic fails after `Complete`, the Panel has already stored the backup as successful. The gateway only marks it internally as `failed`, and a later `GET` returns 404. The dashboard has to show this.
- `DeleteObject` on a backup that is still being processed waits up to 1 min, then 409. Applied to an upload that was never completed, it aborts it (204).
- Only one repository, no secondary targets yet.
- **`restic forget` does not free space.** The space comes back with the next `prune` in the maintenance window (or immediately via the admin API).
- If the gateway is down at the time of the window, the window is skipped and not made up for. Only `check` catches up, because it becomes due based on the interval since its last run.
- Deleted backups disappear from history and totals because the gateway only knows the status `deleted`.
- The restored archive is gzip-compressed anew. Its checksum therefore differs from the checksum the Panel stored at backup time.
- Newer aws-sdk-php versions may send "flexible checksum" parameters (`x-amz-checksum-*`, `x-amz-sdk-checksum-algorithm`). Test this against a real Panel; the headers are only harmless if they are signed and can be ignored by the gateway.
- If the spool quota is full because many waiting uploads occupy space, even the currently running upload cannot store further parts (503 until space is free). Choose the quota generously or raise `PEB_MAX_CONCURRENT_INGESTS`.
- No range requests, no virtual-host style, no aws-chunked upload (`STREAMING-*`), no multi-bucket.
- Part numbers must start at 1 without gaps (which is what Wings does).
- The file index is kept in memory until the backup ends (at most `PEB_MAX_INDEX_FILES` entries) and written in blocks of 5000 rows. If writing fails, the snapshot is kept and the backup is marked complete with `index_truncated`.
- The ingest path has not yet been measured with real Wings/Panel instances and real game-server data (dedup ratio of tar stream vs. file tree is still to be determined).
