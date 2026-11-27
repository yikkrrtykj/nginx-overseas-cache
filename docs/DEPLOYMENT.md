# Installation, upgrade and recovery

Use a disposable Ubuntu 24.04 or 26.04 host to verify the release first. An operator installs the package on the target host after approving the exact release, its backup scope and the cutover window.

1. Install the Ubuntu Nginx and ca-certificates packages. Renderer cookie rewriting requires Nginx 1.19.3 or newer (the stock Ubuntu 22.04 Nginx 1.18 lacks this directive). Ensure systemd is running.
2. Copy the tarball for Linux amd64 or arm64 and `SHA256SUMS`. Verify it with `sha256sum -c --ignore-missing SHA256SUMS`.
3. Unpack and run `sudo bash scripts/install-admin.sh` from the package directory. For a fresh host supply a password at the terminal prompt. Operations retain the original V1 settings, legacy runtime files, cache directory and password record.
4. Forward Admin through SSH for the default local binding, or configure the Admin environment file. Confirm uptime, TLS, read-only diagnostics and Nginx state before changing renderer settings.

| File | Purpose |
|---|---|
| `/etc/nginx/overseas-cache/admin-v2.env` | Optional Admin bind, allowed network and TLS settings |
| `/etc/nginx/overseas-cache/fanout-v2.env` | Optional relay resource controls |
| `/etc/nginx/overseas-cache/settings.conf` | V1-compatible renderer settings |
| `/var/lib/overseas-cache-admin/admin-password.json` | Active version-1 PBKDF2 password record |
| `/var/lib/overseas-cache-installer` | Installation snapshots |
| `/etc/nginx/overseas-cache/backups` | Config snapshots and crash-recovery marker |
| `/var/log/overseas-cache/traffic.log` | Safe route counts used by the UI |
| `/var/cache/nginx/web_cache` | Original cache storage |

For TLS use `OVERSEAS_ADMIN_HOST` with the desired IP, `OVERSEAS_ADMIN_ALLOWED_NETWORKS` with allowed CIDRs, and `OVERSEAS_ADMIN_TLS_CERT` / `OVERSEAS_ADMIN_TLS_KEY` with files readable by the Admin service account. Configure `OVERSEAS_ADMIN_ALLOWED_HOSTS` for a DNS name on the certificate. Direct HTTP outside loopback requires `OVERSEAS_ADMIN_ALLOW_HTTP=yes` and trusted management VLAN reachability.

Fanout knobs include `MAX_CLIENTS`, `MAX_GROUPS`, `MAX_MESSAGE_BYTES`, `MAX_QUEUE_MESSAGES`, `MAX_QUEUE_BYTES`, `MAX_TOTAL_QUEUE_BYTES`, `JOIN_TIMEOUT`, `WRITE_TIMEOUT`, `IDLE_TIMEOUT`, `PING_INTERVAL`, `RECONNECT_MIN`, `RECONNECT_MAX` and `STATUS_INTERVAL`. Duration values use Go duration syntax such as `15s`. Inspect the persisted `status.json` timestamps and loopback metrics before tuning.

Repeat the installer for a package upgrade. It takes a fresh snapshot before switching services. When installation fails it restores that snapshot and reloads old Nginx state. If config-apply recovery is interrupted, the helper uses the pending marker on startup. To inspect changes, use the backup ID and fixed settings paths without printing secrets. Config rollback inside the UI restores only the managed host and renderer fields into the installed Nginx template.

Use `sudo bash scripts/install-admin.sh rollback` to restore the first pre-V2 system snapshot. A V1 rollback returns its saved network and renderer settings and migrates the latest V2 password record back. `uninstall` uses the same snapshot restoration and removes the active V2 service files. The log and cache directories, legacy assets and backups remain for operator-managed retention.
