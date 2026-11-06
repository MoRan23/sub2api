#!/usr/bin/env python3
"""Build identical full-HTTP fixtures against baseline/current sources; preserve binaries.

Default mode compiles both Linux binaries and performs 32-request concurrent smoke checks.
Formal rounds require --mode run and reuse those preserved binaries. No external
model or application endpoint is contacted: both HTTP servers run on loopback.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time


ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / ".git" / "task-artifacts" / "memory-20261106"
BASELINE = OUT / "baseline-src" / "backend"
BASELINE_REF = "8972274c2871fd961dd5a01d0a4c63b76e358add"
FIXTURE = Path("internal/handler/http_billing_lifecycle_benchmark_test.go")
PATTERN = "^BenchmarkHTTPBillingLifecycle$"


def linux_path(path):
    value = str(path.resolve()).replace("\\", "/")
    if re.match(r"^[A-Za-z]:/", value):
        return "/mnt/" + value[0].lower() + value[2:]
    return value


def build(label, env):
    source = BASELINE if label == "baseline" else ROOT / "backend"
    if not (source / "go.mod").is_file():
        raise SystemExit(f"Missing source tree: {source}")
    if label == "baseline":
        # This is the only source copied into baseline. Its production code stays
        # at the exported reference; no new runtime/observability code is added.
        shutil.copyfile(ROOT / "backend" / FIXTURE, source / FIXTURE)
    else:
        # Retain all tracked and nonignored new sources, including added files
        # that a git diff alone cannot preserve. Build this immutable copy.
        frozen = OUT / ("candidate-http-src-" + str(time.time_ns()))
        paths = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "backend"], cwd=ROOT).decode().split("\0")
        for relative in paths:
            if not relative or not (ROOT / relative).is_file():
                continue
            target = frozen / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, target)
        source = frozen / "backend"
    binary = OUT / f"{label}-http-lifecycle.test"
    command = ["go", "test", "-tags=unit", "-c", "-o", str(binary), "./internal/handler"]
    started = time.monotonic()
    with (OUT / f"{label}-http-lifecycle-build.log").open("w", encoding="utf-8") as stream:
        subprocess.run(command, cwd=source, env=dict(env, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0"), stdout=stream, stderr=subprocess.STDOUT, check=True)
    manifest = {
        "label": label, "binary": binary.name,
        "binary_source": BASELINE_REF if label == "baseline" else "retained candidate source copy; see source_directory and retained diff/status",
        "measurement_checkout_head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "go": subprocess.check_output(["go", "env", "GOVERSION"], cwd=source, env=env, text=True).strip(),
        "goexperiment": env["GOEXPERIMENT"], "gomaxprocs": 4,
        "source_directory": str(source), "fixture": str(FIXTURE), "build_command": command,
        "build_elapsed_seconds": round(time.monotonic() - started, 3),
        "scope": "Standard API-key auth plus HTTP Responses SSE forwarding and ordinary RecordUsage; loopback transports; fake storage boundaries only. Fixed 16 closed-loop client lifecycles, 4 workers, queue 128. HTTP EOF and persistence settlement p95/rates are separate; CPU is the combined process/path.",
        "resource_scope": "GNU time CPU and MaxRSS cover the whole test process, both payloads, calibration and warmup. Benchmark runtime CPU covers the measured concurrent lifecycles through persistence settlement. The clients wait for both HTTP EOF and settlement before issuing their next request; both completion rates describe this same load.",
    }
    if label == "candidate":
        for suffix, command in (("source.diff", ["git", "diff", "HEAD", "--", "backend"]), ("source-status.txt", ["git", "status", "--short", "--", "backend"])):
            with (OUT / f"{label}-http-lifecycle-{suffix}").open("w", encoding="utf-8") as stream:
                subprocess.run(command, cwd=ROOT, stdout=stream, check=True)
    (OUT / f"{label}-http-lifecycle-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def run(label, index, benchtime, smoke, env, tag="", benchmark=PATTERN):
    binary = OUT / f"{label}-http-lifecycle.test"
    if not binary.is_file():
        raise SystemExit(f"Missing preserved binary: {binary}; run --mode build-smoke first")
    suffix = "-" + tag if tag else ""
    stem = f"{label}-http-lifecycle-{'smoke' if smoke else index}{suffix}"
    output, resources = OUT / f"{stem}.txt", OUT / f"{stem}-resources.txt"
    command = ["/usr/bin/time", "-v", "-o", linux_path(resources), "env", "GOMAXPROCS=4", "GIN_MODE=release",
               linux_path(binary), "-test.run=^$", "-test.bench=" + benchmark, "-test.benchmem",
               "-test.benchtime=" + benchtime, "-test.count=1", "-test.timeout=10m"]
    if os.name == "nt":
        command = ["wsl.exe", "-d", "Ubuntu-24.04", "--", *command]
    print(f"Running {stem}", flush=True)
    started = time.monotonic()
    with output.open("w", encoding="utf-8") as stream:
        result = subprocess.run(command, cwd=ROOT / "backend", env=env, stdout=stream, stderr=subprocess.STDOUT)
    entry = {"label": label, "round": index, "smoke": smoke, "benchtime": benchtime,
             "command": command, "exit_code": result.returncode, "output": output.name,
             "resources": resources.name, "elapsed_seconds": round(time.monotonic() - started, 3)}
    if tag:
        entry["tag"] = tag
    if benchmark != PATTERN:
        entry["benchmark"] = benchmark
    print(json.dumps(entry), flush=True)
    if result.returncode:
        raise SystemExit(f"Failed {stem}; inspect {output}")
    return entry


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("build-smoke", "smoke", "run"), default="build-smoke")
    parser.add_argument("--labels", choices=("baseline", "candidate"), nargs="+", default=["baseline", "candidate"])
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--benchtime", default="2s")
    parser.add_argument("--tag", default="", help="Suffix for round outputs and run manifests; reuse the same preserved binaries")
    parser.add_argument("--benchmark", default=PATTERN, help="Go -test.bench filter; default selects the complete HTTP fixture")
    args = parser.parse_args()
    if args.rounds < 1:
        parser.error("rounds must be positive")
    if args.tag and not re.fullmatch(r"[a-zA-Z0-9_-]+", args.tag):
        parser.error("tag must contain only letters, digits, hyphens and underscores")
    if not args.benchmark:
        parser.error("benchmark must not be empty")
    OUT.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOEXPERIMENT="greenteagc,jsonv2", GOMAXPROCS="4")
    version = subprocess.check_output(["go", "env", "GOVERSION"], cwd=ROOT / "backend", env=env, text=True).strip()
    if version != "go1.27.3":
        raise SystemExit(f"Expected Go 1.27.3, got {version}")
    if args.mode == "build-smoke":
        for label in args.labels:
            build(label, env)
    if args.tag:
        # Preserve the binary build provenance and separately record the current
        # measurement settings. Tagged runs never replace the original manifest.
        checkout = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        for label in args.labels:
            source = OUT / f"{label}-http-lifecycle-manifest.json"
            manifest = json.loads(source.read_text(encoding="utf-8-sig")) if source.exists() else {"label": label, "binary_source": "preserved; original build manifest unavailable"}
            manifest.update({"tag": args.tag, "measurement_checkout_head": checkout,
                             "measurement_mode": args.mode, "measurement_rounds": args.rounds if args.mode == "run" else 1,
                             "measurement_benchtime": args.benchtime if args.mode == "run" else "32x",
                             "measurement_benchmark": args.benchmark, "build_manifest": source.name})
            (OUT / f"{label}-http-lifecycle-manifest-{args.tag}.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    entries = []
    rounds = args.rounds if args.mode == "run" else 1
    for index in range(1, rounds + 1):
        # Alternate process order to balance monotonic host drift across rounds.
        labels = args.labels if index % 2 else list(reversed(args.labels))
        for label in labels:
            entries.append(run(label, index, args.benchtime if args.mode == "run" else "32x", args.mode != "run", env, args.tag, args.benchmark))
    suffix = "-" + args.tag if args.tag else ""
    (OUT / f"http-lifecycle-{args.mode}-runs{suffix}.json").write_text(json.dumps(entries, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
