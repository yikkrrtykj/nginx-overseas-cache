# Permissions and security

| Boundary | Effective permissions |
|---|---|
| Nginx renderer | Allowlisted LAN IPv4/IPv6. The root redirect runs after Nginx access checks. Browser WebSocket Origin must match the renderer authority. |
| Fanout | Loopback listener under the `overseas-fanout` account. Nginx replaces destination headers. Outbound WSS verifies TLS and pins validated public DNS answers. |
| Admin | `overseas-admin` account, CIDR and Host checks, no forwarding-header trust, Secure cookies over TLS, same-origin POST and session-bound CSRF. |
| Mutation service | Unix socket owned by Admin, mode 0600. The separate root helper accepts only typed config read, apply and rollback. Nginx command paths and arguments are fixed. |
| Credentials | PBKDF2-SHA256, 600,000 rounds for new records, password hashing concurrency limit, five-attempt per-address limit, expiring bounded sessions. |

The V1 renderer URL shape includes a token, including in some authenticated asset paths. Keep port 23333 on the trusted renderer network. Browser history and cached metadata may hold these paths. Restrict access to the Nginx cache and configuration directories. WebSocket snapshots and metric labels never store the raw token. The dedicated Nginx traffic log contains route categories, time and counts. It omits URLs, headers and client addresses. Standard Nginx renderer error logs are suppressed because they can contain token paths. Data-plane failures appear as HTTP status counts and helper audit events.

Admin discovery accepts HTTPS port 443 and DNS names, validates all resolved addresses, dials a validated numeric address and verifies SNI against the hostname. Every redirect uses the same gate. It disables environment proxies, inherits no client cookies or Referer, and caps response headers, body size, redirects, requests and wall time. The explicit discovered Token is returned only to the authenticated operator for transferring the new renderer configuration.

Nginx authenticates the TLS peer with the system CA file and configured origin name. Nginx resolves named upstream pools during config load or reload. Admin validates public resolution before applying new hosts. Deploy egress controls appropriate to the site if origin DNS is outside administrative control.

Settings and backups are root-only. The Admin password is user-owned mode 0600 inside a 0700 state directory. The Linux unit does not run the web server as root. Cache bypass uses request credentials, query strings and ranges, and the upstream's response caching headers remain effective.

The relay pins the Gorilla WebSocket fix for [GO-2026-6278](https://pkg.go.dev/vuln/GO-2026-6278). The Go dependency checksum is generated from the exact Git tree. CI resolves modules and scans the finalized dependency set.
