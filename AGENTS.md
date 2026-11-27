# Overseas Cache engineering guide

The nginx-overseas-cache repository owns the renderer data plane, WebSocket relay and management console. Keep implementation branches separate from main. Use loopback peers, fixture certificates, temporary configs and synthetic credentials for all automated tests. Production servers, live renderer accounts, deployment and merge require explicit operator authorization.

## Runtime and interfaces

- Nginx owns reverse proxying, cache policy, TLS verification and hostname rewriting.
- Keep the Go fanout, unprivileged Go Admin and socket-activated Nginx config helper in separate processes. Prefer Go standard-library packages and keep third-party dependencies pinned.
- Keep protocol parsing and lifecycle state in `internal/fanout`. Perform socket I/O outside the state lock. An upstream generation owns its acknowledgement, replay and deduplication state.
- Data-plane credentials belong in the protected renderer settings. Redact credentials from logs, errors, metrics, snapshots, URLs returned by read APIs and test evidence.
- Follow [the HTTP contract](docs/API.md) for compatibility with V1 and versioned endpoints. Coordinate any intentional contract change in the issue and update the compatibility matrix and regression tests.
- Keep the V1 Python and JavaScript assets usable as migration baselines until rollback support can be retired with operator approval.

## Console

Use [design.md](design.md) and the shared tokens in `web/static/app.css`. Type new UI data in `web/src/api.ts` and reuse controls and DOM utilities in `web/src/components.ts`. Keep one design system. Build all browser resources locally and retain keyboard and reduced-motion behavior. Model missing, stale, degraded and empty values separately. In screenshots, identify synthetic evidence by source.

## Verification and review

Run `go vet ./...` and the Go suite with the race detector, the Python/Node V1 regression suite, TypeScript strict checks, frontend lint, browser tests and the applicable installation tests. Functional changes should include focused regression evidence. Verify the rendered console at 1920×1080, 1440×900, 1280×720 and the mobile width supported by the browser suite. Record observed results from the final commit, including artifacts, failures and unresolved review threads. Merge and deploy from a separate approved action.
