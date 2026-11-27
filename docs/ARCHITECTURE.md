# V2 architecture

The renderer uses the V1 public Nginx paths and the original settings and cache directories. Nginx terminates plain HTTP for the trusted renderer network and uses TLS with SNI to access the HTTPS origin and CDN. Cache policy is kept in Nginx.

Nginx proxies `/ws` to the loopback-only Go Fanout. For each sharing identity, one generation owns one overseas socket. Identity comprises the normalized WSS endpoint, full SHA-256 token digest, channel and renderer mode. The WebSocket protocol uses one writer per socket. The server lock orders membership, frames and the generation transition. Network reads and writes occur outside the lock. Per-client and per-group queues and the global byte budget bound retained delivery data. Queue overflow closes a downstream peer or aborts an upstream generation. Clients joining an active generation receive their own acknowledgement, then any available complete call, then live frames. On upstream loss, any client with pending old-generation delivery must rejoin. In-sync clients stay connected and receive a new acknowledgement. Existing complete state and response IDs expire with the generation.

Admin runs under a dedicated account, serves embedded assets, authenticates sessions and reads bounded local state snapshots or route-level logs. The configuration helper runs as its own root unit on a Unix socket restricted to Admin. The helper validates typed data, owns the config lock and writes the backups and transaction journal. It uses fixed commands for config validation and Nginx reload.

| V1 surface | V2 behavior |
|---|---|
| `/render`, other renderer pages and API | Live proxying, with verified upstream certificates |
| CDN templates and fingerprinted JS | Nginx cache with origin/CDN/host isolation. Cookies, authorization, query strings and ranges bypass caching. Response directives retain their normal Nginx effects. |
| Shared WebSocket and leader | Same protocol fields and bidirectional text/binary. The oldest live peer leads and the first eligible response per trxId is relayed. |
| `join_res`, `trxId`, latest `call` replay | V2 confirms the upstream trxId before using its response. V2 canonicalizes local ack and keeps the replay frame type. |
| WebSocket reconnect | New generation clears snapshot, ack and dedup. Backoff and jitter cap retries. |
| `/run/ws-fanout/status.json` | Reader accepts V1 and V2. V2 preserves regular keys and adds generation and queue fields. The display-only token ID is keyed per process. Saved connections use the acknowledged clients across ready groups. |
| Admin JSON endpoints | Legacy unversioned paths and V2 aliases; reconnection and traffic additions versioned as described in the API contract. |
| Current config and token | Original settings format and paths, blank token retains current value, discovery requires current channel and token. |
| Password | PBKDF2-SHA256 version 1 remains readable and writable by either runtime. Sessions are reissued after process changes. |
| Original HTTPS scan and F12 hint | Same operations and CDN ranking. V2 revalidates every redirect and bounds requests, time and bytes. The DNS validator expects ASCII hostnames. |
| Default management on all interfaces | Loopback by default. TLS management and explicit trusted LAN HTTP bindings are available in the V2 environment file. |
| V1 Python admin and relay | Kept in source as comparison baselines. Previous deployment assets remain on the server during upgrade. |

V2 defaults to 512 local sockets and 64 groups, 1 MiB decoded per message, 32 messages / 2 MiB per socket queue and 64 MiB total queued bytes. Read reservations use an independent pool of the same size. Per-group complete-state and acknowledgement buffers are limited by group count and message sizes. Linux systemd applies a further memory ceiling. Downstream compression is disabled by default; upstream advertises per-message deflate.
