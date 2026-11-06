#!/usr/bin/env python3
"""Summarize preserved Go benchmark rounds and GNU time resources; no reruns."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import shlex
import shutil
import statistics

LABELS = ("baseline", "candidate")
NUMBER = r"[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?"
BENCHMARK = re.compile(r"^(Benchmark\S+)\s+(\d+)\s+(.*)$")
METRIC = re.compile(rf"({NUMBER})\s+(\S+)")

# These are executed validation records, not commands run by this summarizer.
# Published logs use .txt so the evidence bundle contains only small text files.
VALIDATION = {
    "integration.log": {
        "published": "verification-integration.txt",
        "name": "Repository integration / real PostgreSQL and Redis",
        "command": "CI=true GOMAXPROCS=4 integration-repository.test '-test.run=Test(Scheduler|UsageRecordSnapshotDetached|UsageBillingRepositoryApply|CodexEngineBillingStore|ListSchedulableAccountLoads)' -test.v -test.timeout=15m",
        "environment": "Earlier Linux/amd64 integration binary compiled with -tags=integration; testcontainers PostgreSQL 18.1-alpine3.23 and Redis 8.4-alpine; final rerun is listed separately",
        "success_pattern": r"^PASS$",
    },
    "frontend-final.log": {
        "published": "verification-frontend-build.txt",
        "name": "Frontend locale completeness, TypeScript and production build",
        "command": "pnpm run check:i18n && vue-tsc -b && vite build",
        "environment": "frontend directory; invoked by the package build script",
        "success_pattern": r"built in [0-9.]+s",
    },
    "frontend-components.log": {
        "published": "verification-frontend-components.txt",
        "name": "Memory dashboard component tests",
        "command": "pnpm exec vitest run src/views/admin/ops/components/__tests__/OpsMemory.spec.ts",
        "environment": "frontend directory; FastCtx job j-puxjvd started 2026-11-06T15:32:12Z; Vitest 2.1.9, 1 file and 7 tests passed, tests 439 ms, total 4.00 s; original job output copied after checking for sensitive content",
        "job_command": "pnpm exec vitest run src/views/admin/ops/components/__tests__/OpsMemory.spec.ts && pnpm exec eslint src/api/admin/ops.ts src/views/admin/ops/components/OpsMemory.vue src/views/admin/ops/components/__tests__/OpsMemory.spec.ts src/i18n/locales/en/admin/ops.ts src/i18n/locales/zh/admin/ops.ts && pnpm build > ../.git/task-artifacts/memory-20261106/frontend-final.log 2>&1",
        "started_at": "2026-11-06T15:32:12Z",
        "success_pattern": r"^\s*Tests\s+7 passed \(7\)\s*$",
    },
    "frontend-lint-manifest.json": {
        "published": "verification-frontend-lint.json",
        "name": "Targeted frontend ESLint",
        "command": "pnpm exec eslint src/views/admin/ops/OpsDashboard.vue src/views/admin/ops/components/OpsMemory.vue src/views/admin/ops/components/__tests__/OpsMemory.spec.ts src/api/admin/ops.ts src/i18n/locales/en/admin/ops.ts src/i18n/locales/zh/admin/ops.ts",
        "environment": "frontend directory; FastCtx job j-hhz78g exit 0, no diagnostics; result recorded by the coordinating agent",
        "success_pattern": r'"exit_code":\s*0\s*[,}]',
    },
    "billing-log-trace-race.log": {
        "published": "verification-billing-log-trace-race.txt",
        "name": "Handler billing error request-ID logging race regression",
        "command": "GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage|WrapUsage|UsageRecord)' -count=1 -timeout=5m",
        "environment": "backend directory; after restoring request_id on the WebSocket billing error branch; FastCtx job j-nnqqjs exit 0 confirmed by the coordinating agent",
        "success_pattern": r"^ok\s+github\.com/Wei-Shaw/sub2api/internal/handler\s+[0-9.]+s$",
    },
}
for _phase in ("unit", "race", "build", "integration"):
    VALIDATION[f"final-{_phase}.log"] = {
        "published": f"verification-final-{_phase}.txt",
        "name": f"Final {_phase}",
        "command": f"python backend/benchmarks/verify-memory.py --phase {_phase}",
        "environment": "GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log",
        "success_pattern": r"^exit_code=0$",
    }
VALIDATION["billing-final-log-trace-race.log"] = {
    "published": "verification-billing-final-log-trace-race.txt",
    "name": "Final handler billing error tracing race regression",
    "command": "GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage|WrapUsage|UsageRecord)' -count=1 -timeout=5m",
    "environment": "backend directory; WebSocket error logging restores request_id/model from the owned Result and gateway_request_id/client_request_id from the independent worker context; FastCtx job j-momlwb exit 0 confirmed by the coordinating agent",
    "success_pattern": r"^ok\s+github\.com/Wei-Shaw/sub2api/internal/handler\s+[0-9.]+s$",
}
KNOWN_FAILURES = {
    "baseline-known-grok-failure.log": {
        "published": "verification-baseline-known-grok-failure.txt",
        "test": "TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown/missing_configured_proxy",
        "expected": "GROK_QUOTA_TOKEN_UNAVAILABLE",
        "actual": "ACCOUNT_PROXY_UNAVAILABLE",
        "binary": "baseline-service.test, preserved from 8972274c2871fd961dd5a01d0a4c63b76e358add",
    },
}


def evidence_path(source, published_name):
    path = source / published_name
    for original, record in (VALIDATION | KNOWN_FAILURES).items():
        if record["published"] == published_name and (source / original).exists():
            return source / original
    return path


def collect_validation(source):
    records = []
    for original, spec in VALIDATION.items():
        path = evidence_path(source, spec["published"])
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8-sig")
        if not re.search(spec["success_pattern"], text, re.MULTILINE):
            raise ValueError(f"Validation completion marker missing: {path}")
        if original.startswith("final-") and not text.rstrip().endswith("exit_code=0"):
            raise ValueError(f"Validation log is incomplete: {path}")
        if re.search(r"^exit_code=(?!0$)\d+$", text, re.MULTILINE):
            raise ValueError(f"Validation reports a failed subprocess: {path}")
        records.append({key: value for key, value in spec.items() if key != "success_pattern"} | {"original_file": original, "status": "passed"})
    return records


def collect_known_failures(source):
    records = []
    for original, spec in KNOWN_FAILURES.items():
        path = evidence_path(source, spec["published"])
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8-sig")
        if not re.search(r"^FAIL$", text, re.MULTILINE) or any(spec[key] not in text for key in ("test", "expected", "actual")):
            raise ValueError(f"Known-failure evidence differs from the recorded baseline failure: {path}")
        records.append(spec | {"original_file": original, "status": "pre_existing_baseline_failure"})
    return records


def collect_sharing_canary(source):
    comparison = source / "sharing-canary-comparison.json"
    if not comparison.exists():
        return None, []
    data = json.loads(comparison.read_text(encoding="utf-8-sig"))
    if not str(data.get("note", "")).lower().startswith("concurrent host workload"):
        raise ValueError(f"Sharing canary lacks host-contention marker: {comparison}")
    files = [comparison.name]
    for name in ("sharing-canary-runs.json",
                 "baseline-sharing-canary-50.txt", "baseline-sharing-canary-50-resources.txt",
                 "candidate-sharing-canary-50.txt", "candidate-sharing-canary-50-resources.txt",
                 "baseline-sharing-canary-100.txt", "baseline-sharing-canary-100-resources.txt",
                 "candidate-sharing-canary-100.txt", "candidate-sharing-canary-100-resources.txt"):
        if (source / name).exists():
            files.append(name)
    return data, files


def statistics_for(values):
    middle = statistics.median(values)
    return {
        "rounds": values,
        "median": middle,
        "min": min(values),
        "max": max(values),
        "spread_pct": (max(values) - min(values)) / abs(middle) * 100 if middle else None,
    }


def compare(before, after):
    left, right = statistics_for(before), statistics_for(after)
    return {
        "baseline": left,
        "candidate": right,
        "median_change_pct": (right["median"] / left["median"] - 1) * 100 if left["median"] else None,
    }


def parse_benchmarks(path, phase):
    text = path.read_text(encoding="utf-8-sig")
    if not re.search(r"^PASS$", text, re.MULTILINE):
        raise ValueError(f"Benchmark run did not pass: {path}")
    output, metadata = {}, {}
    for line in text.splitlines():
        for field in ("goos", "goarch", "pkg", "cpu"):
            if line.startswith(field + ":"):
                metadata[field] = line.split(":", 1)[1].strip()
        match = BENCHMARK.match(line)
        if not match:
            continue
        raw_name, iterations, rest = match.groups()
        concurrency = re.search(r"-(\d+)$", raw_name)
        if concurrency:
            current = int(concurrency.group(1))
            if metadata.setdefault("benchmark_gomaxprocs", current) != current:
                raise ValueError(f"Benchmark GOMAXPROCS differs within {path}")
        name = re.sub(r"-\d+$", "", raw_name)
        if name in output:
            raise ValueError(f"Repeated benchmark {name} in {path}; use one run per file")
        metrics = {unit: float(number) for number, unit in METRIC.findall(rest)}
        if "ns/op" not in metrics or metrics["ns/op"] <= 0:
            raise ValueError(f"Missing positive ns/op: {path}: {name}")
        # Retention fixtures include payload construction, explicit GC and queue
        # shutdown. Their reciprocal timing is experiment rate, not billing TPS.
        rate_unit = "experiments/s" if phase == "billing" else "ops/s"
        metrics[rate_unit] = 1_000_000_000 / metrics["ns/op"]
        if "bucket-reads/op" in metrics:
            metrics["bucket-reads/s"] = metrics["bucket-reads/op"] * metrics[rate_unit]
        if "account-views/op" in metrics:
            metrics["account-views/s"] = metrics["account-views/op"] * metrics[rate_unit]
        output[name] = {"iterations": int(iterations), "metrics": metrics}
    if not output:
        raise ValueError(f"No Go benchmark records in {path}")
    return output, metadata


def parse_resources(path):
    text = path.read_text(encoding="utf-8-sig")
    def numeric(label):
        match = re.search(rf"^\s*{re.escape(label)}:\s*({NUMBER})\s*$", text, re.MULTILINE)
        if not match:
            raise ValueError(f"Missing {label} in {path}")
        return float(match.group(1))
    status = numeric("Exit status")
    if status != 0:
        raise ValueError(f"Resource capture reports exit status {status}: {path}")
    wall = re.search(r"Elapsed \(wall clock\) time \(h:mm:ss or m:ss\):\s*(\S+)", text)
    if not wall:
        raise ValueError(f"Missing elapsed time in {path}")
    wall_seconds = 0.0
    for component in wall.group(1).split(":"):
        wall_seconds = wall_seconds * 60 + float(component)
    user, system = numeric("User time (seconds)"), numeric("System time (seconds)")
    return {
        "max_rss_KiB": numeric("Maximum resident set size (kbytes)"),
        "user_CPU_s": user, "system_CPU_s": system,
        "user_plus_system_CPU_s": user + system, "wall_s": wall_seconds,
    }


def validate_manifest_settings(manifests):
    expected = {}
    for name, manifest in manifests.items():
        if not name.endswith("manifest") or not isinstance(manifest, dict):
            continue
        for setting in ("go", "goexperiment", "gomaxprocs"):
            if setting not in manifest:
                continue
            value = manifest[setting]
            if setting in expected and expected[setting] != value:
                raise ValueError(f"Manifest {name} differs for {setting}: {value!r} versus {expected[setting]!r}")
            expected[setting] = value


def aggregate(source, phases, rounds, candidate_labels=None, phase_tags=None):
    candidate_labels = candidate_labels or {}
    phase_tags = phase_tags or {}
    bench_runs, resource_runs, files, metadata = {}, {}, [], {}
    for label in LABELS:
        bench_runs[label], resource_runs[label] = {}, {}
        for phase in phases:
            bench_runs[label][phase], resource_runs[label][phase] = [], []
            file_label = candidate_labels.get(phase, label) if label == "candidate" else label
            tag_suffix = "-" + phase_tags[phase] if phase_tags.get(phase) else ""
            for number in range(1, rounds + 1):
                benchmark_path = source / f"{file_label}-{phase}-{number}{tag_suffix}.txt"
                resource_path = source / f"{file_label}-{phase}-{number}{tag_suffix}-resources.txt"
                benchmarks, run_metadata = parse_benchmarks(benchmark_path, phase)
                bench_runs[label][phase].append(benchmarks)
                resource_runs[label][phase].append(parse_resources(resource_path))
                metadata.setdefault(phase, run_metadata)
                if metadata[phase] != run_metadata:
                    raise ValueError(f"Benchmark host/package metadata changed: {benchmark_path}")
                files.extend([benchmark_path.name, resource_path.name])
    manifests = {}
    for label in LABELS:
        for suffix in ("manifest", "runs"):
            path = source / f"{label}-all-{suffix}.json"
            if path.exists():
                manifests[f"{label}_{suffix}"] = json.loads(path.read_text(encoding="utf-8-sig"))
                files.append(path.name)
    for label in LABELS:
        for phase in phases:
            file_label = candidate_labels.get(phase, label) if label == "candidate" else label
            tag_suffix = "-" + phase_tags[phase] if phase_tags.get(phase) else ""
            for suffix in ("manifest", "runs"):
                path = source / f"{file_label}-{phase}-{suffix}{tag_suffix}.json"
                if path.exists():
                    manifests[f"{label}_{phase}_{suffix}"] = json.loads(path.read_text(encoding="utf-8-sig"))
                    files.append(path.name)
    for phase in phases:
        for suffix in ("manifest", "runs"):
            path = source / f"{phase}-{suffix}.json"
            if path.exists():
                manifests[f"{phase}_{suffix}"] = json.loads(path.read_text(encoding="utf-8-sig"))
                files.append(path.name)
    validate_manifest_settings(manifests)
    rows, resources = [], []
    for phase in phases:
        reference = bench_runs["baseline"][phase][0]
        for label in LABELS:
            for run in bench_runs[label][phase]:
                if run.keys() != reference.keys():
                    raise ValueError(f"Benchmark fixture set differs in {label}/{phase}")
        for name in reference:
            units = reference[name]["metrics"].keys()
            iterations = reference[name]["iterations"]
            for label in LABELS:
                for run in bench_runs[label][phase]:
                    if run[name]["metrics"].keys() != units or (phase != "http-lifecycle" and run[name]["iterations"] != iterations):
                        raise ValueError(f"Benchmark metrics/iterations differ for {label}/{name}")
                    if phase == "http-lifecycle":
                        for count in ("clients", "workers", "auth-storage-reads"):
                            if run[name]["metrics"][count] != reference[name]["metrics"][count]:
                                raise ValueError(f"HTTP workload differs for {count}: {label}/{name}")
            rows.append({
                "phase": phase, "benchmark": name, "iterations_per_round": "adaptive" if phase == "http-lifecycle" else iterations,
                "iterations": {label: [run[name]["iterations"] for run in bench_runs[label][phase]] for label in LABELS},
                "metrics": {unit: compare(
                    [run[name]["metrics"][unit] for run in bench_runs["baseline"][phase]],
                    [run[name]["metrics"][unit] for run in bench_runs["candidate"][phase]],
                ) for unit in units},
            })
        resources.append({
            "phase": phase,
            "metrics": {unit: compare(
                [run[unit] for run in resource_runs["baseline"][phase]],
                [run[unit] for run in resource_runs["candidate"][phase]],
            ) for unit in resource_runs["baseline"][phase][0]},
        })
    return {
        "schema_version": 1, "round_count": rounds, "phases": phases,
        "candidate_labels": candidate_labels,
        "phase_tags": phase_tags,
        "spread_definition": "100 * (maximum - minimum) / abs(median) across rounds",
        "change_definition": "100 * (candidate median / baseline median - 1)",
        "throughput_definition": "Per-round 1e9 / ns/op; median of these reciprocals. Cache ops are candidate-set reads; sharing ops are 100-bucket sweeps, with bucket-reads/s and account-views/s derived from fixture counts. SSE ops are complete fixture streams. Billing experiments/s is GC-inclusive fixture rate, not billing throughput.",
        "resource_scope": "GNU time resources cover the complete phase process, including all sub-benchmark setup, warmup, explicit GC and teardown; RSS is process maximum, not retained Go heap. kbytes are KiB on this Linux host.",
        "metadata": metadata, "manifests": manifests,
        "source_files": sorted(files), "benchmarks": rows, "resources": resources,
    }


def aggregate_lifecycle(source, rounds, prefix="candidate-lifecycle"):
    runs, resources, files, metadata = [], [], [], None
    for index in range(1, rounds + 1):
        path = source / f"{prefix}-{index}.txt"
        resource = source / f"{prefix}-{index}-resources.txt"
        records, run_metadata = parse_benchmarks(path, "lifecycle")
        if metadata is not None and metadata != run_metadata:
            raise ValueError(f"Lifecycle host/package/concurrency metadata changed: {path}")
        metadata = run_metadata
        runs.append(records)
        resources.append(parse_resources(resource))
        files.extend([path.name, resource.name])
    reference = runs[0]
    if any(run.keys() != reference.keys() for run in runs):
        raise ValueError("Lifecycle fixture set differs across rounds")
    output = []
    for name in reference:
        if not name.endswith("/legacy_capture"):
            continue
        other = name.removesuffix("/legacy_capture") + "/owned_snapshot"
        if other not in reference:
            raise ValueError(f"Missing lifecycle pair for {name}")
        units = reference[name]["metrics"].keys()
        for run in runs:
            for mode in (name, other):
                if run[mode]["metrics"].keys() != units:
                    raise ValueError(f"Lifecycle metric set differs: {mode}")
            for count in ("producers", "workers"):
                expected = reference[name]["metrics"][count]
                if run[name]["metrics"][count] != expected or run[other]["metrics"][count] != expected:
                    raise ValueError(f"Lifecycle parallelism differs: {count}")
        output.append({
            "fixture": name.split("/", 1)[1].removesuffix("/legacy_capture"),
            "legacy_benchmark": name, "owned_benchmark": other,
            "iterations": {
                "legacy_capture": [run[name]["iterations"] for run in runs],
                "owned_snapshot": [run[other]["iterations"] for run in runs],
            },
            "metrics": {unit: compare(
                [run[name]["metrics"][unit] for run in runs],
                [run[other]["metrics"][unit] for run in runs],
            ) for unit in units},
        })
    if not output or len(output) * 2 != len(reference):
        raise ValueError("Unexpected unpaired lifecycle fixture")
    manifests = {}
    for suffix in ("manifest", "runs"):
        path = source / f"{prefix}-{suffix}.json"
        if path.exists():
            manifests[suffix] = json.loads(path.read_text(encoding="utf-8-sig"))
            files.append(path.name)
    return {
        "scope": "Same candidate binary, legacy_capture versus owned_snapshot modes; baseline/candidate keys in metric records mean these two modes, not commits. No timed forced GC or simulated external latency. Adaptive benchmark iteration counts may differ. Parallel ns/op measures amortized throughput; p95-e2e-ns measures the complete local request lifecycle. Combined process resources cover both modes.",
        "benchmarks": output,
        "source_prefix": prefix,
        "metadata": metadata,
        "manifests": manifests,
        "resources": {unit: statistics_for([run[unit] for run in resources]) for unit in resources[0]},
    }, files


def aggregate_http_lifecycle(source, rounds, candidate_label="candidate", tag=""):
    phases = ["http-lifecycle"]
    # The ordinary parser enforces per-round metric sets and host metadata. The
    # HTTP fixture has adaptive benchmark N, so its parser path permits that
    # while validating fixed clients/workers/storage-read counts in aggregate.
    result = aggregate(source, phases, rounds, {"http-lifecycle": candidate_label}, {"http-lifecycle": tag})
    suffix = "-" + tag if tag else ""
    run_path = source / f"http-lifecycle-run-runs{suffix}.json"
    if run_path.exists():
        result["manifests"]["http_paired_runs"] = json.loads(run_path.read_text(encoding="utf-8-sig"))
        result["source_files"].append(run_path.name)
    return result


def number(value):
    if value is None:
        return "n/a"
    if abs(value) >= 1000:
        return f"{value:,.0f}"
    if value == int(value):
        return str(int(value))
    return f"{value:.4g}"


def change(metric):
    value = metric["median_change_pct"]
    return "n/a" if value is None else f"{value:+.2f}%"


def rounds_cell(stats, divisor=1):
    values = " / ".join(format(value / divisor, ".12g") for value in stats["rounds"])
    spread = stats["spread_pct"]
    spread_text = "n/a" if spread is None else f"{spread:.2f}%"
    return f"{values}; **{number(stats['median'] / divisor)}**; {spread_text}"


def lifecycle_markdown(lifecycle, title):
    output = ["", f"## {title}", "", lifecycle["scope"], "", f"Input prefix: `{lifecycle['source_prefix']}`.", ""]
    if lifecycle.get("note"):
        output += [lifecycle["note"], ""]
    for row in lifecycle["benchmarks"]:
        output += [f"### {row['fixture']}", "",
                   "| Metric | Legacy capture rounds; median; spread | Owned snapshot rounds; median; spread | Median change |", "|---|---|---|---:|"]
        for unit, metric in row["metrics"].items():
            output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
        output += [""]
    output += ["### Combined lifecycle process resources", "", "Each process runs both ownership modes; resources are not attributable to either individual mode.", "",
               "| Metric | Rounds; median; spread |", "|---|---|"]
    for unit, metric in lifecycle["resources"].items():
        output.append(f"| {unit} | {rounds_cell(metric)} |")
    return output


def write_full_markdown(summary, path):
    output = [
        "# Memory performance: preserved round summary", "",
        f"{summary['round_count']} rounds per phase and revision. Each value cell is `round 1 / round 2 / round 3; median; spread`.",
        "Spread = `(max - min) / |median| × 100%`; change = `(candidate median / baseline median - 1) × 100%`. Three rounds are descriptive evidence, without a significance test.",
        "", "Cache ops/s means complete candidate-set reads; sharing ops/s means 100-bucket sweeps; bucket-reads/s and account-views/s use the reported per-op counts. SSE ops/s means complete fixture streams. Billing experiments/s and ns/op include payload creation, explicit GC, queue lifecycle and teardown; they do not measure business latency or billing TPS.",
        "", "The in-round p95 metric is retained separately; its three-round median is not a pooled p95. Cache/SSE/sharing fixtures use nearest-rank p95 over up to 1,024 sampled operations. Lifecycle fixtures sort every request duration and select index floor(0.95 × (N−1)).",
    ]
    for phase, note in summary.get("phase_notes", {}).items():
        output += ["", f"Measurement conditions for `{phase}`: {note}"]
    for phase, label in summary.get("candidate_labels", {}).items():
        output += ["", f"Selected `{phase}` inputs: `baseline-{phase}` versus `{label}-{phase}`."]
    for row in summary["benchmarks"]:
        output.extend(["", f"## {row['benchmark']}", "", f"Phase: {row['phase']}; iterations per round: {row['iterations_per_round']}.", "",
                       "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"])
        for unit, metric in row["metrics"].items():
            output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
    output.extend(["", "## Whole-process resources", "", summary["resource_scope"]])
    for row in summary["resources"]:
        output.extend(["", f"### {row['phase']}", "", "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"])
        for unit, metric in row["metrics"].items():
            output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
    lifecycle = summary.get("lifecycle")
    if lifecycle:
        output += lifecycle_markdown(lifecycle, "Selected lifecycle ownership comparison")
    if summary.get("authenticated_lifecycle"):
        output += lifecycle_markdown(summary["authenticated_lifecycle"], "Authenticated lifecycle microbenchmark")
    for historical in summary.get("lifecycle_history", []):
        output += lifecycle_markdown(historical, f"Historical lifecycle diagnostic: {historical['source_prefix']}")
    for historical in summary.get("phase_history", []):
        phase = historical["phases"][0]
        label = historical["candidate_labels"][phase]
        output += ["", f"## Historical phase diagnostic: {label}-{phase}", "", "Kept separate from the selected candidate; baseline/candidate labels refer to this historical comparison only."]
        for row in historical["benchmarks"]:
            output += ["", f"### {row['benchmark']}", "", "| Metric | Baseline rounds; median; spread | Historical candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
            for unit, metric in row["metrics"].items():
                output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
        output += ["", "### Historical phase process resources", "", "| Metric | Baseline rounds; median; spread | Historical candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
        for unit, metric in historical["resources"][0]["metrics"].items():
            output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
    if summary.get("http_lifecycle"):
        http = summary["http_lifecycle"]
        output += ["", "## HTTP billing lifecycle", "", "This is a separate HTTP client/server/upstream loop. Each of 16 clients waits for its settled UsageLog.Create result before sending another request; it must not be combined with the fallback or authenticated service microbenchmarks. GNU time resources cover both payload sizes and process setup.", ""]
        for row in http["benchmarks"]:
            output += [f"### {row['benchmark']}", "", "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
            for unit in ("http-requests/s", "settled-requests/s", "p95-http-ns", "p95-settled-ns", "runtime-cpu-ns/request", "B/op", "allocs/op"):
                metric = row["metrics"][unit]
                output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
        output += ["", "HTTP resources", "", "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
        for unit, metric in http["resources"][0]["metrics"].items():
            output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
        for history in summary.get("http_history", []):
            tag = history["phase_tags"]["http-lifecycle"] or "original (2s)"
            output += ["", f"## Historical HTTP batch: {tag}", "", "Retained separately; no pooled samples or combined resource medians."]
            for row in history["benchmarks"]:
                output += ["", f"### {row['benchmark']}", "", "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
                for unit, metric in row["metrics"].items():
                    output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
            output += ["", "### Historical HTTP process resources", "", "| Metric | Baseline rounds; median; spread | Candidate rounds; median; spread | Median change |", "|---|---|---|---:|"]
            for unit, metric in history["resources"][0]["metrics"].items():
                output.append(f"| {unit} | {rounds_cell(metric['baseline'])} | {rounds_cell(metric['candidate'])} | {change(metric)} |")
    canary = summary.get("sharing_canary")
    if canary:
        output += ["", "## Host-contended sharing canary (diagnostic only)", "", canary["note"],
                   "This one-run canary is retained as diagnostic context and is not pooled with the three-round exclusive 0%-sharing retest.",
                   "", "| Fixture | Time change | p95 change | Allocation bytes | Allocations |", "|---|---:|---:|---:|---:|"]
        for fixture, metrics in canary["delta_percent"].items():
            output.append(f"| {fixture} | {metrics['ns/op']:+.3f}% | {metrics['p95-sweep-ns']:+.3f}% | {metrics['B/op']:+.3f}% | {metrics['allocs/op']:+.3f}% |")
    output += ["", "## Binary and checkout provenance", "", "Manifest `source_head` in the original runs records the checkout at measurement time. Later manifests separate `binary_source` from `measurement_checkout_head`. Preserved binaries may predate later changes; these fields do not identify a final release commit.", ""]
    for name, manifest in summary["manifests"].items():
        if name.endswith("manifest"):
            output.append(f"- `{name}`: binary source = `{manifest.get('binary_source', 'not recorded by original runner; preserved binary')}`; measurement checkout = `{manifest.get('measurement_checkout_head', manifest.get('source_head', 'not recorded'))}`.")
    output.extend(["", "## Raw inputs", ""] + [f"- [{name}]({name})" for name in summary["source_files"]])
    path.write_text("\n".join(output) + "\n", encoding="utf-8")


def report_table(summary, phase, columns, zh=False):
    output = ["| Fixture | " + " | ".join(label for _, label, _ in columns) + " |", "|---|" + "---:|" * len(columns)]
    for row in summary["benchmarks"]:
        if row["phase"] != phase:
            continue
        name = row["benchmark"].split("/", 1)[1]
        values = []
        for unit, _, divisor in columns:
            metric = row["metrics"][unit]
            before = metric["baseline"]["median"] / divisor
            after = metric["candidate"]["median"] / divisor
            values.append(f"{number(before)} → {number(after)} ({change(metric)})")
        output.append("| " + name + " | " + " | ".join(values) + " |")
    return output


def operational_sections(summary, raw, zh):
    evidence_names = {record["original_file"] for record in summary.get("verification", [])}
    has_integration = bool(evidence_names & {"integration.log", "final-integration.log"})
    has_frontend = "frontend-final.log" in evidence_names
    if zh:
        output = [
            "", "## 已执行验证", "",
            "验证记录与性能二进制分开保存。" + ("集成覆盖共享缓存跨实例权威与 Redis 重连、分组退休/重开 epoch、快照/outbox、调度投影、Engine 原子结算恢复与并发、普通计费去重及脱离请求生命周期后的结算。日志中的 Redis 重连故障和 cache-not-ready 是对应测试主动注入的场景。" if has_integration else "以下仅列出证据目录中已完成的验证日志。"),
            "", "| 范围 | 实际命令 | 环境与记录 |", "|---|---|---|",
        ]
    else:
        output = [
            "", "## Executed verification", "",
            "Validation logs are separate from the performance binaries. " + ("Integration coverage includes shared-cache cross-instance authority and Redis reconnect, group retirement/reopen epochs, snapshots/outbox, scheduling projections, Engine atomic settlement recovery and concurrency, ordinary billing deduplication and detached request-lifetime settlement. Redis reconnect and cache-not-ready messages are deliberately injected test scenarios." if has_integration else "Only completed validation logs present in the evidence directory are listed below."),
            "", "| Scope | Executed command | Environment and evidence |", "|---|---|---|",
        ]
    for record in summary.get("verification", []):
        command = record["command"].replace("|", "&#124;")
        output.append(f"| {record['name']} | <code>{command}</code> | {record['environment']}; [PASS]({raw}/{record['published']}) |")
    if "final-integration.log" in evidence_names:
        output += ["", "最终集成另包含真实 PostgreSQL 用户行锁等待：确认 worker 阻塞后取消父请求、清空原输入，再解锁并验证一次扣费、缓存同步、预留释放与重复幂等。" if zh else "Final integration also includes a real PostgreSQL user-row lock: after confirming that the worker waits on the lock, the test cancels the parent request and clears the original inputs, releases the lock, then verifies one debit, cache synchronization, reservation release and idempotent replay."]
    if "billing-final-log-trace-race.log" in evidence_names:
        output += ["", "完整 unit/race/build/integration 验证后，最后补齐了 WebSocket 计费错误日志字段：从已独立持有的 Result 读取 request_id/model，从独立 worker context 读取 gateway_request_id/client_request_id。成功请求路径保持原样；随后用上表同一 handler race 命令通过定向回归（6.514 秒）。两份日志追踪回归记录均保留。" if zh else "After the full unit/race/build/integration checks, the final change restored WebSocket billing error log fields: request_id/model from the owned Result and gateway_request_id/client_request_id from the independent worker context. The successful-request path is unchanged. The same targeted handler race command then passed in 6.514 seconds; both logging regression records are retained."]
    for failure in summary.get("known_failures", []):
        output += ["", (f"既有失败单列：保存自 `8972274c2` 的 baseline 二进制复现 `{failure['test']}`；断言期待 `{failure['expected']}`，实际为 `{failure['actual']}`。本次未弱化该断言，保留[失败日志]({raw}/{failure['published']})。" if zh else f"Separately recorded existing failure: the baseline binary preserved from `8972274c2` reproduces `{failure['test']}`. The assertion expects `{failure['expected']}` and receives `{failure['actual']}`. The assertion remains unchanged; see the [failure log]({raw}/{failure['published']}).")]
    if zh:
        output += [
            "", "前端 build 运行 i18n 完整性检查、TypeScript 检查与 Vite 生产构建；日志保留 Browserslist 数据过期、动态/静态混合导入和 bundle 大小提示。该命令没有包含 lint。" if has_frontend else "",
            "", "## 升级、观测与回退", "",
            "1. 保存当前后端/前端发布产物与配置，先在一个实例更新并按正常关闭流程排空普通计费队列。本次内存优化没有新增数据库迁移或修改共享 Redis 快照格式。进程启动后本地解码缓存为空。",
            "2. `gateway.scheduling.snapshot_cache_max_bytes` 是每进程母本与 bucket 索引的软预算，默认 64 MiB；值为 0 时使用默认值。沿用当前限流、账单队列容量及溢出策略；按相同流量比较命中率、估算字节、淘汰与延迟。缓存复用最长 10 秒，空闲淘汰为 100 秒，清理周期 10 秒。",
            "3. 管理员在运维页面打开内存观测，并保持已有运维监控开关启用。选择当前实例 ID，观察 RSS、HeapAlloc/Inuse/Idle/Released、GC、goroutine、缓存、active body/SSE 与队列指标。每 10 秒采样，单实例保留 360 点（约 1 小时），30 秒未见采样标为 stale。用相同实例、负载和观察窗口比较；进程重启会生成新 ID 并重置累计计数。",
            "4. 流结束或请求退出后，`active_sse_buffers`/`active_sse_buffer_bytes` 与 active body 指标应随实际活跃请求回落；队列 waiting/running 应随负载排空。关注 dropped、sync_fallback、等待/执行直方图与结算失败增量。完成观察后逐实例扩大部署。",
            "5. 需要 heap profile 时，在所选实例对应的后端连接上启用下载，再下载分析，完成后关闭。管理 API 为 `/api/v1/admin/ops/runtime/memory` 和 `/api/v1/admin/ops/runtime/heap-profile/{enable,disable,download}`。启用/禁用 JSON 与下载 query 都需 `instance_id`；仅当前响应进程可操作，跨进程请求返回 409。下载默认关闭，启用 30 分钟后过期，每进程只允许一个下载；它直接写出已有采样，不强制 GC、不修改 MemProfileRate、不落临时文件。",
            "6. 回退时先停止向目标实例分配新流量，按正常关闭流程等待活动流和计费任务完成，再恢复保存的后端/前端产物与配置。本地缓存随进程重启清空；共享 Redis 快照继续复用，观测数据按 TTL 自然过期，无需清空 Redis。",
            "", "队列语义：`completed`、`failed` 统计 worker 闭包执行，`submitted`、`dropped`、`sync_fallback` 反映调度行为。普通 `RecordUsage` 调用的返回结果单独计入 `settlement_succeeded`/`settlement_failed`，幂等去重返回成功也归成功；Engine 普通计费 no-op 被排除，其 durable ledger 结算另行观察。闭包正常返回可能仍有结算错误，两组计数需要一起看。队列等待直方图覆盖排队任务，执行直方图还包含同步回退。",
            "", "认证 L1 的每个发布条目另持有一份独立的 billing-only Group 价格投影，队列任务复用该冻结版本；普通非认证 Group 仍逐次深复制。价格投影不引用认证 Group 的地图或价格指针；L2 JSON 格式保持原样，解码后重建投影。改价沿现有 L1/L2 失效、PubSub 与 TTL 发布新版本，已排队任务保留原版本。这部分常驻内存按 `api_key_auth_cache.l1_size` 条目数约束，不包含在 `scheduler_accounts`/`scheduler_cache_total` 的估算或调度缓存 64 MiB 预算内。",
            "", "## 适用范围与尚未覆盖的场景", "",
            "性能数据来自上述固定合成 fixture。缓存 benchmark 使用 miniredis；SSE writer 丢弃输出，首写是本地 writer 返回时间；生命周期组只执行本地价格计算和扣费命令构造。真实 PostgreSQL/Redis 用于独立正确性集成测试；真实模型、外部网络、TLS、数据库吞吐和长时间生产流量未参与这些计时。",
            "", "缓存软预算以对象估算字节计数，与实际 HeapAlloc/RSS 的口径不同，正在使用的调用方视图可继续持有已淘汰母本。10 秒快照复用上限保留了未推进 revision 的写入可见窗口。最初共享比例组每轮仅 3 次 sweep，复测必须保留独立样本数与二进制来源。",
            "", "运行观测仅保存数值快照；Redis 保存期 70 分钟，最多登记 128 个进程，达到上限显示 truncated。Redis 不可用时当前实例仍返回进程内历史。heap profile 属于单个进程的采样堆视图，应按现有管理员访问权限管理下载文件。",
        ]
    else:
        output += [
            "", "The frontend build ran locale completeness, TypeScript and Vite production build checks. Its log retains stale Browserslist data, mixed dynamic/static import and bundle-size notices. Lint was not part of this command." if has_frontend else "",
            "", "## Upgrade, observe and roll back", "",
            "1. Preserve the current backend/frontend release artifacts and configuration. Update one instance first and drain ordinary billing tasks through the normal shutdown flow. These memory changes add no database migration and leave the shared Redis snapshot format unchanged. The decoded process cache starts empty.",
            "2. `gateway.scheduling.snapshot_cache_max_bytes` is the per-process soft budget for account masters and bucket indexes: 64 MiB by default, with 0 retaining that default. Keep the existing admission, billing queue and overflow settings; compare cache hits, estimated bytes, evictions and latency at the same load. Snapshot reuse is bounded to 10 seconds, idle eviction to 100 seconds, with cleanup every 10 seconds.",
            "3. As an administrator, open memory observation in Operations with the existing monitoring switch enabled. Select the current instance ID and observe RSS, HeapAlloc/Inuse/Idle/Released, GC, goroutines, caches, active bodies/SSE and queue metrics. Samples arrive every 10 seconds; each instance keeps 360 samples (about one hour), and becomes stale after 30 seconds without a sample. Compare the same instance, workload and window; process restart creates a new ID and resets cumulative counters.",
            "4. After streams or requests finish, `active_sse_buffers`/`active_sse_buffer_bytes` and active-body gauges should fall with active work; queue waiting/running should drain with load. Inspect dropped tasks, synchronous fallbacks, wait/execution histograms and settlement-failure deltas before extending the rollout.",
            "5. For a heap profile, connect to the backend serving the selected instance, enable download, download it for analysis, then disable access. Admin APIs are `/api/v1/admin/ops/runtime/memory` and `/api/v1/admin/ops/runtime/heap-profile/{enable,disable,download}`. Enable/disable JSON and the download query require `instance_id`; only the responding process accepts the operation, with 409 on a process mismatch. Downloads start disabled, expire after 30 minutes and allow one download per process. They stream the existing sampled profile without forcing GC, changing MemProfileRate or creating temporary files.",
            "6. To roll back, stop routing new traffic to the instance, use normal shutdown to finish active streams and billing work, then restore the preserved backend/frontend artifacts and configuration. Restart clears the local cache. Shared Redis snapshots remain usable and observation records expire by TTL; no Redis flush is needed.",
            "", "Queue semantics: `completed` and `failed` describe worker closure execution; `submitted`, `dropped` and `sync_fallback` describe scheduling. Ordinary `RecordUsage` return outcomes increment `settlement_succeeded`/`settlement_failed` separately, including successful idempotent deduplication. Engine ordinary-billing no-ops are excluded; its durable ledger is observed separately. A closure can return normally after a settlement error, so inspect both groups. The wait histogram covers queued tasks; the execution histogram also covers synchronous fallbacks.",
            "", "Each published authentication L1 entry additionally owns an independent billing-only Group price projection, which queued tasks reuse. Group objects outside an authentication-cache publication keep the per-submission deep-copy path. The projection retains none of the authentication Group's maps or price pointers. L2 JSON stays unchanged and reconstructs the projection after decoding. Price changes publish a new version through existing L1/L2 invalidation, PubSub and TTL behavior; already queued work keeps its prior version. This residency is bounded by the `api_key_auth_cache.l1_size` entry limit and is outside `scheduler_accounts`/`scheduler_cache_total` estimates and the scheduler's 64 MiB budget.",
            "", "## Scope and remaining coverage", "",
            "Performance measurements use the fixed synthetic fixtures above. Cache benchmarks use miniredis; SSE writers discard output and first-write time ends at the local writer return; lifecycle fixtures perform local price calculation and billing-command construction. Real PostgreSQL/Redis are used by the separate correctness integration tests. Real models, external networks, TLS, database throughput and sustained production traffic are outside these timed fixtures.",
            "", "The cache soft budget uses estimated object bytes, a different quantity from HeapAlloc or RSS. Active caller views may retain an evicted master. The 10-second snapshot reuse limit preserves the visibility window for writes that do not advance revisions. Initial sharing-ratio runs have only three sweeps per round; follow-up measurements retain separate sample counts and binary provenance.",
            "", "Runtime observation stores numeric snapshots. Redis records expire after 70 minutes and register up to 128 processes, showing truncated when capacity is reached. The current instance retains local history if Redis is unavailable. Heap profiles represent one process's sampled heap; handle downloaded files under the existing administrator access policy.",
        ]
    return output


def write_report(summary, path, zh):
    raw = "../backend/benchmarks/results/memory-20261106"
    def lookup(phase, suffix, unit):
        return next(row["metrics"][unit] for row in summary["benchmarks"]
                    if row["phase"] == phase and row["benchmark"].endswith(suffix))
    cache_one = lookup("cache", "accounts_2000_groups_1", "decoded-retained-B")["median_change_pct"]
    cache_many = lookup("cache", "accounts_2000_groups_100", "decoded-retained-B")["median_change_pct"]
    ws_first = lookup("sse", "WS/small_256", "first-write-ns/op")
    http_multiline = lookup("sse", "HTTP/multiline_128", "first-write-ns/op")
    ws_multiline = lookup("sse", "WS/multiline_128", "first-write-ns/op")
    billing_resource = next(row["metrics"] for row in summary["resources"] if row["phase"] == "billing")
    reproduce = "python backend/benchmarks/summarize-memory.py --source backend/benchmarks/results/memory-20261106 --output-dir .git/task-artifacts/memory-20261106 --phases " + " ".join(summary["phases"])
    if "lifecycle" in summary:
        reproduce += " --include-lifecycle"
        if summary["lifecycle"]["source_prefix"] != "candidate-lifecycle":
            reproduce += " --lifecycle-prefix " + shlex.quote(summary["lifecycle"]["source_prefix"])
    if "authenticated_lifecycle" in summary:
        reproduce += " --authenticated-prefix " + shlex.quote(summary["authenticated_lifecycle"]["source_prefix"])
    if "http_lifecycle" in summary:
        reproduce += " --http-prefix " + shlex.quote(summary["http_lifecycle"]["candidate_labels"]["http-lifecycle"])
        if summary["http_lifecycle"]["phase_tags"]["http-lifecycle"]:
            reproduce += " --http-tag " + shlex.quote(summary["http_lifecycle"]["phase_tags"]["http-lifecycle"])
        for history in summary.get("http_history", []):
            reproduce += " --http-history-tag " + shlex.quote(history["phase_tags"]["http-lifecycle"] or "original")
    for historical in summary.get("lifecycle_history", []):
        reproduce += " --lifecycle-history-prefix " + shlex.quote(historical["source_prefix"])
    for group in ([summary["lifecycle"]] if "lifecycle" in summary else []) + summary.get("lifecycle_history", []):
        if group.get("note"):
            reproduce += " --lifecycle-note " + shlex.quote(group["source_prefix"] + "=" + group["note"])
    for phase, label in summary.get("candidate_labels", {}).items():
        reproduce += " --candidate-label " + shlex.quote(phase + "=" + label)
    for historical in summary.get("phase_history", []):
        phase = historical["phases"][0]
        reproduce += " --history-phase " + shlex.quote(phase + "=" + historical["candidate_labels"][phase])
    for phase, note in summary.get("phase_notes", {}).items():
        reproduce += " --phase-note " + shlex.quote(phase + "=" + note)
    title = "# 2026-11-06 内存优化性能记录" if zh else "# Memory performance measurements — 2026-11-06"
    output = [title, ""]
    if zh:
        output += [
            f"共享调度缓存的收益随账户在多个分组中的复用程度增加。2000 账户的单组 retained heap 变化 {cache_one:+.2f}%；100 组变化 {cache_many:+.2f}%。SSE 减少逐帧分配和重复解析，普通计费积压不再保留整份请求/响应正文。下表保留所有正负变化。",
            "", "## 测量环境与口径", "",
            "最初 cache/SSE/billing 基线测试二进制保存自 `8972274c2871fd961dd5a01d0a4c63b76e358add`，候选测试二进制保存自其上的内存优化工作树。后续分项可能使用重新构建的二进制，分别保留各自 manifest。原 runner 的 `source_head` 记录测量时 checkout HEAD；新 runner 将 `binary_source` 与 `measurement_checkout_head` 分开记录。来源按分项构建记录解释，不把测量时 HEAD 当成保存二进制的源码身份，也不把这些分项测量标成最终发布提交。Linux amd64（WSL），AMD Ryzen 7 9700X，Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，`GOMAXPROCS=4`。",
            "", "每侧独立运行三轮；cache 每个 fixture 100 次，SSE 20 次，billing 1 次。表格为基线 → 候选的三轮中位数及变化百分比。波动定义为 `(最大值−最小值)/|中位数|×100%`；三轮是描述性样本。每轮内测得的 p95 再取中位数，不合并为一个总体 p95。",
            "", f"全部三轮原值、中位数、波动和资源记录见[完整汇总]({raw}/performance-summary.md)与[机器可读 JSON]({raw}/performance-summary.json)。",
            "", "## 调度缓存", "",
            "相同 2000 账户由 1/10/100 组复用。Redis fixture 建立完成后，以显式 GC 前后 HeapAlloc 差值测解码对象 retained heap；计时区间是循环读取快照，含两侧相同反射桥和本地 miniredis 访问。ops/s 表示每秒完整候选集读取。",
        ]
    else:
        output += [
            f"Shared scheduling-cache savings grow with account reuse across groups. For 2,000 accounts, one-group retained heap changes {cache_one:+.2f}%; the 100-group case changes {cache_many:+.2f}%. SSE reduces per-frame allocation and repeated parsing, and ordinary queued billing records release full request/response bodies. The tables retain both improvements and regressions.",
            "", "## Environment and measurement definitions", "",
            "Initial cache/SSE/billing baseline test binaries were preserved from `8972274c2871fd961dd5a01d0a4c63b76e358add`, and candidate binaries from the memory-optimization working tree on that base. Later groups may use rebuilt binaries and retain their own manifests. The original runner's `source_head` records the checkout at measurement time; the new runner separates `binary_source` from `measurement_checkout_head`. Interpret provenance per build record: measurement checkout HEAD does not identify a preserved binary's source, and these phase measurements are not labelled as the final release commit. Linux amd64 (WSL), AMD Ryzen 7 9700X, Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`.",
            "", "Each revision has three independent rounds: 100 iterations per cache fixture, 20 per SSE fixture, and one per billing fixture. Tables show baseline → candidate round medians and percentage changes. Spread is `(max − min)/|median| × 100%`; three rounds provide descriptive samples. A reported p95 median summarizes the three in-round p95 values rather than pooling their observations.",
            "", f"All round values, medians, spreads, and process resources are in the [full summary]({raw}/performance-summary.md) and [machine-readable JSON]({raw}/performance-summary.json).",
            "", "## Scheduling cache", "",
            "The same 2,000 accounts are shared by 1/10/100 groups. After constructing the Redis fixture, retained decoded heap is the HeapAlloc difference around explicit GC. Timed reads use the same reflection bridge and local miniredis access on both revisions. ops/s means complete candidate-set reads per second.",
        ]
    output += [""] + report_table(summary, "cache", [("decoded-retained-B", "Retained MiB", 1<<20), ("B/op", "KiB/op", 1024), ("ns/op", "ms/read", 1e6), ("p95-ns", "p95 ms", 1e6), ("ops/s", "reads/s", 1)])
    output += ["", "单组 retained heap 增长体现母本索引与轻量视图的固定成本；每次读取的分配字节仍减少，分配次数未下降。单组延迟与波动应与多组结果一起评估。" if zh else "The one-group retained-heap increase reflects fixed master-index and lightweight-view costs. Allocated bytes per read still decline; allocation count is unchanged. Evaluate the one-group timing and spread alongside the multi-group results."]
    if "sharing" in summary["phases"]:
        output += ["", "### 共享比例与冷热态" if zh else "### Sharing ratios and warm/cold reads", "",
                   "此组固定 2000 个不同账户与 100 个 bucket。共享比例为 0%/50%/100% 时，每个 bucket 分别有 20/1010/2000 个成员；剩余非共享账户均匀分配。一次 op 完整读取 100 个 bucket，因此跨共享比例比较时必须保留不同成员数量。warm 先预热一次；cold 每次 sweep 使用空进程缓存，Redis 保持预填，sweep 后显式 GC 位于计时区间外。每轮只有 3 次 sweep，其 p95 为本轮最大样本。" if zh else "This group fixes 2,000 unique accounts and 100 buckets. At 0%/50%/100% sharing, each bucket contains 20/1,010/2,000 members; nonshared accounts are distributed disjointly. An op reads all 100 buckets, so membership counts differ across sharing ratios. Warm mode performs a prior sweep; each cold sweep starts with an empty process cache and populated Redis, with explicit GC after each sweep excluded from timing. Each round has only three sweeps, so its p95 is its maximum sampled sweep.", ""]
        output += report_table(summary, "sharing", [("ns/op", "ms/sweep", 1e6), ("B/op", "MiB/sweep", 1<<20), ("allocs/op", "allocs/sweep", 1), ("p95-sweep-ns", "p95 sweep ms", 1e6), ("bucket-reads/s", "bucket reads/s", 1)])
    for phase in summary["phases"]:
        if not phase.startswith("sharing-"):
            continue
        counts = sorted({row["iterations_per_round"] for row in summary["benchmarks"] if row["phase"] == phase})
        output += ["", f"### {phase}", "",
                   (f"单独保留的复测组；每个 fixture 每轮迭代数：{counts}。" if zh else f"Separately retained follow-up group; iterations per fixture per round: {counts}."),
                   summary.get("phase_notes", {}).get(phase, ""), ""]
        output += report_table(summary, phase, [("ns/op", "ms/sweep", 1e6), ("B/op", "MiB/sweep", 1<<20), ("allocs/op", "allocs/sweep", 1), ("p95-sweep-ns", "p95 sweep ms", 1e6), ("bucket-reads/s", "bucket reads/s", 1)])
        output += ["", "| Fixture / metric (ms) | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |", "|---|---|---|"]
        for row in summary["benchmarks"]:
            if row["phase"] != phase:
                continue
            for unit in ("ns/op", "p95-sweep-ns"):
                metric = row["metrics"][unit]
                output.append(f"| {row['benchmark'].rsplit('_', 1)[-1]} / {unit} | {rounds_cell(metric['baseline'], 1e6)} | {rounds_cell(metric['candidate'], 1e6)} |")
    canary = summary.get("sharing_canary")
    if canary:
        output += ["", "### 争抢宿主 canary（仅诊断）" if zh else "### Host-contended canary (diagnostic only)", "",
                   canary["note"], "该批存在并发宿主负载，仅作诊断；不与独占100x复测合并。" if zh else "This batch observed concurrent host workload and is diagnostic only; it is not pooled with the exclusive 100x retest.", "",
                   "| Fixture | Time | p95 | B/op | allocs/op |", "|---|---:|---:|---:|---:|"]
        for fixture, metrics in canary["delta_percent"].items():
            output.append(f"| {fixture} | {metrics['ns/op']:+.3f}% | {metrics['p95-sweep-ns']:+.3f}% | {metrics['B/op']:+.3f}% | {metrics['allocs/op']:+.3f}% |")
    output += ["", "## SSE 转发" if zh else "## SSE relay", "",
               "全部采用合成内容和丢弃输出的 writer；一次 op 为完整 fixture 流。WS 指 Engine 的 HTTP/SSE→客户端 WebSocket 适配器，fixture 不建立真实 WebSocket 连接。慢写组每次 Write 睡眠 100 µs，实际调度延迟包含在结果中。" if zh else "All payloads are synthetic and the writer discards output; an op is one complete fixture stream. WS denotes Engine's HTTP/SSE-to-client WebSocket adapter; these fixtures do not create live WebSocket peers. Slow-write fixtures sleep 100 µs in each Write, including actual scheduler delay in the result.", ""]
    output += report_table(summary, "sse", [("ns/op", "ms/stream", 1e6), ("B/op", "MiB/op", 1<<20), ("allocs/op", "allocs/op", 1), ("p95-ns", "p95 ms", 1e6), ("ops/s", "streams/s", 1)])
    output += ["", "### 首次写出" if zh else "### First write", "",
               "首次写出为本地合成流从 relay 入口到 writer 首次返回的时间，包含组帧和观察；它独立于完整流吞吐。" if zh else "First-write timing runs from local relay entry to the first writer return, including framing and observation. It is reported separately from complete-stream throughput.", ""]
    output += report_table(summary, "sse", [("first-write-ns/op", "First write µs", 1000), ("write-ns/op", "Total write ms", 1e6)])
    output += ["", f"代价同样保留：WS 小帧首次写出中位数为 {number(ws_first['baseline']['median']/1000)} → {number(ws_first['candidate']['median']/1000)} µs（{change(ws_first)}），HTTP/WS 多行分别变化 {change(http_multiline)}/{change(ws_multiline)}。这些本地微秒级启动成本与完整流耗时下降同时存在。" if zh else f"The startup cost is retained: WS small-frame median first write is {number(ws_first['baseline']['median']/1000)} → {number(ws_first['candidate']['median']/1000)} µs ({change(ws_first)}); HTTP/WS multiline first write changes {change(http_multiline)}/{change(ws_multiline)}. These local microsecond-scale startup costs coexist with lower complete-stream duration."]
    output += ["", "## 普通计费队列积压" if zh else "## Ordinary billing backlog", "",
               "阻塞一个 worker，排队 32/128 个任务，每个任务使用独立 1 MiB 正文；显式 GC 后测每任务保留内存。`ns/op` 包含 fixture 创建、GC、队列排空与关闭，是实验耗时；`experiments/s` 只是其倒数。两者不作为业务延迟或计费 TPS。累计 B/op 仍包含正文的初始创建，因此不会随 retained heap 同比例下降。" if zh else "One worker is blocked while 32/128 tasks each receive a distinct 1 MiB body. Retained bytes per task are measured after explicit GC. ns/op includes fixture construction, GC, queue drain and shutdown: it is experiment duration, and experiments/s is only its reciprocal. Neither is business latency or billing TPS. Cumulative B/op still includes initial body creation and therefore does not fall in proportion to retained heap.", ""]
    billing_label = summary.get("candidate_labels", {}).get("billing", "candidate")
    output += [f"本节选择 `baseline-billing` → `{billing_label}-billing`。" if zh else f"Selected inputs for this section: `baseline-billing` → `{billing_label}-billing`.", ""]
    if summary.get("phase_notes", {}).get("billing"):
        output += [summary["phase_notes"]["billing"], ""]
    output += report_table(summary, "billing", [("retained-B/task", "Retained B/task", 1), ("B/op", "Allocated MiB/experiment", 1<<20), ("ns/op", "Experiment ms (includes GC)", 1e6)])
    for historical in summary.get("phase_history", []):
        if historical["phases"] != ["billing"]:
            continue
        label = historical["candidate_labels"]["billing"]
        output += ["", "### 早期积压实验（单独保留）" if zh else "### Earlier backlog experiment (retained separately)", "",
                   f"`baseline-billing` → `{label}-billing`；早期测量的诊断结果，未与上表合并。" if zh else f"`baseline-billing` → `{label}-billing`; diagnostic results from an earlier measurement, kept separate from the table above.", ""]
        output += report_table(historical, "billing", [("retained-B/task", "Retained B/task", 1), ("B/op", "Allocated MiB/experiment", 1<<20), ("ns/op", "Experiment ms (includes GC)", 1e6)])
    lifecycle = summary.get("lifecycle")
    if lifecycle:
        output += ["", "### 同一候选版本内的完整本地生命周期" if zh else "### Complete local lifecycle within the candidate revision", "",
                   "此组在同一个候选测试二进制内对比 legacy_capture 与 owned_snapshot；不把两种模式标成两个提交。16 个 producer、4 个 worker、128 队列容量；每次复制并解析 8 KiB/1 MiB 正文，经实际 worker pool，执行相同的本地价格计算和扣费命令构造，再等待结果。计时区间无显式 GC、sleep、模拟网络或数据库时延；fixture 不访问真实数据库。吞吐来自并行全程耗时，p95-e2e 包含正文处理、入队、worker 工作与唤醒，不能直接用并行 ns/op 替代单请求延迟。" if zh else "This group compares legacy_capture and owned_snapshot inside the same candidate test binary, not two commits. Sixteen producers, four workers and a 128-task queue copy/parse an 8 KiB/1 MiB body, use the actual worker pool, perform identical local price calculation and billing-command construction, then await completion. The timed section has no forced GC, sleeps or simulated network/database latency, and the fixture does not access a real database. Throughput uses parallel elapsed time; p95-e2e includes body handling, enqueue, worker work and wake-up. Parallel ns/op is not per-request latency.", "",
                   "| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |", "|---|---:|---:|---:|---:|"]
        for row in lifecycle["benchmarks"]:
            values = []
            for unit, divisor in [("requests/s", 1), ("p95-e2e-ns", 1e6), ("B/op", 1024), ("runtime-cpu-ns/op", 1000)]:
                metric = row["metrics"][unit]
                values.append(f"{number(metric['baseline']['median']/divisor)} → {number(metric['candidate']['median']/divisor)} ({change(metric)})")
            output.append("| " + row["fixture"] + " | " + " | ".join(values) + " |")
        output += ["", f"原始文件前缀：`{lifecycle['source_prefix']}`。" if zh else f"Raw-file prefix: `{lifecycle['source_prefix']}`."]
        if lifecycle.get("note"):
            output += ["", lifecycle["note"]]
    for historical in summary.get("lifecycle_history", []):
        output += ["", "### 早期生命周期诊断（单独保留）" if zh else "### Earlier lifecycle diagnostic (retained separately)", "",
                   (f"`{historical['source_prefix']}` 结果保留如下；它是各自二进制内两种模式的对比，不与上面的最终组混合统计。" if zh else f"The earlier results from `{historical['source_prefix']}` remain below. Each comparison is between two modes within its own binary and is kept separate from the selected final group."), "",
                   historical.get("note", ""), "",
                   "| Fixture | requests/s change | p95 E2E change | B/op change |", "|---|---:|---:|---:|"]
        for row in historical["benchmarks"]:
            output.append(f"| {row['fixture']} | {change(row['metrics']['requests/s'])} | {change(row['metrics']['p95-e2e-ns'])} | {change(row['metrics']['B/op'])} |")
    authenticated = summary.get("authenticated_lifecycle")
    if authenticated:
        output += ["", "### 真实认证 L1 路径的生命周期微基准" if zh else "### Lifecycle microbenchmark with real authentication L1", "",
                   "此组单独测量真实 `APIKeyService.GetByKey` 的 L1 命中与后续 worker 生命周期；每个计时请求都鉴权，冷发布与独立价格投影构建在计时前经真实缓存 miss 完成，仓库替身仅位于存储边界。仍为同一二进制的 legacy_capture/owned_snapshot 对比，16 producer、4 worker；不经过 HTTP handler。" if zh else "This group separately measures the real `APIKeyService.GetByKey` L1-hit path and subsequent worker lifecycle. Every timed request authenticates; a real cache miss publishes the independent price projection before timing, with a repository double only at the storage boundary. It compares legacy_capture/owned_snapshot within one binary, using 16 producers and four workers, without an HTTP handler.", "",
                   "| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |", "|---|---:|---:|---:|---:|"]
        for row in authenticated["benchmarks"]:
            values = []
            for unit, divisor in [("requests/s", 1), ("p95-e2e-ns", 1e6), ("B/op", 1024), ("runtime-cpu-ns/op", 1000)]:
                metric = row["metrics"][unit]
                values.append(f"{number(metric['baseline']['median']/divisor)} → {number(metric['candidate']['median']/divisor)} ({change(metric)})")
            output.append("| " + row["fixture"] + " | " + " | ".join(values) + " |")
        output += ["", f"Raw-file prefix: `{authenticated['source_prefix']}`."]
    http = summary.get("http_lifecycle")
    if http:
        output += ["", "### HTTP 请求与计费结算闭环" if zh else "### HTTP request and billing settlement lifecycle", "",
                   "16 个客户端构成同一闭环：每次等待 HTTP EOF 和 UsageLog.Create 结算完成后才发下一请求；该组与服务层 fallback/authenticated microbench 分开。它使用本地 transport 和存储边界替身，不测 PostgreSQL 性能。GNU time RSS/CPU 覆盖两个正文尺寸、校准与预热。" if zh else "Sixteen clients form one closed loop: each waits for both HTTP EOF and UsageLog.Create settlement before issuing another request. This group is separate from service-level fallback/authenticated microbenchmarks. It uses loopback transports and storage-boundary doubles, so it does not measure PostgreSQL performance. GNU time RSS/CPU covers both payload sizes, calibration and warmup.", "",
                   "| Fixture | HTTP req/s | Settled req/s | HTTP p95 ms | Settled p95 ms | Runtime CPU ms/request | Allocated MiB/op |", "|---|---:|---:|---:|---:|---:|---:|"]
        for row in http["benchmarks"]:
            values = []
            for unit, divisor in [("http-requests/s", 1), ("settled-requests/s", 1), ("p95-http-ns", 1e6), ("p95-settled-ns", 1e6), ("runtime-cpu-ns/request", 1e6), ("B/op", 1<<20)]:
                metric = row["metrics"][unit]
                values.append(f"{number(metric['baseline']['median']/divisor)} → {number(metric['candidate']['median']/divisor)} ({change(metric)})")
            output.append("| " + row["benchmark"].split("request_bytes=")[-1].removesuffix("-4") + " bytes | " + " | ".join(values) + " |")
        small = next(row for row in http["benchmarks"] if "request_bytes=8192" in row["benchmark"])
        large = next(row for row in http["benchmarks"] if "request_bytes=1048576" in row["benchmark"])
        http_p95 = small["metrics"]["p95-http-ns"]["median_change_pct"]
        settled_p95 = small["metrics"]["p95-settled-ns"]["median_change_pct"]
        cpu = small["metrics"]["runtime-cpu-ns/request"]["median_change_pct"]
        worst_large = max(large["metrics"][unit]["median_change_pct"] for unit in ("p95-http-ns", "p95-settled-ns", "runtime-cpu-ns/request"))
        failed_metrics = [f"{row['benchmark'].split('/', 1)[1]} {unit}" for row in http["benchmarks"]
                          for unit in ("p95-http-ns", "p95-settled-ns", "runtime-cpu-ns/request")
                          if row["metrics"][unit]["median_change_pct"] > 5]
        tag = http["phase_tags"]["http-lifecycle"] or "original (2s)"
        if zh:
            output += ["", f"所选HTTP批次：{tag}。8 KiB 组 HTTP p95 {http_p95:+.6f}%，结算 p95 {settled_p95:+.6f}%，CPU/request {cpu:+.6f}%；1 MiB p95/CPU 最差变化 {worst_large:+.6f}%。" + ("超过5%的中位指标：" + "；".join(failed_metrics) + "。" if failed_metrics else "本批两个尺寸的 p95/CPU 中位变化均在5%以内。")]
        else:
            output += ["", f"Selected HTTP batch: {tag}. At 8 KiB, HTTP p95 changes {http_p95:+.6f}%, settled p95 {settled_p95:+.6f}% and CPU/request {cpu:+.6f}%; the worst 1 MiB p95/CPU change is {worst_large:+.6f}%. " + ("Medians exceeding 5%: " + "; ".join(failed_metrics) + "." if failed_metrics else "Both sizes remain within 5% for p95/CPU medians in this batch.")]
        output += ["", "| HTTP fixture / metric | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |", "|---|---|---|"]
        for row in http["benchmarks"]:
            for unit, divisor in [("p95-http-ns", 1e6), ("p95-settled-ns", 1e6), ("runtime-cpu-ns/request", 1e6)]:
                metric = row["metrics"][unit]
                output.append(f"| {row['benchmark'].split('/', 1)[1]} / {unit} (ms) | {rounds_cell(metric['baseline'], divisor)} | {rounds_cell(metric['candidate'], divisor)} |")
        for history in summary.get("http_history", []):
            historical_tag = history["phase_tags"]["http-lifecycle"] or "original (2s)"
            output += ["", f"#### HTTP history: {historical_tag}", "",
                       "早期批次保留为独立诊断，不与所选批次混合。" if zh else "Earlier batch retained separately; not pooled with the selected batch.", "",
                       "| Fixture | CPU change | HTTP p95 change | Settled p95 change |", "|---|---:|---:|---:|"]
            for row in history["benchmarks"]:
                output.append(f"| {row['benchmark'].split('/',1)[1]} | {change(row['metrics']['runtime-cpu-ns/request'])} | {change(row['metrics']['p95-http-ns'])} | {change(row['metrics']['p95-settled-ns'])} |")
        if "http-cpu-final-enqueue.txt" in summary.get("profile_evidence", []):
            output += ["", "CPU 定位使用另一次带采样的 8 KiB 请求运行，不混入性能三轮。候选 profile 中 submitFrozenOpenAIUsageRecord 整个同步构造与入队路径累计 0.42 秒、占样本 0.47%；其中 SnapshotOpenAIRecordUsageInput 为 0.25 秒、0.28%。0.47% 不是纯构造占比。完整路径主要成本仍在 HTTP/JSON 解析与系统调用；这些数值描述该次 profile。" if zh else "CPU attribution comes from a separate profiled 8 KiB run, outside the benchmark medians. The candidate profile attributes 0.42 seconds / 0.47% cumulative samples to the complete synchronous submitFrozenOpenAIUsageRecord construction-and-enqueue path; SnapshotOpenAIRecordUsageInput accounts for 0.25 seconds / 0.28%. The 0.47% figure is not pure construction. The full path spends most time in HTTP/JSON parsing and system calls; these shares describe that profile run.",
                       f"[Candidate CPU top]({raw}/http-cpu-final-top.txt), [baseline CPU top]({raw}/http-cpu-baseline-top.txt), [construction and enqueue detail]({raw}/http-cpu-final-enqueue.txt).",
                       "原始 .prof 保存在本地任务 artifacts，交付只包含其文本输出。" if zh else "Raw .prof files remain in local task artifacts; the delivery contains their text output only."]
    output += ["", "## 整个阶段进程的资源" if zh else "## Whole-phase process resources", "",
               "GNU time 统计整个阶段进程，涵盖全部子基准、fixture 准备、预热、GC 与清理。峰值 RSS 与上文 retained Go heap 口径不同；CPU 每轮先对 user+system 秒数求和，再取三轮中位数。" if zh else "GNU time covers the entire phase process, including all sub-benchmarks, fixture preparation, warmup, GC and teardown. Maximum RSS has a different scope from retained Go heap above; total CPU is the median of per-round user-plus-system sums.", "",
               "| Phase | Peak RSS MiB | User CPU s | System CPU s | Total CPU s | Wall s |", "|---|---:|---:|---:|---:|---:|"]
    for row in summary["resources"] + (http["resources"] if http else []):
        values = []
        for unit, divisor in [("max_rss_KiB", 1024), ("user_CPU_s", 1), ("system_CPU_s", 1), ("user_plus_system_CPU_s", 1), ("wall_s", 1)]:
            metric = row["metrics"][unit]
            values.append(f"{number(metric['baseline']['median']/divisor)} → {number(metric['candidate']['median']/divisor)} ({change(metric)})")
        output.append("| " + row["phase"] + " | " + " | ".join(values) + " |")
    output += ["", f"计费实验进程的峰值 RSS 中位数变化 {change(billing_resource['max_rss_KiB'])}，CPU 总秒数变化 {change(billing_resource['user_plus_system_CPU_s'])}；这与阻塞队列的 retained heap 降低同时出现。该进程仍构造全部 1 MiB 正文并执行显式 GC，资源表覆盖这些成本。" if zh else f"The billing experiment process changes median maximum RSS by {change(billing_resource['max_rss_KiB'])} and total CPU seconds by {change(billing_resource['user_plus_system_CPU_s'])} alongside lower backlogged retained heap. It still constructs every 1 MiB body and runs explicit GC; the resource table includes those costs."]
    if lifecycle:
        output += ["", "生命周期进程每轮都运行两种模式，其整进程资源只报告合计，不归因给任一模式。" if zh else "Each lifecycle process runs both ownership modes. Its process resources are reported together, without attributing them to either mode.", "",
                   "| Lifecycle resource | Round 1 / 2 / 3; median; spread |", "|---|---|"]
        for unit, metric in lifecycle["resources"].items():
            output.append(f"| {unit} | {rounds_cell(metric)} |")
    if authenticated:
        output += ["", "认证微基准的进程资源同样包含两种模式，单独列出。" if zh else "Authenticated microbenchmark process resources likewise cover both modes and remain separate.", "",
                   "| Authenticated resource | Round 1 / 2 / 3; median; spread |", "|---|---|"]
        for unit, metric in authenticated["resources"].items():
            output.append(f"| {unit} | {rounds_cell(metric)} |")
    output += operational_sections(summary, raw, zh)
    output += ["", "## 从原始文件重新汇总" if zh else "## Rebuild the summary from raw files", "",
               "汇总脚本只读取已保存结果，不执行基准。交付目录只含小型 txt/json/md 原始数据与汇总，不含测试二进制。" if zh else "The script reads saved results and does not execute benchmarks. The delivered evidence directory contains only small txt/json/md inputs and summaries, without test binaries.", "",
               "```sh", reproduce, "```", "",
               "共享比例与生命周期属于独立实验组，分别保留自己的操作单位与测量范围。此文报告已保存的原始测量，是否满足验收需结合协议、正确性与性能评审。" if zh else "Sharing-ratio and lifecycle measurements are separate groups with their own operation units and scope. This document reports saved measurements; acceptance also depends on protocol, correctness and performance review."]
    output += ["", "### 复现性能与回归验证" if zh else "### Reproduce performance and regression checks", "",
               "性能 runner 固定 Go/GOEXPERIMENT/GOMAXPROCS，并通过 GNU time 保存每轮资源；在独占 CPU 的窗口执行。使用新的 label 保存复测，避免覆盖历史。只有已有匹配的测试二进制时才使用 `--skip-build`，并传入准确的 `--binary-source`。baseline 必须从基线源码与相同 benchmark fixture 构建，不能把当前候选构建标成 baseline。" if zh else "The performance runner fixes Go/GOEXPERIMENT/GOMAXPROCS and captures each round with GNU time. Run it in an exclusive CPU window and use a new label to preserve earlier runs. Use `--skip-build` only with the matching preserved test binary and an accurate `--binary-source`. Build the baseline from baseline production sources plus the identical benchmark fixture; do not label a current candidate build as baseline.", "",
               "```sh",
               "python backend/benchmarks/memory-performance.py --label rerun --phase all",
               "python backend/benchmarks/memory-performance.py --label rerun --phase sharing",
               "python backend/benchmarks/memory-performance.py --label rerun --phase lifecycle",
               "python backend/benchmarks/verify-memory.py --phase unit",
               "python backend/benchmarks/verify-memory.py --phase race",
               "python backend/benchmarks/verify-memory.py --phase build",
               "python backend/benchmarks/verify-memory.py --phase integration",
               "```"]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(output) + "\n", encoding="utf-8")


def main():
    root = Path(__file__).resolve().parents[2]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=root / ".git/task-artifacts/memory-20261106")
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--phases", nargs="+", default=["cache", "sse", "billing"])
    parser.add_argument("--candidate-label", action="append", default=[], metavar="PHASE=LABEL", help="Choose a separate candidate file label for one phase")
    parser.add_argument("--history-phase", action="append", default=[], metavar="PHASE=LABEL", help="Keep an earlier phase candidate as separate diagnostic history")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--publish-dir", type=Path, help="Copy only parsed txt/json evidence and summaries here")
    parser.add_argument("--write-docs", action="store_true", help="Generate English and Chinese overview documents")
    parser.add_argument("--include-lifecycle", action="store_true", help="Compare legacy/owned modes from candidate-lifecycle round files")
    parser.add_argument("--lifecycle-prefix", default="candidate-lifecycle", help="File stem for the selected same-binary lifecycle group")
    parser.add_argument("--authenticated-prefix", help="File stem for the separately measured real-authentication L1 lifecycle group")
    parser.add_argument("--http-prefix", help="Candidate label for the separate HTTP client/server lifecycle group (normally candidate)")
    parser.add_argument("--http-tag", default="", help="Tag suffix of the selected HTTP measurement batch")
    parser.add_argument("--http-history-tag", action="append", default=[], help="Retain an earlier HTTP tag separately; use original for the untagged batch")
    parser.add_argument("--lifecycle-history-prefix", action="append", default=[], help="Retain an earlier lifecycle group as a separate diagnostic, without pooling")
    parser.add_argument("--lifecycle-note", action="append", default=[], metavar="PREFIX=TEXT", help="Record build/host conditions of a selected or historical lifecycle group")
    parser.add_argument("--phase-note", action="append", default=[], metavar="PHASE=TEXT", help="Retain measurement conditions, including known host contention")
    args = parser.parse_args()
    output = args.output_dir or args.source
    candidate_labels = {}
    for item in args.candidate_label:
        phase, separator, label = item.partition("=")
        if not separator or phase not in args.phases or not re.fullmatch(r"[a-zA-Z0-9_-]+", label):
            parser.error("--candidate-label must be selected PHASE=filename-safe-LABEL")
        candidate_labels[phase] = label
    summary = aggregate(args.source, args.phases, args.rounds, candidate_labels)
    summary["phase_notes"] = {}
    for item in args.phase_note:
        phase, separator, note = item.partition("=")
        if not separator or phase not in args.phases or not note:
            parser.error("--phase-note must name a selected phase and include nonempty text")
        summary["phase_notes"][phase] = note
    summary["verification"] = collect_validation(args.source)
    summary["known_failures"] = collect_known_failures(args.source)
    summary["source_files"] = sorted(set(summary["source_files"] + [record["published"] for record in summary["verification"] + summary["known_failures"]]))
    summary["sharing_canary"], canary_files = collect_sharing_canary(args.source)
    summary["source_files"] = sorted(set(summary["source_files"] + canary_files))
    summary["profile_evidence"] = [path.name for path in sorted(args.source.glob("http-cpu-*.txt"))]
    summary["source_files"] = sorted(set(summary["source_files"] + summary["profile_evidence"]))
    summary["phase_history"] = []
    for item in args.history_phase:
        phase, separator, label = item.partition("=")
        if not separator or phase not in args.phases or not re.fullmatch(r"[a-zA-Z0-9_-]+", label):
            parser.error("--history-phase must be selected PHASE=filename-safe-LABEL")
        if label == candidate_labels.get(phase, "candidate"):
            parser.error("Historical phase candidate must differ from the selected candidate")
        historical = aggregate(args.source, [phase], args.rounds, {phase: label})
        if historical["metadata"][phase] != summary["metadata"][phase]:
            raise ValueError(f"Historical phase metadata differs: {phase}")
        validate_manifest_settings(summary["manifests"] | {
            "historical_" + key: value for key, value in historical["manifests"].items()
        })
        summary["phase_history"].append(historical)
        summary["source_files"] = sorted(set(summary["source_files"] + historical["source_files"]))
    if args.include_lifecycle:
        summary["lifecycle"], lifecycle_files = aggregate_lifecycle(args.source, args.rounds, args.lifecycle_prefix)
        expected_host = next(iter(summary["metadata"].values()))
        for field in ("goos", "goarch", "cpu", "benchmark_gomaxprocs"):
            if summary["lifecycle"]["metadata"].get(field) != expected_host.get(field):
                raise ValueError(f"Lifecycle differs from main measurements for {field}")
        validate_manifest_settings(summary["manifests"] | {
            f"lifecycle_{key}": value for key, value in summary["lifecycle"]["manifests"].items()
        })
        summary["source_files"] = sorted(set(summary["source_files"] + lifecycle_files))
    elif args.lifecycle_history_prefix:
        parser.error("--lifecycle-history-prefix requires --include-lifecycle")
    summary["lifecycle_history"] = []
    for prefix in args.lifecycle_history_prefix:
        if prefix == args.lifecycle_prefix:
            parser.error("Lifecycle history must have a different prefix from the selected group")
        historical, history_files = aggregate_lifecycle(args.source, args.rounds, prefix)
        if historical["metadata"] != summary["lifecycle"]["metadata"]:
            raise ValueError(f"Historical lifecycle metadata differs: {prefix}")
        validate_manifest_settings(summary["manifests"] | {
            f"historical_{key}": value for key, value in historical["manifests"].items()
        })
        summary["lifecycle_history"].append(historical)
        summary["source_files"] = sorted(set(summary["source_files"] + history_files))
    lifecycle_groups = {group["source_prefix"]: group for group in ([summary["lifecycle"]] if "lifecycle" in summary else []) + summary["lifecycle_history"]}
    for item in args.lifecycle_note:
        prefix, separator, note = item.partition("=")
        if not separator or prefix not in lifecycle_groups or not note:
            parser.error("--lifecycle-note must name a selected/historical prefix and include nonempty text")
        lifecycle_groups[prefix]["note"] = note
    if args.authenticated_prefix:
        authenticated, auth_files = aggregate_lifecycle(args.source, args.rounds, args.authenticated_prefix)
        authenticated["scope"] += " Every timed operation uses production APIKeyService.GetByKey with an L1 hit; the real cold publication is outside timing, and only the repository boundary is a test double. This is an authenticated microbenchmark, not HTTP handler end-to-end latency."
        expected_host = next(iter(summary["metadata"].values()))
        for field in ("goos", "goarch", "cpu", "benchmark_gomaxprocs"):
            if authenticated["metadata"].get(field) != expected_host.get(field):
                raise ValueError(f"Authenticated lifecycle differs for {field}")
        validate_manifest_settings(summary["manifests"] | {f"authenticated_{key}": value for key, value in authenticated["manifests"].items()})
        summary["authenticated_lifecycle"] = authenticated
        summary["source_files"] = sorted(set(summary["source_files"] + auth_files))
    if args.http_prefix:
        http = aggregate_http_lifecycle(args.source, args.rounds, args.http_prefix, args.http_tag)
        expected_host = next(iter(summary["metadata"].values()))
        for field in ("goos", "goarch", "cpu", "benchmark_gomaxprocs"):
            if http["metadata"]["http-lifecycle"].get(field) != expected_host.get(field):
                raise ValueError(f"HTTP lifecycle differs for {field}")
        validate_manifest_settings(summary["manifests"] | {f"http_{key}": value for key, value in http["manifests"].items()})
        summary["http_lifecycle"] = http
        summary["source_files"] = sorted(set(summary["source_files"] + http["source_files"]))
        summary["http_history"] = []
        for historical_tag in args.http_history_tag:
            historical_tag = "" if historical_tag == "original" else historical_tag
            if historical_tag == args.http_tag:
                parser.error("Historical HTTP tag must differ from selected tag")
            historical = aggregate_http_lifecycle(args.source, args.rounds, args.http_prefix, historical_tag)
            if historical["metadata"] != http["metadata"]:
                raise ValueError("Historical HTTP metadata differs")
            summary["http_history"].append(historical)
            summary["source_files"] = sorted(set(summary["source_files"] + historical["source_files"]))
    output.mkdir(parents=True, exist_ok=True)
    for record in summary["verification"] + summary["known_failures"]:
        evidence = evidence_path(args.source, record["published"])
        destination = output / record["published"]
        if evidence.resolve() != destination.resolve():
            shutil.copyfile(evidence, destination)
    json_path, markdown_path = output / "performance-summary.json", output / "performance-summary.md"
    json_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    write_full_markdown(summary, markdown_path)
    if args.publish_dir:
        args.publish_dir.mkdir(parents=True, exist_ok=True)
        for name in summary["source_files"]:
            source, destination = evidence_path(args.source, name), args.publish_dir / name
            if source.resolve() != destination.resolve():
                shutil.copyfile(source, destination)
        for source in (json_path, markdown_path):
            destination = args.publish_dir / source.name
            if source.resolve() != destination.resolve():
                shutil.copyfile(source, destination)
    if args.write_docs:
        write_report(summary, root / "docs/memory-performance-20261106.md", False)
        write_report(summary, root / "docs/memory-performance-20261106.zh-CN.md", True)
    print(f"Summarized {len(summary['benchmarks'])} fixtures across {args.rounds} rounds; {len(summary['source_files'])} raw inputs.")
    print(json_path)
    print(markdown_path)


if __name__ == "__main__":
    main()
