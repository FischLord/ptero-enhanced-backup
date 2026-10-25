# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and each component is versioned on its own with [Semantic Versioning](https://semver.org/). Gateway releases are tagged `gateway-vX.Y.Z`.

## Unreleased

## Gateway 0.2.0 and Extension Free 1.2.0 - 2026-10-25

### Gateway 0.2.0

#### Added
- `PEB_SPOOL_WAIT_TIMEOUT`: how long `UploadPart` waits for free spool space before the upload is failed (default 60 min, at most 110 min).
- Storage used per server: the statistics run now measures, for each server, the space its snapshots occupy in the repository (`restic stats --mode raw-data`, limited to the snapshots tagged `server:<uuid>`; compressed and deduplicated). The nightly run and a manual `stats` run measure all servers, one after the other; after a series of backups only the affected servers are re-measured, once, shortly after the last backup, and at startup only servers without a value; a failed measurement is logged and keeps the previous value.
- `GET /admin/v1/servers` returns `stored_bytes` and `stored_at` for each server (`null` until measured). Fields were only added; existing clients keep working.
- SQLite table `server_stats`, created automatically at startup. Older gateway versions ignore it.
- Offsite copy (one secondary repository): `PEB_SECONDARY_REPO`, `PEB_SECONDARY_PASSWORD`, `PEB_SECONDARY_DELAY`, `PEB_SECONDARY_TIMEOUT`, `RCLONE_CONFIG`. The repository is created with `restic init --copy-chunker-params`; `restic copy` runs a few minutes after the last backup (debounced) and in the nightly window. The nightly run also forgets snapshots that were deleted in the main repository (never if the main snapshot list is unreadable or empty), prunes and checks the secondary. Copy runs do not block ingests or restores; prune and check of the main repository wait for them. Google Drive works through rclone (see the gateway README).
- Admin API: `secondary` block in `/status` and `/summary`, job types `copy`, `forget-secondary`, `prune-secondary`, `check-secondary` in `/maintenance`, and `copy` in `POST /maintenance/run`. Fields were only added. New table `offsite_state`, created automatically.
- Egg variables for the offsite copy (egg and the copy in the extension updated).
- Fuzz tests for SigV4 verification (header and presigned URLs, canonicalisation) and tar index parsing (`go test -fuzz=FuzzVerifyHeader ./internal/s3auth`, `-fuzz=FuzzIndexTar ./internal/ingest`). The first runs found no crashes.
- Prometheus endpoint `GET /metrics` (text format 0.0.4, prefix `peb_`): backups by status, last backup per server (duration, data added, time), repository size and capacity, storage per server, spool, ingest and restore load, maintenance (last success, last result, running, bytes freed), offsite copy (backlog, lag, last copy, error, size), build info, uptime. Bearer token: `PEB_METRICS_TOKEN` if set, else `PEB_ADMIN_TOKEN`; without either token the endpoint is off. Values not measured yet are left out. `PEB_BUCKET=metrics` is now rejected.
- Grafana dashboard and Prometheus scrape example in `gateway/contrib/grafana/`.
- Egg variable `PEB_METRICS_TOKEN`.
- File hash in the index: SHA-256 of every regular file, computed while the tar stream is read for the index and stored in a new nullable column `files.sha256` (added automatically at startup; older gateway versions ignore it, older backups keep `NULL`). `PEB_INDEX_HASH=0` switches it off.
- Admin API: the `files` listing has a `sha256` field per entry (`null` if unknown), and the new `GET /admin/v1/backups/{uuid}/diff?against={uuid2}` compares the files of two backups of one server (`added`, `removed`, `changed`, `unchanged_count`, paged). Fields were only added.
- Restic password safety: `GET`/`PUT /admin/v1/settings/password-ack` stores, per gateway, whether an admin confirmed that the password is kept in a second place (new table `settings`). `POST /admin/v1/keys` adds a second restic key with a random password (`restic key add`, temporary 0600 file); the password is returned exactly once and never stored, only key ID, label and time are (new table `emergency_keys`). `GET /admin/v1/keys` lists the keys, `DELETE /admin/v1/keys/{id}` removes one (never the key in use). Tests include a real `key add`/`list`/`remove` run against a temporary repository. Both tables are created automatically.
- Trash: a delete from the Panel (`DeleteObject`) no longer forgets the snapshot at once. The backup moves to status `trashed` and stays recoverable for `PEB_TRASH_RETENTION` (default `168h`, 7 days; `0` = forget at once as before). The new nightly job `trash-purge` (before the prune, so the space is freed in the same window) forgets the expired ones. The Panel does not see trashed backups (`GET` answers 404). New columns `trashed_at`, `purge_after`, `trash_reason` in `backups`, added automatically.
- Admin API: `GET /trash`, `POST /trash/{uuid}/restore` (the backup becomes a normal gateway backup again, browsable and downloadable by the admin; it does not reappear in the Panel), `DELETE /trash/{uuid}` (purge now), `trash` block in `/summary`, job type `trash-purge` (also accepted by `POST /maintenance/run`). Trashed backups are not counted in the totals but in the repository usage.
- Backups of servers deleted in the Panel (Panel issue #2564: the Panel removes its backup rows but never tells the S3 backend): `POST /servers/{uuid}/deleted` and `POST /servers/reconcile` mark a server as deleted and move its backups to the trash for `PEB_ORPHAN_RETENTION` (default `720h`, 30 days). A server that shows up again lifts the mark and its backups return. An empty server list, or one without any known server, changes nothing. New table `deleted_servers`.
- ntfy messages when backups of a deleted server move to the trash, and 24 hours before the trash is emptied if at least `PEB_TRASH_WARN_GB` (default 10) GB of original data are affected.
- Egg variables `PEB_TRASH_RETENTION`, `PEB_ORPHAN_RETENTION`, `PEB_TRASH_WARN_GB`.
- ntfy message, once per backup, when an upload is abandoned: backups still pending at startup (Wings or Docker restart, gateway restart) and uploads dropped after 6 hours without activity. The message names the server and backup and says that Wings does not retry, so the backup is missing.

#### Changed
- `UploadPart` waits for free spool space (backpressure) instead of failing, for up to `PEB_SPOOL_WAIT_TIMEOUT` (default `60m`, max `110m`). When the wait runs out, or a single part is larger than `PEB_MAX_SPOOL_BYTES`, the upload is failed with a clear reason and the request is answered `409 OperationAborted` after its body was read. Requests on an already failed upload are answered `409` instead of `500`.
- Tests: Wings-style client with retries, small spool and slow ingest (`make test-load` for a multi-GB run).
- Tests: `make fuzz` caps input minimisation (`FUZZMIN`, default `5s`) so a run no longer stalls at 0 execs/s once new coverage is found.
- Documentation: the gateway README now reflects the measured production state instead of prototype wording.
- Offsite mirror-forget safety: `PEB_SECONDARY_FORGET_GRACE` (default `48h`), `PEB_SECONDARY_FORGET_CONFIRM`, `POST /admin/v1/offsite/forget-confirm`, `secondary.forget_blocked` in `/status` and `/summary`. New tables `offsite_copies` and keys in `offsite_state` (repository IDs), created automatically.
- Warning when the prune of the main repository is skipped as busy several times in a row (`PEB_PRUNE_SKIP_WARN`, default 2): one ntfy message (again only after a prune has run), `maintenance.prune_skipped_consecutive` and `maintenance.warnings` in `GET /admin/v1/status`.
- Deleting a backup is mirrored to the offsite copy only after a grace period (48 hours by default), and only for copies the gateway made itself. One nightly run forgets at most 10% of the offsite snapshots and never more than 20; if more would go, or a repository ID changed, nothing is forgotten, the status shows an error and a notification is sent until you release it (see the gateway README).

#### Fixed
- restic ran out of temp space under Wings (`/tmp` is a 100 MB tmpfs): the offsite copy failed within seconds with `no space left on device`. The gateway now sets `TMPDIR=<PEB_DATA_DIR>/tmp` for every restic run (unless `TMPDIR` is set) and removes leftover `restic-temp-pack-*` files there at startup.
- Storage per server and repository size went stale after a series of backups: only the first backup triggered a measurement and later ones inside the 10-minute window were dropped. The refresh is now debounced and runs once, 2 minutes after the last backup (at most 10 minutes after the first), and waits instead of being skipped while maintenance runs.
- Deleting an emergency key failed with "repository is already locked" while a backup or statistics run was active. `restic key add` and `key remove` now wait up to 2 minutes for the repository lock and, if it is still held (left behind by a killed restic), run `restic unlock` (stale locks only) and retry once; `key list` no longer takes a lock; the key operation timeout is 5 minutes.
- `UploadPart`: the full body drain before an error status now happens only after the signature was verified; unauthenticated requests get a drain limited to 1 MiB and the connection is closed.
- `UploadPart`: a failed upload lookup is answered `409` without failing the whole upload; only errors from writing the part itself abort it.
- Offsite: the approval of a stopped forget step is no longer used up by a run that forgets nothing; it stays valid until a run really forgets snapshots. The per-run forget cap now counts only snapshots whose grace period is over.
- Offsite: copies that arrived in a copy run that failed afterwards are still tracked, and untracked copies of known backups are picked up later.
- Admin API: `POST /maintenance/run` with `copy` starts the maintenance jobs first; if the offsite copy is busy afterwards the response is 202 with `copy_skipped`.
- Trash purge can no longer delete a backup that was restored meanwhile: the nightly `trash-purge` and `DELETE /trash/{uuid}` first claim each backup atomically (status `purging`, only from `trashed` and only if due), a failed forget puts it back, a restart resets `purging`. `trash-purge` also removes stale restic locks first, like the prune.
- Server reconcile: a marked server that gets complete backups again has them trashed again with a fresh retention (backups restored by hand stay). New `PEB_RECONCILE` (default on) switches the Panel server-list comparison off; it assumes one Panel per gateway.
- `/metrics` no longer blocks on a hanging repository: the reachability probe waits at most 5 s and then reports `peb_repo_reachable 0`.
- Grafana dashboard: the daily data-added panel uses `delta()` and is labelled as an approximation (the sum drops when backups are deleted).
- `PEB_METRICS_TOKEN` must differ from `PEB_ADMIN_TOKEN` and `PEB_SECRET_KEY`.
- Large backups no longer fail when the spool is full. Previously `UploadPart` answered `503 SlowDown`; Wings repeats such a request with the same, partly read body, which cannot succeed, so the backup was lost after the retry period. The gateway now never answers `UploadPart` with `5xx`.
- Parts are only acknowledged if exactly `Content-Length` bytes arrived. Errors while storing a part fail the upload cleanly (`409`, status `failed`) instead of answering `500` on a half-read body.
- A full spool could stall running uploads until `PEB_SPOOL_WAIT_TIMEOUT` ran out: with more concurrent uploads than ingest slots, queued uploads filled the spool while the uploads holding the slots waited for their next part. That part is now always admitted (quota overbooked by at most one part per slot). `PEB_SPOOL_WAIT_TIMEOUT=0` disables waiting.
- Part bodies are now read to the end before every error answer on `UploadPart` (including authentication and unsupported requests), so Wings does not see a connection reset.
- Offsite copy: a backup that finished while the debounce timer was starting a copy could miss the next copy; the timer now takes its deadline atomically and puts it back if a run is active. No copy run can start after shutdown has begun.

#### Security
- Secret scrubbing also covers quoted values (`KEY="..."`), `client_secret=`-style names and non-string log attributes; the known repository passwords are removed from all log output.
- Credentials are removed from error texts before they are logged, stored or sent: user and password in URLs (`scheme://***@host`), `AWS_SECRET_ACCESS_KEY=`, `RESTIC_PASSWORD=` and similar variables, `password=`/`token=` query parameters and bearer tokens. This covers the log, job messages, the offsite error state and ntfy messages.
- The offsite forget step can no longer wipe the copy after a wrong repository, a swapped repository ID or a mass deletion in the main repository.

### Extension Free 1.2.0

#### Added
- Restic password safety on the dashboard: a warning banner until an admin confirms the password is stored in a second place (kept in the gateway, then only a quiet note), a printable emergency sheet opened in a new window (repository and offsite addresses, gateway address, restore commands without the gateway, server list with UUIDs, an empty box for the password written by hand; never contains the password), and an "Emergency key" card to create a second restic key (password shown once with a copy button), list keys and delete other keys. Panel routes `/password-ack`, `/emergency-sheet`, `/keys`. English and German texts. Needs Gateway 0.2.0.
- "Trash" tab (Admin -> Backups): backups deleted in the Panel and backups of deleted servers, with server, backup, size, deletion date and the date they are removed for good; "Restore" and "Delete now" (with a confirmation). A badge with the count shows on the tab when the trash is not empty. Panel routes `GET /trash`, `POST /trash/{uuid}/restore`, `DELETE /trash/{uuid}`. Needs Gateway 0.2.0.
- Deleted servers: the extension reports a server deletion to the gateway right away (server operation event of the Panel), reconciles the Panel's server list with the gateway hourly (Panel scheduler) and when the dashboard is opened (at most every 5 minutes), so the backups of a deleted server go to the gateway trash instead of staying behind unnoticed. An empty server list is never sent.
- The maintenance list names the new `trash-purge` job.
- Setup wizard (Admin -> Setup): pre-check, egg import, gateway server with generated secrets, copy-ready `.env` and Wings snippets, and a self-test.
- Server table: new column "Stored" with the space the server's backups occupy on the backup target (compressed, deduplicated), shown as "–" until the gateway has measured it. Needs Gateway 0.2.0.
- Offsite card: "Copy now" button that starts a copy run (the panel route for maintenance jobs now accepts `copy`; English and German texts).
- Offsite card: yellow warning line when the gateway reports that prune keeps being skipped, and when the offsite forget step is stopped by a safety check. Needs Gateway 0.2.0.
- Dashboard: "Offsite copy" card next to the storage target (status, last copy, snapshots not yet copied, used space, next copy, error text) with a warning when the last copy failed or snapshots are missing for more than 24 hours; a short note with a link to the docs if no offsite copy is configured. The maintenance list names the new offsite jobs. Needs Gateway 0.2.0.

## Gateway 0.1.0 and Extension Free 1.1.0 - 2026-10-24

First public release.

### Gateway 0.1.0

- Incremental, deduplicated storage with restic, also across servers, behind the native S3 backup driver of Pterodactyl (create, restore, download and delete work as before).
- Runs as an ordinary Pterodactyl server from its own egg and Docker image (`ghcr.io/fischlord/peb-gateway`).
- Daily maintenance window: `prune`, `check`, repository statistics, with a history of every job in SQLite.
- Admin API under `/admin/v1`: status, savings, history, servers, backups, file browser, single-file download, maintenance.
- Notifications for failed backups and maintenance (ntfy).
- Retention through the Panel's own backup rotation (`backup_limit`).

### Extension Free 1.1.0

- Admin dashboard for Pterodactyl Panel 2.0: savings (GB, %, factor, cost), storage target usage and forecast, 30-day history, per-server status with warnings.
- File browser per backup with single-file download, without restoring the whole server.
