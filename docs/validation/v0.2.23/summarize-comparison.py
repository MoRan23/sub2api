"""Summarize raw Go benchmark logs without external packages."""
from collections import defaultdict
from pathlib import Path
import statistics
import sys

results = defaultdict(lambda: defaultdict(lambda: defaultdict(list)))
for directory in sys.argv[1:]:
    replacement = defaultdict(lambda: defaultdict(lambda: defaultdict(list)))
    for path in sorted(Path(directory).glob("*.log")):
        variant = "baseline" if "-baseline-" in path.name else "merged"
        for line in path.read_text(encoding="utf-8").splitlines():
            fields = line.split()
            if not fields or not fields[0].startswith("Benchmark") or "ns/op" not in fields:
                continue
            name = fields[0].removeprefix("Benchmark").removesuffix("-4")
            for i in range(2, len(fields) - 1, 2):
                replacement[name][variant][fields[i + 1]].append(float(fields[i]))
    results.update(replacement)

print("# Three-round comparison / 三轮对比\n")
print("Go 1.27.4; greenteagc,jsonv2; Windows/amd64; GOMAXPROCS=4; CPU affinity 0x55.\n")
print("Median [minimum, maximum]. Time/throughput are local microbenchmarks; retained heap uses one GC-controlled sample per round.\n")
print("| Case | Metric | Baseline | Merged | Median change |")
print("|---|---|---:|---:|---:|")
for name, variants in sorted(results.items()):
    for metric in ("ns/op", "B/op", "allocs/op", "flushes/op", "retained-B/task", "retained/payload"):
        old, new = variants["baseline"].get(metric, []), variants["merged"].get(metric, [])
        if not old or not new:
            continue
        if len(old) != 3 or len(new) != 3:
            raise RuntimeError(f"Incomplete comparison: {name}/{metric}: {len(old)}/{len(new)}")
        def describe(values):
            return f"{statistics.median(values):,.5g} [{min(values):,.5g}, {max(values):,.5g}]"
        before, after = statistics.median(old), statistics.median(new)
        change = f"{(after / before - 1) * 100:+.1f}%" if before else ("0%" if after == 0 else "new")
        print(f"| {name} | {metric} | {describe(old)} | {describe(new)} | {change} |")
