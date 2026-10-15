# Fast Download Sync

Small Go utility for scanning configured game-server folders and publishing selected assets to a web-download directory.

## Current commands

- `fastdownload-sync validate` — validate configuration.
- `fastdownload-sync scan` — list eligible files and SHA-256 hashes.
- `fastdownload-sync sync` — copy new/changed files using a temporary file and atomic rename.
- `fastdownload-sync watch` — synchronize on filesystem events and run a periodic reconciliation scan.
- `fastdownload-sync status` — show tracked file count and last successful run.
- `fastdownload-sync version` — print version.

## Safety

- Only configured source paths and extensions are scanned.
- Symbolic links and non-regular files are rejected or skipped; destination symlinks are never followed.
- Files are verified with SHA-256 before publication.
- Files are never automatically deleted from the destination.
- Run `validate` and `scan` before enabling the systemd service template in `deploy/fastdownload-sync.service`. The service is not installed or enabled automatically.
- The destination path is intentionally not wired into Nginx by this program.

## Configuration

See `configs/config.example.yaml`. The example source path contains a placeholder and must be changed to a real Pterodactyl server UUID before use.

## Runtime behavior

The `watch` command performs an initial synchronization, reacts to filesystem events with a short debounce, and also repeats a full reconciliation at `scan_interval`. It handles SIGINT/SIGTERM for clean shutdown. The SQLite database stores file hashes, sizes, modification times and run history. The scanner avoids re-hashing unchanged files when size and modification time match the saved state. Published file permissions are normalized to 0644.

Published paths follow `destination/source-name/relative-path`. For example, a file at `cstrike/maps/custom.bsp` from source `css-main` is published at `fastdownload/css-main/maps/custom.bsp`. The corresponding server FastDL URL must include `/fastdownload/css-main`; this project does not modify game-server configuration or Nginx.
