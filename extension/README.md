# PEB Backups (Pterodactyl 2.0 extension, Free)

Admin interface for the PEB gateway. The extension is display and proxy only: all data comes through the gateway's admin API (`docs/admin-api.md`). The admin token stays in the Panel (encrypted, `secret()`); the browser never sees it. If the gateway goes down, the routes answer with a clean JSON error, and backups keep running independently.

## Features

Admin -> **Backups** (`/panel/backups`):

1. **Savings:** without PEB (logical), actually stored (marked "approx." if the gateway does not measure the size on disk), saved in GB and %, dedup factor, cost saved per month. Responsive grid (max. 3 tiles per row, values are never truncated), one tooltip per tile with an explanation.
2. **Storage target:** card below the tiles: stacked bar (used by PEB / other data / free), total, free and used, "Full in approx. N days" and "Room for approx. N more backups". Measured for local repositories (NFS), otherwise estimated from `PEB_REPO_CAPACITY_BYTES` (marked "est."). A tooltip explains the estimate.
3. **Health:** gateway and repository reachable, last prune and check with result, next maintenance window, running and queued ingests, spool usage, failures and success rate (30 days).
4. **History (30 days):** repository size as a line, per day the size of backups versus really new data as bars (red dots = days with failures), forecast "full in N days". Plain SVG, follows the light and dark theme.
5. **Servers:** name (link to the admin page), node, owner, last successful backup with warning badges (overdue, last backup failed, never), size of the last backup (uncompressed), of which really new data after deduplication (tooltips explain both), count, last duration. Unknown UUIDs are shown as "Unknown server".
6. **Backups per server and file browser** (directories, sizes, dates, pages) with single-file download. The download is passed through the Panel (streamed, without intermediate storage).
7. **Maintenance:** history and "Run now" with a confirmation dialog (jobs selectable).

The server list shows **backups / backup limit** of the Panel per server (running and successful backups, which is how the Panel counts as well).

Additionally a **Backups** tab on the admin page of each server (`/panel/servers/{id}/backups`).

Language: English, German is included (`src/client/lang/*.json`, follows the Panel language). For another language, add an `xx.json` with the same keys.

## Retention in the Free edition: Panel rotation

The Free extension does not delete anything itself. The built-in rotation of Pterodactyl 2.0 applies (verified in the source of `2.0.0-beta.2`: `Actions\Backups\InitiateBackup`, `DeleteBackup`, `Jobs\Schedule\RunTaskJob`):

- Every server has a **backup limit** (`backup_limit`, Admin -> Server -> Build configuration or the Application API). It counts running and successful backups, not failed ones. **0 means: no backups allowed.**
- **Manual backups** (Backups tab) are rejected at the limit (`TooManyBackupsException`). The owner has to delete a backup themselves.
- **Scheduled backups** (schedule task "Create backup") rotate: at the limit the Panel deletes the oldest **unlocked** backups as soon as Wings has accepted the new one. If Wings rejects it, nothing is deleted. If there are not enough unlocked backups, the run fails.
- **Locked backups** (lock icon in the Backups tab) are never deleted by the rotation. They do count against the limit.
- Deletion goes through the Panel's own deletion path: with S3, `DeleteObject` goes to the gateway (the snapshot is forgotten, `prune` cleans up in the maintenance window), then the Panel row is removed. Panel and gateway stay consistent.

Recommendation: backup limit = desired number plus a few locked backups, and create the servers' schedules as "Create backup" tasks. More control (keep daily/weekly/monthly, preview, default and per-server policies) is offered by the separate **Pro extension**.

## Extension point for Pro

`PebBackups\Pro\ProRegistry` (singleton in the container): the `peb-backups-pro` extension registers itself at boot with ID, version and features. The Free extension reports this to the UI via `GET /api/admin/extensions/peb-backups/edition`; when Pro is active, the small "Pro" hints (server tab, maintenance) disappear. Pro requires `peb-backups` in `requires.extensions`, so the Panel loads Free first. If Pro is disabled, its provider is not loaded and the registry stays empty. Pro brings its pages through its own manifest; nothing more is needed.

## Settings (Admin -> Extensions -> PEB Backups)

| Field | Default | Meaning |
|---|---|---|
| Gateway base URL | empty | Address of the gateway as seen from the Panel server, e.g. `http://10.0.0.5:9000` (without `/admin/v1`) |
| Gateway admin token | empty | Value of the gateway's `PEB_ADMIN_TOKEN`. Leaving it empty keeps the stored value. |
| Storage price per TB and month | 6 | Basis for "saved per month" (1 TB = 10^12 bytes) |
| Currency | EUR | ISO 4217 code |
| Warn after hours | 26 | Servers without a successful backup in this time get a warning |

Settings can only be changed through the Panel UI (session). Not possible with an API key.

## Endpoints (`/api/admin/extensions/peb-backups`, root admins only)

`GET /edition` (is Pro active?), `GET /overview` (status, metrics, history, computed savings), `GET /servers`, `GET /backups?server=&status=&limit=&offset=`, `GET /backups/{uuid}/files?path=&limit=&offset=`, `GET /backups/{uuid}/file?path=` (download), `GET /maintenance`, `POST /maintenance/run` (body `{"jobs":["prune","check","stats"],"confirm":true}`; without `confirm` you get 422). Errors: `{"error": "<code>", "message": "..."}` with `not_configured` (503), `gateway_unreachable` (502), `gateway_timeout` (504), `gateway_unauthorized` (502, never 401, so the UI does not mistake it for an expired session), `api_disabled` (502), and 400/404/409/503 from the gateway unchanged.

There is no dedicated permission (`ext.peb-backups.*`): the extension permission system only concerns server subusers. The routes are reachable by root admins only anyway.

## Large single files: time limits

A single-file download restores the file from the repository (`restic dump` up to the file). For large backups the gateway needs **minutes until the first byte**, after which the stream runs through the Panel. The extension lifts the PHP limit itself (`set_time_limit(0)`), but the web server has to cooperate:

- Apache: set `Timeout` (and, with PHP-FPM via `ProxyPassMatch`/`SetHandler proxy:`, also `ProxyTimeout`) high enough, e.g. 7200.
- nginx: `fastcgi_read_timeout 7200s;` (with a proxy `proxy_read_timeout`) and `fastcgi_buffering off;` for streaming.
- PHP-FPM: `request_terminate_timeout = 0` (or at least 7200) in the pool.

Before the download, the UI makes a probe call (`?probe=1`: session, backup complete, file in the index) and then starts a normal browser download. If the probe fails, a message is shown instead of a saved JSON file. If the stream breaks later, the Panel logs it (`PEB download aborted mid-stream`) and the browser reports an incomplete download because of the set `Content-Length`. The gateway itself limits a download to 2 hours (`PEB_DOWNLOAD_TIMEOUT`). JSON calls (not downloads) have a time limit of 8 seconds.

## Build, test, package

Everything runs in Docker; Node and PHP are not needed on the host. The SDK `@pterodactyl/sdk` is not on npm but part of the Panel source. `make setup` therefore fetches it to `~/.cache/peb-ext/panel-src` (tag `v2.0.0-beta.2`; Docker Desktop does not share `/tmp` with containers).

```sh
cd extension
make setup      # once: Panel checkout, npm ci, SDK test runtime, composer install, PHP image
make install    # npm dependencies of the extension (npm 11; npm 10 fails on the peer tree)
make check      # tsc + vitest + PHP unit tests (pest)
make pack       # check, build, p:extension:doctor, package to build/peb-backups-<version>.pteroext
```

Single targets: `make typecheck`, `make test-js`, `make test-php`, `make build`, `make doctor`, `make lang`. The language files `resources/lang/*/messages.php` are generated from `src/client/lang/*.json` during the build (not checked in).

## Install

```sh
# on the Panel server
runuser -u www-data -- php artisan p:extension:install /path/peb-backups-1.1.0.pteroext --enable
chown -R www-data:www-data extensions public/assets/extensions
```

or Admin -> Extensions -> Install. Then enter gateway URL and token and reload the Panel in the browser. The Panel must serve `/assets/extensions` (the Apache `/assets/` block from the upgrade guide).

## Structure

- `extension.json`: manifest, two screens (`backups` in the admin area, `server-backups` as a tab of `admin.server`)
- `src/` (PHP, `PebBackups\`): `PebBackupsProvider` (settings, routes, translations, `Pro\ProRegistry`), `Gateway/` (client with time limits and error mapping), `Services/` (server mapping, calculations), `Http/Controllers/`
- `routes/admin.php`: the proxy routes
- `src/client/` (React/TypeScript): screens, components, custom SVG chart code, `lang/`
- `tests/Unit/` (PHP, no database) and `tests/client/` (Vitest, not part of the package)
- Build rules and the PHP image live in `../tools/` (`extension.mk`, `Dockerfile.php`)
