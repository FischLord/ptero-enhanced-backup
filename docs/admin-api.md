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

Backups with status `deleted` are not counted in any total.

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
    "running_job": null
  }
}
```

- `repo.type`: `local`, `rest`, `sftp`, `s3`, `rclone`, `b2`, `azure`, `gs`, `swift`. `location` is shown without user and password.
- `repo.reachable` is re-checked at most once per minute (`checked_at`).
- `ingest.running`: ingests currently using restic. `queued`: waiting for a free slot or for a maintenance run to end.
- `last_prune` / `last_check`: last finished job of that type (including `skipped`), otherwise `null`. `running_job`: the current maintenance job or `null`.

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
      "last_data_added": 52428800
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
      "last_data_added": 0
    }
  ]
}
```

`last_status` is `complete` or `failed` (status of the most recent finished backup). `backup_count`, `logical_bytes`, `data_added_total` and `avg_duration_ms` refer to completed, non-deleted backups. `last_backup_bytes` (uncompressed size) and `last_data_added` (really newly stored after deduplication) belong to the server's most recent completed backup, 0 without a completed backup. `logical_bytes` and `data_added_total` remain as totals.

## GET /admin/v1/backups

Parameters (all optional): `server` (server UUID), `status` (`complete`, `failed`, `pending`, `deleted`; without it everything except `deleted`), `limit` (default 50, maximum 500), `offset`. Newest first.

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
    { "name": "region", "path": "world/region", "type": "dir", "size": 0, "mode": "0755", "mtime": "2026-10-23T20:11:02Z" },
    { "name": "data", "path": "world/data", "type": "dir", "size": 0, "mode": null, "mtime": null },
    { "name": "level.dat", "path": "world/level.dat", "type": "file", "size": 3145728, "mode": "0644", "mtime": "2026-10-24T02:59:58Z" }
  ]
}
```

- `type`: `dir`, `file`, `symlink`, `other`. `size` is always 0 for directories.
- `mode`: permissions as an octal number in a string.
- `404` for an unknown backup UUID, `409` (`not_complete`) if the backup is still running or failed.

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

- `type`: `prune`, `check`, `stats`. `trigger`: `schedule` or `manual`.
- `result`: `queued`, `running`, `ok`, `failed`, `skipped`. `skipped: busy` means running backups or restores were still not finished after `PEB_MAINTENANCE_BUSY_WAIT` (15 min); the job was skipped for today.
- A job that exceeds `PEB_MAINTENANCE_TIMEOUT` (3 h) ends as `failed` with the message `<job> timed out after 3h0m0s`.
- `message` is short and generic (`prune failed, see gateway log`); `bytes_freed` exists only for `prune` and only if the restic output was readable.
- The daily run consists of `prune` (if enabled), `check` (if due) and `stats`. Size refreshes after backups do not appear as a job.

## POST /admin/v1/maintenance/run

Starts maintenance jobs immediately. Only one maintenance run happens at a time.

```
POST /admin/v1/maintenance/run
Content-Type: application/json

{"jobs": ["prune", "check", "stats"]}
```

`jobs` may contain `prune`, `check` and `stats` (at least one). The order is fixed: prune, check, stats. A manually triggered `prune` also runs with `PEB_PRUNE_ENABLED=false`, and `check` also when not due.

Response `202`:

```json
{ "id": 15, "job_ids": [15, 16, 17] }
```

`id` is the first job, `job_ids` contains all of them (in execution order). Progress is shown by `GET /admin/v1/maintenance` (result `queued`, `running`, then the final result) and `running_job` in `/status`.

Errors: `409` (`busy`) if a run is already active. `400` for an invalid body or an unknown job name.

---

## Notes for the extension

- One call each to `/status` and `/summary` is enough for the dashboard header. `/history` provides the series for charts.
- `/servers` and `/backups` contain only UUIDs. The extension fetches names and owners from the Panel.
- The extension must reach the gateway via an address the Panel server can see. The token belongs in a `secret()` field of the extension settings.
- Maintenance window, retention, targets and notifications are gateway egg variables and are not changed through the API for now.
