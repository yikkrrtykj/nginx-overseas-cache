import argparse
import csv
import json
import math
from pathlib import Path
from statistics import median
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

MAIN = "8eaeb2aa8d8b6552fc5abe7aaf04581678814a88"
TREE = "23e87b4542caf050dfd2fe4dc64049485983bf89"
SCENARIOS = {
    "30c-1g-2h": (30, 1, 7200),
    "100c-5g-1h": (100, 5, 3600),
    "300c-5g-1h": (300, 5, 3600),
}
MIB = 1024 ** 2
COLORS = ("#115eaa", "#c25123", "#0e8162")

def flatten(rows):
    previous = None
    points = []
    for row in rows:
        trace = row["trace"]
        status = trace["Status"]
        proc = trace["OS"]
        q = trace["Queue"]
        current = row["elapsedSeconds"]
        interval = current - previous["elapsed"] if previous else 0
        valid_window = interval > 0
        point = {
            "phase": row["phase"], "elapsed": current,
            "cpu_seconds": proc["CPUSeconds"],
            "cpu_pct": (proc["CPUSeconds"] - previous["cpu_seconds"]) / interval * 100 if valid_window else None,
            "rss_mib": proc["RSSBytes"] / MIB,
            "heap_mib": trace["HeapAlloc"] / MIB, "inuse_mib": trace["HeapInuse"] / MIB,
            "released_heap_mib": trace["HeapReleased"] / MIB,
            "gc_count": trace["GCCount"], "gc_pause_ms": trace["GCPauseNS"] / 1e6,
            "goroutines": trace["Goroutines"], "fds": proc["OpenFDs"], "handles": proc["Handles"],
            "clients": status["localClients"], "upstreams": status["upstreamConnections"],
            "groups": status["activeHubs"], "fixture_active": row["fixtureActive"],
            "global_queue": trace["GlobalQueue"], "queue_budget": trace["QueueBudget"],
            "max_peer_messages": max((p["Messages"] for p in q), default=0),
            "max_peer_bytes": max((p["Bytes"] for p in q), default=0),
            "peer_budget": trace["PeerBudget"], "peer_limit": trace["PeerLimit"],
            "verified": row["verifiedTotal"], "expected": row["expectedTotal"],
            "window_deliveries_per_s": row["verifiedWindow"] / row["windowSeconds"] if row["windowSeconds"] > 0 else None,
            "p50_ms": None, "p95_ms": None, "p99_ms": None,
            "planned_upstream_faults": row["reconnectEvents"], "planned_fault_disconnects": row["expectedFaultDisconnects"],
            "observed_relay_retries": status["reconnects"],
            "slow_disconnects": status["slowDisconnects"], "overflows": status["queueOverflows"],
            "churn": row["churnCycles"], "large_frames": row["largeFrames"],
            "duplicate_upstreams": row["duplicateUpstreams"], "write_failures": trace["WriteFailures"],
            "protocol_errors": trace["ProtocolErrors"], "admission_rejected": trace["AdmissionRejected"],
        }
        if row["latencyP50P95P99Ms"]:
            point["p50_ms"], point["p95_ms"], point["p99_ms"] = row["latencyP50P95P99Ms"]
        points.append(point)
        previous = point
    return points

def epoch_deltas(rows, summary):
    loaded = next((x for x in rows if x["phase"] == "end-loaded"), None)
    if loaded is None:
        return None
    return sum(loaded["fixtureEpochs"].values()) - summary["groups"]

def describe(name, rows, summary, points):
    steady = [p for p in points if p["phase"] == "steady"]
    tail = steady[-180:]
    buckets = [tail[i:i + 60] for i in range(0, len(tail), 60) if len(tail[i:i + 60]) >= 50]
    tail_medians = {key: [round(median(p[key] for p in b), 3) for b in buckets]
                    for key in ("rss_mib", "heap_mib", "goroutines")}
    intervals = [b["elapsed"] - a["elapsed"] for a, b in zip(steady, steady[1:])]
    info = {
        "scenario": name, "rows": len(rows), "steady_samples": len(steady),
        "max_steady_gap_seconds": round(max(intervals, default=0), 2),
        "min_sample_elapsed_seconds": round(min(p["elapsed"] for p in points), 2),
        "max_sample_elapsed_seconds": round(max(p["elapsed"] for p in points), 2),
        "tail_block_medians_oldest_first": tail_medians, "approx_samples_per_tail_block": 60,
        "observed_peaks": {key: max(p[key] for p in points if p[key] is not None)
                           for key in ("rss_mib", "heap_mib", "inuse_mib", "goroutines", "global_queue",
                                       "max_peer_messages", "max_peer_bytes", "gc_count", "overflows",
                                       "write_failures", "protocol_errors", "admission_rejected")},
        "fd_range": [min(p["fds"] for p in points if p["fds"] is not None),
                     max(p["fds"] for p in points if p["fds"] is not None)] if points[0]["fds"] is not None else None,
    }
    if summary is None:
        return info
    n, groups, duration = SCENARIOS[name]
    idle, end, released = summary["idle"], summary["endLoaded"], summary["released"]
    mean_cpu = 100 * (end["OS"]["CPUSeconds"] - idle["OS"]["CPUSeconds"]) / summary["elapsedSeconds"]
    info.update({
        "main": summary["main"], "tree": summary["tree"], "go": summary["go"], "platform": summary["platform"],
        "clients": n, "groups": groups, "target_seconds": duration, "elapsed_seconds": summary["elapsedSeconds"],
        "steady_frames": summary["steadyMessages"], "effective_steady_source_hz": summary["steadyMessages"] / summary["elapsedSeconds"],
        "verified": summary["verifiedDeliveries"], "expected": summary["expectedDeliveries"],
        "checks_per_second": summary["deliveryRatePerSecond"], "p50_p95_p99_ms": summary["latencyP50P95P99Ms"],
        "cpu_percent_including_initial_join": mean_cpu,
        "expected_fault_disconnects": summary["expectedFaultDisconnects"],
        "upstream_faults": summary["reconnectEvents"], "additional_upstream_epochs": epoch_deltas(rows, summary),
        "churn_cycles": summary["churnCycles"], "large_frames": summary["largeFrames"],
        "slow_events": summary["slowEvents"], "clean_exit": summary["cleanExit"],
        "goroutines_idle_loaded_released": [idle["Goroutines"], end["Goroutines"], released["Goroutines"]],
        "rss_mib_idle_loaded_released": [round(x["OS"]["RSSBytes"] / MIB, 3) for x in (idle, end, released)],
        "heap_mib_idle_loaded_released": [round(x["HeapAlloc"] / MIB, 3) for x in (idle, end, released)],
        "fds_idle_loaded_released": [x["OS"]["OpenFDs"] for x in (idle, end, released)],
        "handles_idle_loaded_released": [x["OS"]["Handles"] for x in (idle, end, released)],
        "gc_count": end["GCCount"] - idle["GCCount"],
        "gc_pause_ms": (end["GCPauseNS"] - idle["GCPauseNS"]) / 1e6,
    })
    info["checks"] = {
        "exact_source_baseline": (summary["main"], summary["tree"]) == (MAIN, TREE),
        "required_duration_and_scale": summary["elapsedSeconds"] >= duration and summary["clients"] == n and summary["groups"] == groups,
        "every_expected_delivery_validated": summary["verifiedDeliveries"] == summary["expectedDeliveries"] > n,
        "all_steady_peers_and_groups": bool(steady) and all((p["clients"], p["upstreams"], p["groups"], p["fixture_active"]) ==
                                                          (n, groups, groups, groups) for p in steady),
        "only_expected_upstream_generations": info["additional_upstream_epochs"] == summary["reconnectEvents"] and
                                              all(p["duplicate_upstreams"] == 0 for p in points),
        "reconnect_churn_burst_slow_exercised": min(summary[k] for k in ("reconnectEvents", "churnCycles", "largeFrames", "slowEvents")) > 0,
        "only_expected_slow_evictions": end["Status"]["slowDisconnects"] == summary["slowEvents"] and
                                       end["Status"]["queueOverflows"] == summary["slowEvents"],
        "no_protocol_or_admission_errors": end["ProtocolErrors"] == 0 and end["AdmissionRejected"] == 0,
        "queue_samples_in_budget": all(0 <= p["global_queue"] <= p["queue_budget"] and p["max_peer_bytes"] <= p["peer_budget"]
                                       and p["max_peer_messages"] <= p["peer_limit"] for p in points),
        "detached_and_exited": (released["GlobalQueue"], released["ReadingBudget"], released["Status"]["localClients"],
                               released["Status"]["activeHubs"], released["Status"]["upstreamConnections"]) == (0, 0, 0, 0, 0) and
                               released["Goroutines"] <= idle["Goroutines"] + 2 and summary["cleanExit"],
        "fd_cleanup_on_linux": None if idle["OS"]["OpenFDs"] is None else released["OS"]["OpenFDs"] <= idle["OS"]["OpenFDs"] + 2,
    }
    return info

def graph(graph_data, output, platform):
    fig, axes = plt.subplots(2, 2, figsize=(13, 8.7), layout="constrained")
    fig.patch.set_facecolor("#f3f5f9")
    for ax, metric, title, units in zip(axes.flat,
            ("cpu_pct", "rss_mib", "heap_mib", "goroutines"),
            ("Relay process CPU", "Resident memory", "Go heap allocated", "Relay goroutines"),
            ("% of one logical CPU", "MiB", "MiB", "count")):
        ax.set_facecolor("white")
        for color, (name, points) in zip(COLORS, graph_data.items()):
            selected = [p for p in points if p["phase"] in ("steady", "end-loaded") and p[metric] is not None]
            ax.plot([p["elapsed"] / 60 for p in selected], [p[metric] for p in selected], label=name, color=color, linewidth=1.05)
        ax.set_title(title, loc="left", fontweight="bold", fontsize=12)
        ax.set_xlabel("Workload elapsed (min)")
        ax.set_ylabel(units)
        ax.grid(axis="y", color="#d8dee6", alpha=.7)
        ax.spines[["top", "right"]].set_visible(False)
    axes[0][0].legend(loc="upper right", fontsize=8)
    fig.suptitle("Overseas Cache V2 • isolated WebSocket soak\n" + platform + "  |  actual 10-second resource samples  |  base 8eaeb2aa", fontsize=15)
    fig.savefig(output, dpi=150)
    plt.close(fig)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--preview", action="store_true")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    graph_data = {}
    report = []
    for name in SCENARIOS:
        run_path = args.source / name
        raw = (run_path / "samples.jsonl").read_bytes()
        if args.preview and not raw.endswith(b"\n"):
            raw = raw.rsplit(b"\n", 1)[0]
        rows = [json.loads(line) for line in raw.splitlines() if line]
        summary_path = run_path / "summary.json"
        summary = json.loads(summary_path.read_bytes()) if summary_path.exists() else None
        if summary is None and not args.preview:
            raise ValueError(f"{name} has no completion summary")
        points = flatten(rows)
        graph_data[name] = points
        result = describe(name, rows, summary, points)
        report.append(result)
        with (args.output / f"{name}.csv").open("w", newline="", encoding="utf-8") as stream:
            writer = csv.DictWriter(stream, fieldnames=list(points[0]))
            writer.writeheader()
            writer.writerows(points)
    graph(graph_data, args.output / ("preview-resource-curves.png" if args.preview else "resource-curves.png"), args.label)
    (args.output / "aggregate.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    if not args.preview and any(value is False for row in report for value in row["checks"].values()):
        return 1
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
