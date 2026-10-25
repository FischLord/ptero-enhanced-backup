# Gateway admin API

Contract between the gateway and the Panel extension. All paths are under `/admin/v1` on the same port as the S3 API. Responses are JSON (UTF-8), field names in English.

## Basics

- **Authentication:** `Authorization: Bearer <PEB_ADMIN_TOKEN>`. The token is independent of the S3 keys. If `PEB_ADMIN_TOKEN` is not set, the gateway answers `/admin/v1/*` with `404` (API off). A wrong or missing token yields `401` (also for unknown paths).
- **No secrets:** The API never returns keys, passwords or credentials from repository addresses.
- **Names:** The gateway only knows UUIDs from the S3 key (`<server-uuid>/<backup-uuid>.tar.gz`). The extension maps them to names via the Panel.
- **Times:** RFC 3339 in UTC (`2026-10-24T12:00:00Z`). Missing times are `null`. Day boundaries in the history follow the gateway's `TZ`.
- **Sizes:** always bytes (integers). Ratios and percentages are floating-point numbers.
- **Errors:** `{"error": "<code>", "message": "<text>"}` with a matching HTTP status. Codes: `unauthorized` (401), `bad_request` (400), `not_found` (404), `not_complete` (409), `not_a_file` (400), `busy` (409 or 503), `internal` (500).
- **Backup error texts** are deliberately coarse: `ingest failed`, `timeout`, `aborted`, `gateway restarted`. Details are only in the gateway log.
- **Lists** return `{"items": [...]}`; paged lists additionally return `total`, `limit`, `offset`.
- Responses carry `Cache-Control: no-store`.

### Terms

| Term | Meaning |
|---|---|
| `logical_bytes` | Sum of the uncompressed tar sizes. This is what Pterodactyl would store natively. |
| `received_bytes` | Sum of the received (gzip-compressed) archive sizes |
| `data_added` | Data newly stored in the repository according to restic (before compression in the repository) |
| `referenced_bytes` | Data referenced by snapshots, last value of `restic stats --mode raw-data` (excluding unreferenced garbage before the next `prune`) |
| `stored_bytes` | Repository usage. Local repository: real size of the directory on disk. Otherwise equal to `referenced_bytes`. |
| `stored_bytes_exact` | `true` if `stored_bytes` was measured on disk; `false` if it is only the referenced value (REST, SFTP, S3, rclone ...) |
| `saved_bytes` | `logical_bytes - stored_bytes` |
| `upload_saved_bytes` | `logical_bytes - data_added`: the amount that did not have to be stored anew |

Backups with status `deleted` are not counted in any total. Neither are `trashed` ones (see `/trash`); they only count toward the repository usage.

---

## GET /admin/v1/status

Operational state of the gateway.

```json
{
  "version": "dev",
  "uptime_seconds": 86412,
  "repo": {
    "type": "rest",
    "location": "https://nas.example:8000/peb",
    "reachable": true,
    "checked_at": "2026-10-24T12:00:00Z"
  },
  "ingest": { "running": 1, "queued": 2 },
  "dumps": { "active": 0 },
  "spool": { "used_bytes": 268435456, "limit_bytes": 8589934592 },
  "maintenance": {
    "prune_enabled": true,
    "next_window": "2026-10-25T00:00:00Z",
    "last_prune": {
      "id": 12, "type": "prune", "trigger": "schedule",
      "started_at": "2026-10-24T00:00:05Z", "finished_at": "2026-10-24T00:03:41Z",
      "result": "ok", "message": "prune finished, 1572864 bytes freed", "bytes_freed": 1572864
    },
    "last_check": null,
    "running_job": null,
    "prune_skipped_consecutive": 0,
    "warnings": []
  }
}
```

- `maintenance.prune_skipped_consecutive`: how many prune jobs in a row ended as `skipped: busy` (long backups, restores or an offsite copy held the repository), `0` after any prune that ran. `maintenance.warnings`: short English texts for things that need attention, empty if fine; currently `prune was skipped as busy N times in a row` once `N` reaches `PEB_PRUNE_SKIP_WARN` (default `2`, `0` = never warn). The same condition sends one notification, and again only after a prune has run in between.
- `secondary`: offsite copy, see below.
- `repo.type`: `local`, `rest`, `sftp`, `s3`, `rclone`, `b2`, `azure`, `gs`, `swift`. `location` is shown without user and password.
- `repo.reachable` is re-checked at most once per minute (`checked_at`).
- `ingest.running`: ingests currently using restic. `queued`: waiting for a free slot or for a maintenance run to end.
- `last_prune` / `last_check`: last finished job of that type (including `skipped`), otherwise `null`. `running_job`: the current maintenance job or `null`.

### The `secondary` block

Present in `/status` and `/summary`. Without `PEB_SECONDARY_REPO` it is `{"configured": false, "state": "off", ...}` with `null` values.

```json
"secondary": {
  "configured": true,
  "repo": "rclone:gdrive:peb",
  "type": "rclone",
  "state": "ok",
  "running": false,
  "last_copy_at": "2026-10-24T11:50:00Z",
  "last_copy_result": "ok",
  "last_error": "",
  "forget_blocked": false,
  "lag_snapshots": 0,
  "lag_since": null,
  "stored_bytes": 25769803776,
  "stored_at": "2026-10-24T00:10:00Z",
  "next_copy_at": "2026-10-25T00:00:00Z"
}
```

- `repo`: address without credentials or tokens (user info is removed).
- `state`: `off`, `pending` (no copy yet), `copying`, `ok`, `error`. `error` when the last copy failed or the last error is not cleared (setup, chunker mismatch, refused forget).
- `last_copy_at` / `last_copy_result` (`ok`, `failed`): last finished copy job; `null` before the first one. `last_error`: short, scrubbed text, empty if fine.
- `forget_blocked`: `true` while the nightly forget step is stopped by a safety check (more snapshots to forget than allowed, a changed repository ID, an unreadable or empty main snapshot list). Nothing is forgotten meanwhile; `last_error` says why and stays until a forget run passes (also across copy runs). Release it with `POST /admin/v1/offsite/forget-confirm` or `PEB_SECONDARY_FORGET_CONFIRM`.
- `lag_snapshots`: main snapshots without a copy (`null` until known, exact after each copy, +1 per backup in between). `lag_since`: since when the lag is above 0 (`null` if none).
- `stored_bytes` / `stored_at`: raw-data size of the secondary, measured in the nightly run (`null` until then).
- `next_copy_at`: end of the pending debounce, otherwise the next nightly window.

## GET /admin/v1/summary

Metrics for the dashboard. Based on all completed, non-deleted backups.

```json
{
  "backups_count": 148,
  "logical_bytes": 412316860416,
  "received_bytes": 398000000000,
  "data_added_total": 41231686041,
  "upload_saved_bytes": 371085174375,
  "stored_bytes": 25769803776,
  "referenced_bytes": 24696061952,
  "stored_bytes_exact": true,
  "stored_at": "2026-10-24T11:50:00Z",
  "saved_bytes": 386547056640,
  "saved_percent": 93.75,
  "factor": 16.0,
  "success_rate_30d": 0.9863,
  "failures_30d": 2,
  "last_backup_at": "2026-10-24T11:41:07Z",
  "target": {
    "total_bytes": 4000787030016,
    "free_bytes": 3100000000000,
    "used_bytes": 900787030016,
    "peb_bytes": 25769803776,
    "other_bytes": 875017226240,
    "exact": true,
    "days_until_full": 2531.4,
    "avg_data_added": 268435456,
    "backups_remaining": 11548
  }
}
```

- `target`: space on the backup target (this is where these values live; `/history` uses the same source for the forecast). For local repositories (e.g. an NFS mount) measured with `statfs` on every statistics refresh: `total_bytes` size of the file system, `free_bytes` space available to the process (`f_bavail`), `used_bytes = total - free`, `peb_bytes` = repository size (`stored_bytes`), `other_bytes` = remaining used space (never negative), `exact: true`. For other repositories `PEB_REPO_CAPACITY_BYTES` counts as the total size, `free = capacity - stored_bytes`, `exact: false`. Without either (or before the first statistics run) `target` is `null`. The measured values are also stored in the table `repo_stats` (columns `target_total_bytes`, `target_free_bytes`, added at startup for existing databases).
- `days_until_full`: forecast from the growth of the repository size (30 days) against the free space, `null` without growth or measurements.
- `avg_data_added`: average of `data_added` of the completed backups of the last 30 days; `backups_remaining = free_bytes / avg_data_added` (rounded down), `null` without backups or if the average is 0.

- `stored_bytes`, `referenced_bytes`, `stored_bytes_exact`, `stored_at`, `saved_bytes`, `saved_percent`, `factor` are `null` until a repository statistic exists (or `saved_percent` when `logical_bytes = 0`, `factor` when `stored_bytes = 0`).
- `trash`: `{"count": 2, "logical_bytes": 6442450944, "next_purge_at": "2026-10-31T10:00:00Z"}`, the trash in numbers (`next_purge_at` is `null` when it is empty). Gateway 0.2.0 and later.
- `success_rate_30d`: share of successful backups among the finished backups of the last 30 days (aborted ones do not count), `null` without backups.
- `failures_30d`: failed backups in that period.
- Note: The gateway only learns about a Panel backup when the upload arrives. Errors that occur earlier in the Panel or in Wings are not visible to it.

## GET /admin/v1/servers

One row per server UUID, sorted by UUID. Considers completed and failed backups (not aborted ones).

```json
{
  "items": [
    {
      "server_uuid": "5b1f8c0e-1d7a-4c0e-9a55-0c3f5d2a9b11",
      "backup_count": 12,
      "last_success_at": "2026-10-24T03:00:41Z",
      "last_status": "complete",
      "last_error": "",
      "last_finished_at": "2026-10-24T03:00:41Z",
      "logical_bytes": 51539607552,
      "data_added_total": 3221225472,
      "avg_duration_ms": 18250,
      "last_duration_ms": 17100,
      "last_backup_bytes": 17179869184,
      "last_data_added": 52428800,
      "stored_bytes": 2147483648,
      "stored_at": "2026-10-24T03:05:12Z"
    },
    {
      "server_uuid": "9c4e7a12-77b0-4f43-8d0a-6a1b2c3d4e5f",
      "backup_count": 0,
      "last_success_at": null,
      "last_status": "failed",
      "last_error": "timeout",
      "last_finished_at": "2026-10-24T09:00:00Z",
      "logical_bytes": 0,
      "data_added_total": 0,
      "avg_duration_ms": 0,
      "last_duration_ms": 21600000,
      "last_backup_bytes": 0,
      "last_data_added": 0,
      "stored_bytes": null,
      "stored_at": null
    }
  ]
}
```

`stored_bytes` is the space the server's snapshots occupy in the repository (`restic stats --mode raw-data`, limited to the snapshots tagged `server:<uuid>`; compressed and deduplicated). Data shared with other servers counts in full for each of them, so the sum over all servers can exceed the repository size. `stored_at` is the time of the measurement. Both are `null` until the first measurement. The values are refreshed together with the repository statistics (nightly and manual `stats` run for all servers; after a series of backups only for the affected servers, once, 2 minutes after the last backup (at most 10 minutes after the first); at startup only for servers without a value); if a measurement fails, the previous value stays.

`last_status` is `complete` or `failed` (status of the most recent finished backup). `backup_count`, `logical_bytes`, `data_added_total` and `avg_duration_ms` refer to completed, non-deleted backups. `last_backup_bytes` (uncompressed size) and `last_data_added` (really newly stored after deduplication) belong to the server's most recent completed backup, 0 without a completed backup. `logical_bytes` and `data_added_total` remain as totals.

## GET /admin/v1/backups

Parameters (all optional): `server` (server UUID), `status` (`complete`, `failed`, `pending`, `deleted`; without it everything except `deleted` and trashed ones, which are listed by `/trash`), `limit` (default 50, maximum 500), `offset`. Newest first.

```json
{
  "total": 148,
  "limit": 50,
  "offset": 0,
  "items": [
    {
      "backup_uuid": "0d7e6f55-3a1b-4c2d-8e9f-a0b1c2d3e4f5",
      "server_uuid": "5b1f8c0e-1d7a-4c0e-9a55-0c3f5d2a9b11",
      "status": "complete",
      "error": "",
      "snapshot_id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6",
      "received_bytes": 4294967296,
      "logical_bytes": 5368709120,
      "data_added": 73400320,
      "total_bytes_processed": 5368709120,
      "file_count": 8151,
      "index_truncated": false,
      "started_at": "2026-10-24T03:00:23Z",
      "finished_at": "2026-10-24T03:00:41Z",
      "duration_ms": 18250
    }
  ]
}
```

`status`: `pending` (still running), `complete`, `failed`, `deleted`. `error` is only filled for `failed`. `index_truncated`: the file index is incomplete (limit `PEB_MAX_INDEX_FILES` or write error); the file browser and single-file download then only show part of the backup. Invalid parameters yield `400`.

## GET /admin/v1/backups/{backup_uuid}/files

Directory view from the file index. Parameters: `path` (directory, empty = root; leading and trailing `/` as well as `./` are ignored, `..` yields `400`), `limit` (default 200, maximum 1000), `offset`. Directories come first, then files, each sorted by name. Directories that only occur in deeper paths are derived (their `mode` and `mtime` are then `null`).

```
GET /admin/v1/backups/0d7e6f55-.../files?path=world
```

```json
{
  "backup_uuid": "0d7e6f55-3a1b-4c2d-8e9f-a0b1c2d3e4f5",
  "path": "world",
  "index_truncated": false,
  "total": 3,
  "limit": 200,
  "offset": 0,
  "items": [
    { "name": "region", "path": "world/region", "type": "dir", "size": 0, "mode": "0755", "mtime": "2026-10-23T20:11:02Z", "sha256": null },
    { "name": "data", "path": "world/data", "type": "dir", "size": 0, "mode": null, "mtime": null, "sha256": null },
    { "name": "level.dat", "path": "world/level.dat", "type": "file", "size": 3145728, "mode": "0644", "mtime": "2026-10-24T02:59:58Z", "sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" }
  ]
}
```

- `type`: `dir`, `file`, `symlink`, `other`. `size` is always 0 for directories.
- `mode`: permissions as an octal number in a string.
- `sha256`: SHA-256 of the file content as 64 hex characters, `null` for directories, symlinks, backups made before the hash was introduced, entries beyond `PEB_MAX_INDEX_FILES`, and when the gateway runs with `PEB_INDEX_HASH=0`.
- `404` for an unknown backup UUID, `409` (`not_complete`) if the backup is still running or failed.

## GET /admin/v1/backups/{backup_uuid}/diff

Compares the regular files of two complete backups of the same server by path. `{backup_uuid}` is the newer side, the required parameter `against` the baseline backup. Optional `limit` (default 200, maximum 1000) and `offset` apply to each of the three lists separately; the counts always cover the whole comparison. Lists are sorted by path.

```
GET /admin/v1/backups/0d7e6f55-.../diff?against=9a8b7c6d-...
```

```json
{
  "backup_uuid": "0d7e6f55-...", "against": "9a8b7c6d-...",
  "index_truncated": false, "limit": 200, "offset": 0,
  "added":   [ { "path": "world/new.dat", "size": 9, "sha256": "ba78..." } ],
  "removed": [ { "path": "old.log", "size": 5, "sha256": "e3b0..." } ],
  "changed": [ { "path": "world/level.dat", "size": 12, "sha256": "2d71...", "old_size": 12, "old_sha256": "ab12..." } ],
  "added_count": 1, "removed_count": 1, "changed_count": 1, "unchanged_count": 4120
}
```

- A path that occurs several times in a tar counts once, with the size and hash of its last member.
- The comparison reads both indexes once and is limited to 10 seconds (it holds the gateway's only database connection); on expiry the answer is `503` with code `diff_timeout`. `limit` is at most 1000 per list.
- `added`: in `{backup_uuid}` but not in `against`; `removed`: the other way round.
- `changed`: the hashes of both sides are known and differ. If one side has no hash (older backup, `PEB_INDEX_HASH=0`), the size decides.
- `index_truncated` is `true` if either index is incomplete; the result then only covers the indexed files.
- `400` if `against` is missing or belongs to another server (`different_server`), `404` for an unknown UUID, `409` (`not_complete`) if either backup is not complete.

## GET /admin/v1/backups/{backup_uuid}/file

Downloads a single file. Parameter: `path` (required).

```
GET /admin/v1/backups/0d7e6f55-.../file?path=world/level.dat
```

Response `200` with the file content (`Content-Type: application/octet-stream`, `Content-Length`, `Content-Disposition: attachment; filename=level.dat`).

- If the path is not in the index or the index is incomplete (`index_truncated`), the gateway searches the file in the tar instead of answering `404` right away. Hard links return the content of their target. If a name occurs several times in the tar, the first entry wins (unlike extraction, where the last one wins).
- The gateway streams `restic dump` of the tar and reads up to the entry. This takes as long as a restore up to that position and occupies a restore slot.
- A download is limited to `PEB_DOWNLOAD_TIMEOUT` (2 h). `404` if the path is missing in the archive. `400` (`not_a_file`) for directories and symlinks. `503` with `Retry-After` if all restore slots are busy or a maintenance run (prune/check) is active and does not end in time.
- If the data stream breaks midway, the gateway drops the connection, so a response is never silently truncated.

## GET /admin/v1/trash

Backups that were deleted in the Panel (`DeleteObject`) or belong to a server the Panel deleted. They stay in the repository until `purge_after`; the nightly `trash-purge` job then forgets them (see `PEB_TRASH_RETENTION`, `PEB_ORPHAN_RETENTION`). The Panel no longer knows them: `GET` on their S3 key answers `404`. Soonest purge first.

```json
{
  "total": 2,
  "logical_bytes": 6442450944,
  "items": [
    {
      "backup_uuid": "0d7e6f55-3a1b-4c2d-8e9f-a0b1c2d3e4f5",
      "server_uuid": "5b1f8c0e-1d7a-4c0e-9a55-0c3f5d2a9b11",
      "reason": "panel_delete",
      "logical_bytes": 5368709120,
      "received_bytes": 4294967296,
      "finished_at": "2026-10-20T03:00:41Z",
      "trashed_at": "2026-10-24T10:00:00Z",
      "purge_after": "2026-10-31T10:00:00Z"
    }
  ]
}
```

- `reason`: `panel_delete` (a delete in the Panel) or `server_deleted` (the Panel deleted the whole server, see below).
- `logical_bytes`: uncompressed size of the backup, `received_bytes`: size as uploaded. Trashed backups still occupy space in the repository (they count toward `stored_bytes`, not toward `backups_count`, `logical_bytes` or the savings in `/summary`).

## POST /admin/v1/trash/{backup_uuid}/restore

Takes a backup out of the trash. Response `200`: `{"backup_uuid": "...", "server_uuid": "...", "status": "complete"}`. `404` if the backup is not in the trash.

The backup is a normal gateway backup again: it shows up in `/backups` and `/servers`, and `/backups/{uuid}/files` and `/file` work, so an admin can browse and download it through the extension. **It cannot be hooked back into the Panel.** The Panel deleted its own record, so the backup does not appear in the server's Backups tab and the Panel cannot restore it. (Its S3 key answers `GET` again, but the Panel never asks for it.) A server that was deleted in the Panel and then recreated gets a new UUID.

A server that is marked as deleted stays marked when you restore single backups of it; the restored backups stay available (the periodic reconcile only trashes backups of a marked server again if they finished after the mark, never ones you restored). To bring back all backups of a server at once, list its UUID in `/servers/reconcile` (see below), which lifts the mark.

## DELETE /admin/v1/trash/{backup_uuid}

Removes a trashed backup for good: `restic forget` right away (space is freed by the next `prune`). Response `200`: `{"backup_uuid": "...", "purged": true}`. `404` if the backup is not in the trash, `409` if it was restored or purged by someone else in the meantime. While the snapshot is being forgotten the backup has the internal status `purging` and can no longer be restored; if the forget fails it returns to the trash. `503` (`busy`, `Retry-After`) if maintenance holds the repository for longer than two minutes.

## POST /admin/v1/servers/{server_uuid}/deleted

Tells the gateway that the Panel deleted the server. The gateway remembers the server as deleted (first mark wins, table `deleted_servers`) and moves all its complete backups into the trash with `reason: "server_deleted"` and `purge_after = deleted_at + PEB_ORPHAN_RETENTION` (default 30 days; `0` = at the next maintenance window). It sends one notification. Idempotent.

```json
{ "server_uuid": "5b1f8c0e-...", "newly_marked": true, "trashed": 12, "deleted_at": "2026-10-24T10:00:00Z", "purge_after": "2026-11-23T10:00:00Z" }
```

`400` if the UUID is malformed.

## POST /admin/v1/servers/reconcile

Compares the Panel's server list with the gateway's. Body: `{"server_uuids": ["...", "..."]}` (all servers the Panel has). Response `200`:

```json
{ "skipped": null, "marked_deleted": ["5b1f8c0e-..."], "restored": [], "trashed": 12 }
```

- A server with complete backups in the gateway that is not in the list is handled like `POST /servers/{uuid}/deleted` (`marked_deleted`, `trashed` = number of backups moved).
- A server marked as deleted that is in the list again is a false alarm: the mark is removed and the backups that were trashed because of it come back (`restored`).
- Safety nets (nothing is changed, `skipped` says why): `"empty_list"` for an empty list, `"no_overlap"` if the list shares no UUID at all with the servers the gateway knows (wrong Panel, broken query). Malformed UUIDs yield `400`.
- A server that is marked and still missing from the list gets backups that finished after the mark trashed again, with `purge_after` of now plus `PEB_ORPHAN_RETENTION` (never an expired date).
- **One Panel per gateway.** The list must contain every server of the Panel this gateway serves. If two Panels share a gateway, each one's reconcile marks the other's servers as deleted (as soon as one UUID overlaps). Set `PEB_RECONCILE=0` in that case; the endpoint then answers `"skipped": "disabled"`.
- Marking only moves backups into the trash; until `purge_after` everything can be undone by listing the server again.

## GET /admin/v1/notify

The configured notification channels, without secrets. `target` is scheme and host of a URL (`https://discord.com`), or `host:port → N recipients` for e-mail. `format` is present for webhooks (`json`, `discord`, `slack`, `gotify`). `channels` is an empty array if none is configured.

```json
{ "lang": "en",
  "channels": [
    { "type": "ntfy", "target": "https://ntfy.example.org" },
    { "type": "webhook", "target": "https://discord.com", "format": "discord" },
    { "type": "smtp", "target": "smtp.example.org:587 → 2 recipients" } ] }
```

## POST /admin/v1/notify/test

Sends a test message (event `test`, severity `info`) to every channel at once and waits for the outcome, at most about 10 seconds per channel. The status is `200` even if some channels fail; `error` is a short text without URLs or secrets and only present when `ok` is `false`.

```json
{ "results": [
    { "type": "webhook", "target": "https://discord.com", "ok": true },
    { "type": "smtp", "target": "smtp.example.org:587 → 2 recipients", "ok": false, "error": "authentication failed" } ] }
```

A second call within 10 seconds answers `429` (`rate_limited`) with a `Retry-After` header.

## GET /admin/v1/history

Daily series for charts. Parameter: `days` (default 30, 1 to 365). Always contains exactly `days` entries, oldest first, the last one is today.

```json
{
  "days": 3,
  "series": [
    { "date": "2026-10-22", "backups_ok": 24, "backups_failed": 0, "logical_bytes": 128849018880, "data_added": 2147483648, "stored_bytes": 25000000000 },
    { "date": "2026-10-23", "backups_ok": 23, "backups_failed": 1, "logical_bytes": 123480309760, "data_added": 1073741824, "stored_bytes": 25400000000 },
    { "date": "2026-10-24", "backups_ok": 12, "backups_failed": 0, "logical_bytes": 64424509440, "data_added": 536870912, "stored_bytes": 25769803776 }
  ],
  "forecast": {
    "capacity_bytes": 1000000000000,
    "free_bytes": 974230196224,
    "used_bytes": 25769803776,
    "growth_bytes_per_day": 384901888,
    "days_until_full": 2531.4,
    "full_at": "2033-09-30T09:12:44Z"
  }
}
```

- `backups_ok` / `backups_failed` count by the day the backup ended. Per day, `logical_bytes` are the uncompressed sizes and `data_added` the newly stored data of the successful backups.
- `stored_bytes`: last measurement of the day (disk size if measured, otherwise referenced). Days without a measurement take over the previous day's value; `null` only as long as there has never been a measurement.
- `forecast` appears when the free space is known: measured with `statfs` (local repository; `capacity_bytes` is then the file system size and `free_bytes` the real free space) or from `PEB_REPO_CAPACITY_BYTES` minus repository size. Without either it is missing. It is a linear regression line through the days with a real measurement. `growth_bytes_per_day` is `null` with fewer than two measured days. `days_until_full` and `full_at` are `null` if the repository does not grow, and `0` or now if no space is left. `prune` can bend the history downwards.

## GET /admin/v1/maintenance

Job history, newest first. Parameter: `limit` (default 50, maximum 500).

```json
{
  "items": [
    {
      "id": 14, "type": "stats", "trigger": "schedule",
      "started_at": "2026-10-24T00:03:41Z", "finished_at": "2026-10-24T00:03:43Z",
      "result": "ok", "message": "12 snapshots, 25769803776 bytes stored", "bytes_freed": null
    },
    {
      "id": 13, "type": "check", "trigger": "schedule",
      "started_at": "2026-10-24T00:03:41Z", "finished_at": "2026-10-24T00:03:41Z",
      "result": "skipped", "message": "skipped: busy", "bytes_freed": null
    },
    {
      "id": 12, "type": "prune", "trigger": "schedule",
      "started_at": "2026-10-24T00:00:05Z", "finished_at": "2026-10-24T00:03:41Z",
      "result": "ok", "message": "prune finished, 1572864 bytes freed", "bytes_freed": 1572864
    }
  ]
}
```

- `type`: `trash-purge` (forgets trashed backups whose time has come, before the prune), `prune`, `check`, `stats`; with an offsite copy also `copy`, `forget-secondary`, `prune-secondary`, `check-secondary`. `trigger`: `schedule`, `manual` or (copy only) `backup`. Before: `trigger`: `schedule` or `manual`.
- `result`: `queued`, `running`, `ok`, `failed`, `skipped`. `skipped: busy` means running backups or restores were still not finished after `PEB_MAINTENANCE_BUSY_WAIT` (15 min); the job was skipped for today.
- A job that exceeds `PEB_MAINTENANCE_TIMEOUT` (3 h) ends as `failed` with the message `<job> timed out after 3h0m0s`.
- `message` is short and generic (`prune failed, see gateway log`); `bytes_freed` exists only for `prune` and only if the restic output was readable.
- The daily run consists of `trash-purge`, `prune` (if enabled), `check` (if due) and `stats`. Size refreshes after backups do not appear as a job.

## POST /admin/v1/maintenance/run

Starts maintenance jobs immediately. Only one maintenance run happens at a time.

```
POST /admin/v1/maintenance/run
Content-Type: application/json

{"jobs": ["prune", "check", "stats"]}
```

`jobs` may contain `trash-purge`, `prune`, `check`, `stats` and, with a configured offsite copy, `copy` (at least one). `copy` runs on its own (the offsite copy has its own run slot, `409 busy` if a copy or the nightly offsite run is active). The order is fixed: trash-purge, prune, check, stats. A manually triggered `prune` also runs with `PEB_PRUNE_ENABLED=false`, and `check` also when not due.

Response `202`:

```json
{ "id": 15, "job_ids": [15, 16, 17] }
```

`id` is the first job, `job_ids` contains all of them (in execution order). Progress is shown by `GET /admin/v1/maintenance` (result `queued`, `running`, then the final result) and `running_job` in `/status`.

Errors: `409` (`busy`) if a run is already active. `400` for an invalid body or an unknown job name.

## POST /admin/v1/offsite/forget-confirm

Releases a stopped forget step of the offsite copy (see `forget_blocked` above). The release is bound to the current repository ID of the offsite repository, is used up by the next nightly forget run that finishes, and lifts the safety limits for that one run (the grace period and "own copies only" still apply). The same effect, permanently, has the environment variable `PEB_SECONDARY_FORGET_CONFIRM=<offsite repository ID>`; remove it after use.

Response `200`: `{"confirmed": true}`. Errors: `400` without a configured offsite copy, `500` if the repository ID cannot be read.

## GET and PUT /admin/v1/settings/password-ack

Whether an admin has confirmed that the restic password is stored in a second place. The state belongs to the gateway (not to a browser or panel user); the extension shows a warning banner until it is set. The gateway never returns the password itself.

```
GET /admin/v1/settings/password-ack
PUT /admin/v1/settings/password-ack
Content-Type: application/json

{"acknowledged": true, "by": "admin"}
```

Response `200` (both calls):

```json
{ "acknowledged": true, "acknowledged_at": "2026-10-24T15:00:00Z", "by": "admin" }
```

Before the first confirmation: `{"acknowledged": false, "acknowledged_at": null, "by": ""}`. `PUT` with `"acknowledged": false` withdraws the confirmation. `by` is a free label (at most 100 characters). `400` for an invalid body. Stored in the table `settings`.

## Keys: second restic key (emergency key)

A second restic key opens the same repository with another password, for example to hand to a hosting provider or to keep in a safe. The gateway generates the password, adds the key with `restic key add --new-password-file` (temporary file with mode 0600, removed right after), and **stores nothing of the password**. It remembers only the key ID, a label and the time (table `emergency_keys`). Secrets are removed from error texts and logs.

### GET /admin/v1/keys

```json
{
  "keys": [
    { "id": "<64 hex>", "short_id": "caa19104", "created_at": "2026-10-24T21:26:13Z", "host": "gateway", "user": "root",
      "current": true, "emergency": false, "label": "" },
    { "id": "<64 hex>", "short_id": "d201417b", "created_at": "2026-10-24T21:26:16Z", "host": "hoster", "user": "peb-emergency",
      "current": false, "emergency": true, "label": "hoster" }
  ]
}
```

`current` is the key the gateway itself uses. `emergency` is true for keys created through this API. `created_at` is the time restic prints (local time of the creating machine, converted to UTC by the gateway's time zone).

### POST /admin/v1/keys

Body (optional): `{"label": "hoster"}` (at most 64 characters, default `emergency`; also used as the key's host name). Response `201`:

```json
{ "id": "<64 hex>", "short_id": "d201417b", "label": "hoster", "host": "hoster", "user": "peb-emergency",
  "created_at": "2026-10-24T21:26:16Z", "password": "<40 characters>", "notice": "..." }
```

**`password` appears only in this response and cannot be read again.** Errors: `409` (`too_many_keys`) with ten emergency keys, `502` (`restic_failed`) if restic fails, `501` (`not_supported`) if key management is unavailable.

### DELETE /admin/v1/keys/{id}

Removes a key; `{id}` is the full ID or a unique prefix of at least 8 characters. The key in use is never removed. Response `204`. Errors: `404` unknown key, `400` (`ambiguous`) prefix matches several keys, `409` (`current_key`) for the key in use, `502` (`restic_failed`).

---

## GET /metrics

Prometheus text format (version 0.0.4), outside the `/admin/v1` prefix on the same port. Not part of the JSON contract above, but it uses the same data.

- **Authentication:** `Authorization: Bearer <token>`. If `PEB_METRICS_TOKEN` is set, only that token is accepted; otherwise `PEB_ADMIN_TOKEN`. A wrong or missing token yields `401`. If neither variable is set, the endpoint answers `404`. Only `GET` and `HEAD` are allowed.
- **Content-Type:** `text/plain; version=0.0.4; charset=utf-8`.
- **Names:** all series start with `peb_` and carry `# HELP` and `# TYPE`. Per-server series use the label `server="<uuid>"`; backup counts use `status`, maintenance series use `type` (`prune`, `check`, `stats`).
- **Absent instead of zero:** a series that has no value yet (no statistics run, no backup, offsite copy not configured) is not exported.
- **Cache:** database-derived values are cached for 30 seconds.
- **Stability:** metrics are only added, never renamed within a major version.

---

## Notes for the extension

- One call each to `/status` and `/summary` is enough for the dashboard header. `/history` provides the series for charts.
- `/servers` and `/backups` contain only UUIDs. The extension fetches names and owners from the Panel.
- The extension must reach the gateway via an address the Panel server can see. The token belongs in a `secret()` field of the extension settings.
- The dashboard shows a warning until `PUT /settings/password-ack` has confirmed that the restic password is stored elsewhere. The emergency sheet is built in the browser from `/status`, `/summary` and `/servers`; it never contains the password.
- Maintenance window, retention, targets and notification settings are gateway egg variables and are not changed through the API for now. `GET /notify` and `POST /notify/test` show and test the notification setup.
