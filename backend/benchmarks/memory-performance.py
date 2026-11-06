#!/usr/bin/env python3
"""Run isolated, synthetic memory scenarios with fixed Linux Go test binaries.

On Windows this uses the existing Ubuntu-24.04 WSL distribution. It never
contacts application servers. Preserve the baseline binaries before editing
production code; use --skip-build when measuring that baseline again.
"""

import argparse
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time


ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
ARTIFACTS = ROOT / ".git" / "task-artifacts" / "memory-20261106"
SCENARIOS = {
    "cache": ("repository", "^BenchmarkSchedulerCacheReadSharing$", "100x"),
    "sse": ("service", "^BenchmarkCodexEngineRelay$", "20x"),
    "billing": ("service", "^BenchmarkUsageRecordQueueRetainedHeap$", "1x"),
    "sharing": ("sharing", "^BenchmarkSchedulerCacheSharingModes$", "3x"),
    "lifecycle": ("lifecycle", "^BenchmarkUsageRecordLifecycle$", "2s"),
    "authenticated": ("lifecycle", "^BenchmarkUsageRecordLifecycleAuthenticated$", "2s"),
}


def linux_path(path):
    value = str(path.resolve()).replace("\\", "/")
    if re.match(r"^[A-Za-z]:/", value):
        return "/mnt/" + value[0].lower() + value[2:]
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--label", required=True)
    parser.add_argument("--skip-build", action="store_true")
    parser.add_argument("--phase", choices=["all", *SCENARIOS], default="all")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--binary-source", help="Source revision/description of a preserved binary (not the current checkout)")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9_-]+", args.label) or args.rounds < 1:
        parser.error("label must be a filename-safe token and rounds positive")
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOEXPERIMENT="greenteagc,jsonv2", GOMAXPROCS="4")
    phases = ["cache", "sse", "billing"] if args.phase == "all" else [args.phase]
    metadata = {
        "label": args.label,
        "measurement_checkout_head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "binary_source": args.binary_source or ("preserved; see build record" if args.skip_build else "current checkout including local changes"),
        "go": subprocess.check_output(["go", "env", "GOVERSION"], cwd=BACKEND, env=env, text=True).strip(),
        "goexperiment": env["GOEXPERIMENT"],
        "gomaxprocs": 4,
        "rounds": args.rounds,
        "host": platform.platform(),
        "uses_preserved_binary": args.skip_build,
        "phases": phases,
    }
    if metadata["go"] != "go1.27.3":
        raise SystemExit("Expected Go 1.27.3, got " + metadata["go"])
    manifest = ARTIFACTS / (args.label + "-" + args.phase + "-manifest.json")
    manifest.write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    if not args.skip_build:
        build_env = dict(env, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
        for package in sorted({SCENARIOS[p][0] for p in phases}):
            binary = ARTIFACTS / (args.label + "-" + package + ".test")
            source_package = "repository" if package == "sharing" else "service" if package == "lifecycle" else package
            subprocess.run(["go", "test", "-tags=unit", "-c", "-o", str(binary), "./internal/" + source_package],
                           cwd=BACKEND, env=build_env, check=True)
    summary = []
    for phase in phases:
        package, pattern, bench_time = SCENARIOS[phase]
        binary = ARTIFACTS / (args.label + "-" + package + ".test")
        if not binary.exists():
            raise SystemExit("Missing preserved binary: " + str(binary))
        for round_number in range(1, args.rounds + 1):
            stem = f"{args.label}-{phase}-{round_number}"
            output = ARTIFACTS / (stem + ".txt")
            resources = ARTIFACTS / (stem + "-resources.txt")
            command = ["/usr/bin/time", "-v", "-o", linux_path(resources), "env", "GOMAXPROCS=4",
                       "GIN_MODE=release", linux_path(binary), "-test.run=^$", "-test.bench=" + pattern,
                       "-test.benchmem", "-test.benchtime=" + bench_time, "-test.count=1", "-test.timeout=20m"]
            if os.name == "nt":
                command = ["wsl.exe", "-d", "Ubuntu-24.04", "--", *command]
            print(f"Running {stem}", flush=True)
            started = time.monotonic()
            with output.open("w", encoding="utf-8") as stream:
                result = subprocess.run(command, cwd=BACKEND, env=env, stdout=stream, stderr=subprocess.STDOUT)
            entry = {"phase": phase, "round": round_number, "exit_code": result.returncode,
                     "elapsed_seconds": round(time.monotonic() - started, 3), "output": output.name,
                     "resources": resources.name}
            summary.append(entry)
            print(json.dumps(entry), flush=True)
            if result.returncode:
                raise SystemExit("Scenario failed; inspect " + str(output))
    (ARTIFACTS / (args.label + "-" + args.phase + "-runs.json")).write_text(
        json.dumps(summary, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
