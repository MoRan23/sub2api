import json
import argparse
from pathlib import Path
import re
import statistics

base = Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument('--extended', action='store_true')
args = parser.parse_args()
prefix = 'extended-' if args.extended else ''
result = {}
resources = {}
for phase in (['cache-extended', 'sse-multiline-extended'] if args.extended else ['cache', 'sse', 'billing']):
    for label in ['baseline', 'candidate']:
        for number in range(1, 4):
            for line in (base/f'{label}-{phase}-{number}.txt').read_text().splitlines():
                if not line.startswith('Benchmark'):
                    continue
                parts = line.split()
                name = re.sub(r'-4$', '', parts[0])
                metrics = result.setdefault(name, {}).setdefault(label, {})
                for i in range(2, len(parts)-1, 2):
                    metrics.setdefault(parts[i+1], []).append(float(parts[i]))
            text = (base/f'{label}-{phase}-{number}-resources.txt').read_text()
            for key in ['Maximum resident set size (kbytes)', 'User time (seconds)', 'System time (seconds)']:
                match = re.search(re.escape(key)+r':\s*([\d.]+)', text)
                if match:
                    resources.setdefault(phase, {}).setdefault(label, {}).setdefault(key, []).append(float(match[1]))

def stats(values):
    median = statistics.median(values)
    return {'rounds': values, 'median': median, 'spread_percent': 100*(max(values)-min(values))/abs(median) if median else None}

rows = []
for name, labels in result.items():
    for metric in sorted(labels['baseline']):
        a, b = stats(labels['baseline'][metric]), stats(labels['candidate'][metric])
        delta = 100*(b['median']-a['median'])/a['median'] if a['median'] else None
        rows.append({'fixture': name, 'metric': metric, 'baseline': a, 'candidate': b, 'change_percent': delta})
report = {'fixtures': rows, 'process_resources': resources}
(base/(prefix+'performance-summary.json')).write_text(json.dumps(report, indent=2)+'\n')
lines = ['# v0.2.20 merge performance comparison', '',
         'Go 1.27.3; greenteagc,jsonv2; GOMAXPROCS=4; Linux amd64, WSL Ubuntu-24.04. Three alternating baseline/candidate rounds, identical fixtures. Values are round medians; spread is (max-min)/abs(median).', '',
         '| Fixture | Metric | Baseline | Candidate | Change | Baseline spread | Candidate spread |',
         '|---|---|---:|---:|---:|---:|---:|']
for r in rows:
    if r['metric'] not in ['ns/op', 'p95-ns', 'B/op', 'allocs/op', 'decoded-retained-B', 'retained-B/task']:
        continue
    fmt = lambda v: f'{v:.2f}' if v is not None else 'n/a'
    lines.append(f"| {r['fixture'].removeprefix('Benchmark')} | {r['metric']} | {r['baseline']['median']:,.3f} | {r['candidate']['median']:,.3f} | {fmt(r['change_percent'])}% | {fmt(r['baseline']['spread_percent'])}% | {fmt(r['candidate']['spread_percent'])}% |")
lines += ['', '## Per-process resources', '', '| Phase | Metric | Baseline rounds | Candidate rounds |', '|---|---|---|---|']
for phase, labels in resources.items():
    for metric in labels['baseline']:
        lines.append(f"| {phase} | {metric} | {labels['baseline'][metric]} | {labels['candidate'][metric]} |")
(base/(prefix+'performance-summary.md')).write_text('\n'.join(lines)+'\n')
for r in rows:
    if r['metric'] in ['ns/op','p95-ns','decoded-retained-B','retained-B/task']:
        print(r['fixture'].removeprefix('Benchmark'), r['metric'], f"{r['baseline']['median']:.1f} -> {r['candidate']['median']:.1f}", f"{r['change_percent']:+.2f}%")
