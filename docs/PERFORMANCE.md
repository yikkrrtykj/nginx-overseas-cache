# Fanout performance and fault testing

Run the Go fanout benchmark using a real loopback TLS WebSocket origin:

```sh
go test ./internal/fanout -run '^$' -bench '^BenchmarkFanout$' -benchtime=40x -count=3
```

Each scenario checks frame order and payload length for every receiver and verifies one upstream connection. The primary time is one iteration delivering a 1 KiB frame to all clients, including Go test-side TCP reads.

| Clients | Windows amd64 mean ms/iteration (one run) | Upstream | Goroutines including test harness |
|---:|---:|---:|---:|
| 1 | 0.326 | 1 | 9 |
| 10 | 1.350 | 1 | 27 |
| 30 | 2.924 | 1 | 67 |
| 100 | 4.797 | 1 | 207 |
| 300 | 14.223 | 1 | 607 |

Host CPU was Intel Core i7-14650HX. Benchmark observations are local fixture timings. The Go fault fixtures cover sharing and identity separation, ack/replay order, binary frames, slow client isolation, oversize handling, first response deduplication, leader handoff, clearing stale state, reconnection and releasing empty hubs.

The Linux installation workflow runs `tests/compare_fanout.py` using the historical V1 runtime and the packaged Go executable. The harness allocates the fixture TLS address on the runner, verifies resolution before any connection, and compares both processes using the same connection counts (1–300), payloads, groups and message schedule. It captures CPU, RSS at each stage, order/byte integrity and P50/P95/P99 local delivery latency. An unexecuted comparison leaves performance improvement unverified. Slow-client fault testing and small bursts are separate from long soak measurements.
