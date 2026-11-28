"""Generate outage bounds from consecutive service probes."""
import csv
import json
import sys
from pathlib import Path


folder = Path(sys.argv[1])
result = {}
for path in sorted(folder.glob("*.csv")):
    if path.name == "phases.csv":
        continue
    rows = [(int(row["monotonic_ns"]), int(row["duration_ns"]), row["outcome"])
            for row in csv.DictReader(path.open())]
    assert len(rows) >= 5 and rows[0][2] == rows[-1][2] == "ok", path
    failures = [index for index, row in enumerate(rows) if row[2] != "ok"]
    longest = 0.0
    for index in failures:
        before = next(j for j in range(index - 1, -1, -1) if rows[j][2] == "ok")
        after = next(j for j in range(index + 1, len(rows)) if rows[j][2] == "ok")
        bound = (rows[after][0] + rows[after][1] - rows[before][0]) / 10**6
        longest = max(longest, bound)
    result[path.stem] = {"samples": len(rows), "failed": len(failures),
                         "max_bracket_ms": round(longest, 2)}

phases = {key: int(value) for key, value in csv.reader((folder / "phases.csv").open())}
for phase in ("upgrade", "rollback"):
    result[phase + "-installer-ms"] = round((phases[phase + "-complete"] - phases[phase + "-start"]) / 10**6, 2)
report = json.dumps(result, indent=2)
(folder / "timings.json").write_text(report + "\n")
print(report)
