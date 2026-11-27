# Management HTTP API

Content type is JSON. The API accepts the paths below under both `/api/` and `/api/v2/`. Host and socket-peer address are validated before authentication. POST requires an Origin equal to the request origin, including scheme and port. All requests and responses use same-origin credentials and no-store semantics. The status `ok` flag identifies business success.

| Method | Path suffix | Auth | Request and result |
|---|---|---|---|
| GET | `session` | Optional | `authenticated` boolean |
| POST | `login` | No | `username`, `password`; issues an HttpOnly Strict session cookie |
| POST | `logout` | Session | Revokes this session |
| GET | `status` | Session | `status` with availability, timestamp, aggregate WebSocket counts and group details |
| GET | `traffic` | Session | `traffic` from the latest 512 KiB of safe access log, six 5-minute upstream request samples |
| GET | `config` | Session | `config` with hostnames, channelId, accountId and tokenPresent, plus `csrfToken` |
| POST | `config` | Session and CSRF | `originHost`, `cdnHost`, `channelId`, `accountId`, optionally `token`; redacted config and backup ID |
| POST | `rollback` | Session and CSRF | `backup` identifier; redacted restored config and a new backup ID |
| POST | `password` | Session and CSRF | `currentPassword`, `newPassword`; revokes all sessions |
| POST | `discover` | Session and CSRF | `sourceUrl`, optionally `evidenceUrl`; validated metadata, candidates and parameters supplied by scan input or redirect |

State-changing operations with CSRF require `X-CSRF-Token`. Nonces bind to a session, expire after 10 minutes, and are consumed once. Invalid input returns 400, missing session 401, origin/nonce/peer errors 403, login budget exhaustion 429. A replacement nonce is returned after an authorized mutation when the session is still active.

Status readers accept fanout snapshot `version: 1` and `version: 2`. V2 adds `state`, `reconnects`, `queueOverflows`, `slowDisconnects`, `goroutines` and group `upstreamReady`, `generation`, `connectedSeconds`, `queuedMessages`, `reconnects`. When `available` is false, counts have no live meaning and groups are empty. Both file age and generation timestamp must be current.

Public renderer paths are managed by Nginx. Admin and Fanout expose `/metrics` and `/healthz` via their local management listeners. Only local peers can read Fanout metrics. Admin metrics require local or authenticated access. Metric names are stable under `overseas_fanout_` and `overseas_admin_`. Local delivery latency describes time from receiving a frame from the upstream socket to completing the downstream socket write. It excludes the overseas round trip.
