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

## Portable V1/V2 results

Windows amd64, same host and TLS fixture, 150 upstream messages per case. The bridge routes both sockets to a fixture loopback address while leaving TLS verification and renderer messages intact. Every cell is V1 / V2. Percentiles include local network and benchmark-driver scheduling. RSS is the process working set after the traffic interval. CPU is averaged over the traffic interval and can exceed 100% across cores.

| Clients | Groups | Frame KiB | P50 ms | P95 ms | P99 ms | Deliveries/s | RSS MiB | CPU % |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1 | 1 | 1.256 / 0.703 | 1.541 / 1.926 | 4.296 / 2.489 | 764.3 / 1090.6 | 32.1 / 11.5 | 63.7 / 45.4 |
| 1 | 1 | 16 | 1.282 / 0.77 | 1.618 / 1.093 | 1.8 / 1.434 | 765.3 / 1196.9 | 32.2 / 16.4 | 71.7 / 137.1 |
| 10 | 1 | 1 | 1.984 / 1.799 | 3.422 / 3.476 | 4.86 / 4.363 | 3676.4 / 4463.4 | 32.8 / 15.6 | 84.3 / 23.2 |
| 10 | 5 | 16 | 1.459 / 0.947 | 1.828 / 1.608 | 2.078 / 2.133 | 1265 / 1777 | 36.0 / 17.3 | 65.9 / 27.8 |
| 30 | 1 | 1 | 3.443 / 6.478 | 5.544 / 12.554 | 5.944 / 16.869 | 5612.4 / 3487.9 | 34.5 / 16.1 | 91.6 / 20.6 |
| 30 | 5 | 16 | 1.706 / 1.54 | 2.568 / 2.135 | 3.488 / 3.394 | 2775.2 / 3014.4 | 37.7 / 17.5 | 72.3 / 31.4 |
| 100 | 1 | 1 | 17.777 / 36.423 | 44.014 / 65.359 | 105.501 / 89.038 | 2948.8 / 2161.2 | 40.3 / 19.9 | 53.8 / 16.2 |
| 100 | 5 | 16 | 5.175 / 3.583 | 12.522 / 6.419 | 14.675 / 10.209 | 2398.1 / 4492.1 | 43.2 / 20.9 | 48.7 / 37.4 |
| 300 | 1 | 1 | 44.072 / 29.164 | 133.339 / 43.864 | 189.259 / 52.257 | 3326 / 8955.6 | 56.6 / 29.6 | 55 / 64.1 |
| 300 | 5 | 16 | 6.254 / 8.789 | 10.903 / 11.462 | 12.004 / 17.263 | 5759.1 / 5721.7 | 59.8 / 32.0 | 88 / 62.6 |

Both runtimes verified every frame and ended with zero active upstream connections. Windows showed a sizeable memory improvement and better 300-client single-group throughput. The 300-client, five-group 16 KiB case had similar throughput and a slower V2 P99. Windows results include variable host scheduling. Use the Linux artifacts before making a per-scenario performance acceptance decision. No soak duration is implied by these burst results.
