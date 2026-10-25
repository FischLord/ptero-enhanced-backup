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
7. **Maintenance:** history and "Run now" with a confirmation dialog (jobs selectable). The offsite card has a "Copy now" button.
8. **Trash** (tab with a count badge when it is not empty): backups deleted in the Panel and backups of deleted servers wait here for the retention set on the gateway (`PEB_TRASH_RETENTION`, `PEB_ORPHAN_RETENTION`). The table shows server, backup, reason, size, deletion date and removal date. "Restore" makes the backup a normal gateway backup again (browse and download it under Servers; it does not reappear in the Panel, which deleted its own record). "Delete now" removes it for good after a confirmation. Needs Gateway 0.2.0.

The server list shows **backups / backup limit** of the Panel per server (running and successful backups, which is how the Panel counts as well).

9. **Restic password safety:** until an admin confirms ("I have stored the password in a second place"), a warning banner sits at the top of the overview; afterwards only a quiet note remains. The state is stored in the gateway, not in the browser. The **emergency sheet** opens in a new window (nothing is saved on a server) and is meant to be printed: repository address (and the offsite repository), gateway address, restore commands without the gateway (`restic snapshots --tag server:<uuid>`, `dump`, `restore`, rclone note for Google Drive), the list of servers with UUIDs, and an empty box for writing the password by hand. The password itself is never shown. The **Emergency key** card creates a second restic key with its own random password, shown exactly once with a copy button and stored nowhere; it lists the keys and deletes other keys after confirmation (the key in use cannot be deleted). Needs Gateway 0.2.0.

10. **Notifications:** card on the overview that shows the notification channels configured on the gateway (redacted) and the message language, and sends a test notification with a result per channel. Channels are set through egg variables on the gateway, not in the Panel. Needs Gateway 0.3.

Admin -> **Setup** (`/panel/setup`): setup wizard, see below.

**Deleted servers.** The Panel deletes the backup rows of a deleted server without telling the S3 backend. The extension reports it to the gateway so the backups go to its trash and are removed after `PEB_ORPHAN_RETENTION` (default 30 days): right away through the Panel's server-operation event (`delete`), hourly through the Panel scheduler (`php artisan schedule:run` must run, as for any Panel) with a comparison of all server UUIDs, and when the dashboard is opened (at most every 5 minutes). An empty server list is never sent. If a server turns out to still exist, the gateway restores its backups.

Additionally a **Backups** tab on the admin page of each server (`/panel/servers/{id}/backups`).

Language: English, German is included (`src/client/lang/*.json`, follows the Panel language). For another language, add an `xx.json` with the same keys.

## Setup wizard (Admin -> Setup)

One page with five steps. Nothing is written to your `.env` or to Wings; you copy the blocks yourself.

1. **Pre-check:** shows what the Panel is configured with, as OK / warning / missing: backup driver is `s3`, `AWS_ENDPOINT` set, path-style on, bucket and keys set, `BACKUP_MAX_PART_SIZE` (warning below 16 MiB or above the gateway limit of 1 GiB; recommended 256 MiB), gateway URL and admin token set in the extension settings. Secrets and addresses are never shown, only whether they are set.
2. **Gateway egg:** imports the PEB Gateway egg that ships with the extension (`resources/egg/`, a copy of `egg/egg-peb-gateway.json`) with the Panel's own egg importer. If an egg with the same name and author exists, the button becomes **Update egg**; no duplicate is created.
3. **Gateway server (optional):** form with owner (root administrators only), node, a free allocation (and the gateway address, if the allocation has no IP alias; 0.0.0.0 and :: are rejected), memory and disk (defaults 1024 MiB / 20480 MiB), bucket, region and an optional restic repository path. The S3 access key and secret, the admin token (48 characters) and the restic password are generated on the server with a cryptographically secure generator and set as egg variables. They are shown **exactly once** after creation. They are not logged. They are also saved as egg variables of the new server (plain text in the Panel database), so the owner and subusers with startup permission can read them under Server > Startup; therefore only root administrators can be chosen as owner. The restic password is the key to all backups; store it now. The server is created with backup limit 0 and started after installation. Creation uses the Panel's own server creation service; if Wings cannot be reached, the Panel removes the server again and nothing is left behind.
4. **Snippets:** the `.env` block for the Panel (with the gateway address taken from the allocation and the generated values after step 3, otherwise placeholders) and the Wings `config.yml` snippet (`compression_level: none`, `restore_host_allowlist`).
5. **Self-test:** (a) gateway reachable (`/healthz`), (b) admin token valid, (c) a signed S3 request with the Panel's own S3 credentials (`CreateMultipartUpload` on a throw-away key, then `AbortMultipartUpload`), (d) warning if the S3 endpoint or gateway URL is not HTTPS (browser downloads of backups need it). Each check is OK, warning or failed and comes with a hint how to fix it.

The wizard uses internal Panel classes for steps 2 and 3 (egg import and update, server creation, node, allocation and user lists). They are accessed only through one adapter (`src/Setup/PanelAdapter.php`). If a Panel version no longer has them, steps 2 and 3 show a short manual instruction instead of failing, and the pre-check, snippets and self-test keep working.

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

`GET /edition` (is Pro active?), setup wizard: `GET /setup`, `POST /setup/egg` (body `{"mode":"import"|"update"}`), `GET /setup/nodes`, `GET /setup/nodes/{id}/allocations`, `GET /setup/users?q=`, `POST /setup/server`, `POST /setup/self-test`, `GET /overview` (status, metrics, history, computed savings), `GET /servers`, `GET /backups?server=&status=&limit=&offset=`, `GET /backups/{uuid}/files?path=&limit=&offset=`, `GET /backups/{uuid}/file?path=` (download), `GET /trash`, `POST /trash/{uuid}/restore`, `DELETE /trash/{uuid}` (body `{"confirm":true}`), `GET /maintenance`, `GET`/`PUT /password-ack` (body `{"acknowledged":true}`), `GET /emergency-sheet` (data for the sheet, without password), `GET /keys`, `POST /keys` (body `{"label":"hoster"}`, returns the new password once), `DELETE /keys/{id}`, `POST /maintenance/run` (body `{"jobs":["prune","check","stats"],"confirm":true}`, `copy` starts the offsite copy and needs a configured offsite repository; without `confirm` you get 422). Errors: `{"error": "<code>", "message": "..."}` with `not_configured` (503), `gateway_unreachable` (502), `gateway_timeout` (504), `gateway_unauthorized` (502, never 401, so the UI does not mistake it for an expired session), `api_disabled` (502), and 400/404/409/503 from the gateway unchanged.

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
make egg-sync   # after changing ../egg/egg-peb-gateway.json: update the bundled copy (make check fails if they differ)
make pack       # check, build, p:extension:doctor, package to build/peb-backups-<version>.pteroext
```

Single targets: `make typecheck`, `make test-js`, `make test-php`, `make build`, `make doctor`, `make lang`. The language files `resources/lang/*/messages.php` are generated from `src/client/lang/*.json` during the build (not checked in).

## Install

```sh
# on the Panel server
runuser -u www-data -- php artisan p:extension:install /path/peb-backups-1.3.0.pteroext --enable
chown -R www-data:www-data extensions public/assets/extensions
```

or Admin -> Extensions -> Install. Then enter gateway URL and token and reload the Panel in the browser. The Panel must serve `/assets/extensions` (the Apache `/assets/` block from the upgrade guide).

## Structure

- `extension.json`: manifest, three screens (`backups` and `setup` in the admin area, `server-backups` as a tab of `admin.server`)
- `src/` (PHP, `PebBackups\`): `PebBackupsProvider` (settings, routes, translations, `Pro\ProRegistry`), `Gateway/` (client with time limits and error mapping), `Services/` (server mapping, calculations), `Setup/` (setup wizard: pre-check, secret generator, snippets, self-test, adapter to the Panel's internals), `Http/Controllers/`
- `routes/admin.php`: the proxy routes
- `src/client/` (React/TypeScript): screens, components, custom SVG chart code, `lang/`
- `tests/Unit/` (PHP, no database) and `tests/client/` (Vitest, not part of the package)
- Build rules and the PHP image live in `../tools/` (`extension.mk`, `Dockerfile.php`)
