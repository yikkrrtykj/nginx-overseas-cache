# Installation, upgrade and recovery

Use a disposable Ubuntu 24.04 or 26.04 host to verify the release first. An operator installs the package on the target host after approving the exact release, its backup scope and the cutover window.

1. Install the Ubuntu Nginx and ca-certificates packages. Renderer cookie rewriting requires Nginx 1.19.3 or newer (the stock Ubuntu 22.04 Nginx 1.18 lacks this directive). Ensure systemd is running.
2. Copy the tarball for Linux amd64 or arm64 and `SHA256SUMS`. Verify it with `sha256sum -c --ignore-missing SHA256SUMS`.
3. Unpack and run `sudo bash scripts/install-admin.sh` from the package directory. For a fresh host supply a password at the terminal prompt. Operations retain the original V1 settings, legacy runtime files, cache directory and password record.
4. Forward Admin through SSH for the default local binding, or configure the Admin environment file. On V1 upgrades the generated ACL preserves the management networks and adds `127.0.0.1/32` for the loopback listener. Confirm uptime, TLS, read-only diagnostics and Nginx state before changing renderer settings.

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

Repeat the installer for a package upgrade. It takes a fresh snapshot before switching services. When installation fails it restores that snapshot and reloads old Nginx state. A durable journal names the pre-operation installation snapshot until install, rollback or uninstall commits. On boot, the ordered `overseas-cache-recovery.service` restores an interrupted installation and validates any pending configuration before Nginx starts. If recovery fails, the journal remains and systemd reports the failure. Inspect `journalctl -u overseas-cache-recovery.service` and use the operator snapshot if needed. The recovery unit and its drop-ins remain available after restoring V1. To inspect changes, use the backup ID and fixed settings paths without printing secrets. Config rollback inside the UI restores only the managed host and renderer fields into the installed Nginx template.

Use `sudo bash scripts/install-admin.sh rollback` to restore the first pre-V2 system snapshot. A V1 rollback returns its saved network and renderer settings and migrates the latest V2 password record back. `uninstall` uses the same snapshot restoration and removes the active V2 service files. The log and cache directories, legacy assets and backups remain for operator-managed retention.

## Recovery gate and its lifecycle

Installation first stages the root-owned recovery entrypoint in `/usr/local/lib/overseas-cache/install-admin.sh`, a compatible recovery binary in `/usr/local/lib/overseas-cache/config-helper`, the oneshot unit, and the Nginx, Admin and Fanout dependency drop-ins. Boot-time configuration recovery uses this staged binary so it can replay a journal while an older runtime helper is installed. The package validates and loads the gate before changing managed runtime files. An interrupted gate installation can be retried from the verified package. The gate infrastructure is deliberately retained across rollback and uninstall. With neither pending journal present, the unit is skipped and retained drop-ins have no runtime service effect.

A pending installation record names the saved snapshot and binds its contents to a checksum. The active file under `/run` is a temporary permission to activate a validated trial. The gate also verifies the installation lock and the owning process identity. A stale file cannot authorize startup. The gate can restore before a service starts when no managed process is running. If starting a service while managed processes are running would require a synchronous systemd stop or reload inside the gate, startup fails and the journal is retained. Use `sudo bash scripts/install-admin.sh recover` from the verified release to restore the confirmed deployment in the same boot. Inspect `journalctl -u overseas-cache-recovery.service` if recovery cannot finish. While an installation journal exists, the V2 configuration helper refuses configuration reads, writes and configuration-only recovery under the shared configuration lock. Use installer recovery before manually reloading Nginx during an interrupted installation. While upgrading an older V2 helper, keep configuration changes paused until install or recovery completes. An early interruption can leave the previous helper active against the existing files. Admin password migration consumes a bounded regular file captured inside the protected snapshot.

After decommissioning, keep the recovery entrypoint and its dependencies while either pending journal exists. When no V2 service will be used, verify that both journals are absent with the installation and configuration operations quiesced. Remove only the three package-owned `overseas-cache-recovery.conf` drop-ins, then the recovery unit, recovery entrypoint and its recovery binary, and run `systemctl daemon-reload`. Leave other Nginx drop-ins and deployment backups under operator ownership.
