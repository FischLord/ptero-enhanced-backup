# PEB: Pterodactyl Enhanced Backup

Incremental, deduplicated [restic](https://restic.net) backups for Pterodactyl, through the native S3 backup driver.

Server owners keep using the normal Backups tab, unchanged. Behind it, the PEB gateway receives each backup, stores it deduplicated in a restic repository (NAS, cloud, SFTP, S3, rclone) and gives operators an admin dashboard (an extension for Pterodactyl Panel 2.0) with storage targets, status and savings. The gateway runs as an ordinary Pterodactyl server from its own egg, so no extra host or hand-written Docker commands are needed.

Pterodactyl and Wings are never modified. PEB only uses official interfaces: the S3 backup adapter and the Panel 2.0 extension system.

## Features

- Incremental, deduplicated storage with restic, also across servers
- Works with the native Backups tab: create, restore, download and delete as before
- Gateway as a Pterodactyl server (egg + Docker image): console, logs and limits via the Panel
- Daily maintenance window: `prune`, `check`, repository statistics, with a history of every job
- Admin dashboard: savings (GB, %, factor, cost), storage target usage and forecast, 30-day history, per-server status with warnings
- File browser per backup with single-file download, without restoring the whole server
- Notifications for failed backups and maintenance (ntfy)
- Retention via the Panel's own backup rotation (`backup_limit`)

## Screenshots

_Screenshots will follow._

## Requirements

- Pterodactyl Panel **2.0** for the admin extension. The gateway alone works with Panel 1.x and 2.0.
- Wings **1.13 or newer**
- A Wings node that can run the gateway as a server, and a restic-compatible storage target
- Docker (to run the gateway image, and to build and test the project)

## Quick start

This is a high-level outline; see `gateway/README.md` and `extension/README.md` for all options.

1. **Create the gateway server.** Import `egg/egg-peb-gateway.json` as an egg (image `ghcr.io/fischlord/peb-gateway`) and create a server from it on a node of your choice. Set the egg variables: S3 access key and secret, bucket name, restic repository and password, and a `PEB_ADMIN_TOKEN` of at least 32 characters. Set the server's `backup_limit` to 0.
2. **Point the Panel at the gateway.** In the Panel `.env`:
   ```
   APP_BACKUP_DRIVER=s3
   AWS_ENDPOINT=https://<gateway address>
   AWS_USE_PATH_STYLE_ENDPOINT=true
   AWS_DEFAULT_REGION=us-east-1
   AWS_BACKUPS_BUCKET=<bucket>
   AWS_ACCESS_KEY_ID=<access key>
   AWS_SECRET_ACCESS_KEY=<secret>
   BACKUP_MAX_PART_SIZE=268435456
   ```
   The gateway must be reachable under exactly the host name the Panel signs. Do not put it behind an SSO proxy.
3. **Configure Wings** (`config.yml`):
   ```
   system:
     backups:
       compression_level: none
       restore_host_allowlist:
         - <gateway host or IP>   # needed if Wings reaches the gateway on a private address
   ```
4. **Install the extension** (Panel 2.0): build it with `make pack` in `extension/` and install the `.pteroext` via Admin -> Extensions, then enter the gateway URL and admin token in its settings.

Existing backups are not migrated; new backups go through the gateway.

## How it works

```
Server owner --(Backups tab, unchanged)--> Panel
                                             | presigned S3 URLs
                                             v
Wings --tar.gz in parts--> PEB gateway --restic backup--> repository (NAS / cloud / SFTP / S3 / rclone)
   ^                        |  SQLite: file index, metrics
   +--presigned GET---------+
                            ^
Admin --Panel extension (dashboard, file browser)--+  (admin API, bearer token)
```

More detail in [docs/architecture.md](docs/architecture.md) and [docs/admin-api.md](docs/admin-api.md).

## Known limitations

- Only storage is incremental: Wings still packs and sends the complete server for every backup.
- Wings stores the archive locally first, so it temporarily needs space the size of the server.
- The S3 setting is panel-wide: one gateway serves all nodes.
- The browser download button talks to the gateway directly, so it needs an externally reachable HTTPS address (not behind an SSO proxy).
- Wings only accepts restore downloads served as gzip and blocks private addresses unless allowlisted.
- The restic password is the key to all backups. Keep a second safe copy.

The full list is in [docs/architecture.md](docs/architecture.md) and [gateway/README.md](gateway/README.md).

## Security notes

- The S3 API and the admin API (`/admin/v1`) share one port. The admin API is protected by a bearer token (at least 32 characters), but the token travels in plain text unless TLS is in front.
- Restrict the gateway port with a firewall so that only the panel, the Wings nodes and the reverse proxy can reach it, and put TLS in front of any address that leaves your network.
- Only the presigned download path needs to be reachable from browsers. If you expose the gateway publicly, consider blocking `/admin/` at the reverse proxy.

## Free and Pro

This repository contains the complete Free edition: gateway, dashboard, file browser, maintenance, and Panel-based retention. A separate **Pro** extension (`peb-backups-pro`) adds advanced retention policies (keep daily/weekly/monthly with a preview). It is sold separately on BuiltByBit and is not part of this repository.

| | Free (this repository) | Pro |
|---|---|---|
| Gateway, S3 interface, deduplication, restore | yes | yes |
| Dashboard, file browser, maintenance | yes | yes |
| Retention | Panel rotation (`backup_limit`) | policy-based (daily/weekly/monthly, per server, with preview) |

## Development

```
cd gateway && make test        # Go tests in Docker
cd extension && make setup && make install && make check
```

## License

[GNU AGPL-3.0](LICENSE).

## Support

Please use GitHub Issues for questions and bug reports. Never post tokens, keys or restic passwords.

## Trademark

Pterodactyl(R) is a registered trademark of its respective owners; this project is not affiliated with or endorsed by them.
