#!/usr/bin/env python3
"""Run targeted local memory regressions and isolated repository integration tests."""
import argparse
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
OUT = ROOT / ".git" / "task-artifacts" / "memory-20261106"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=["unit", "race", "build", "integration"], required=True)
    args = parser.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOEXPERIMENT="greenteagc,jsonv2", GOMAXPROCS="4")
    if subprocess.check_output(["go", "env", "GOVERSION"], cwd=BACKEND, env=env, text=True).strip() != "go1.27.3":
        raise SystemExit("Go 1.27.3 required")
    pattern = "Test(CodexEngine|OpenAIWSBodyObservation|OpenAIWSPassthroughBodyObservation|OpenAIWSNativeBodyObservation|OpenAIWSNativeInterrupt|OpenAIWSHTTPBridge|UsageRecord|UsageSnapshot|UsageBilling|APIKeyAuth|FrozenUsage|WrapUsage|InflightEstimate_AccountMapping|Scheduler|Candidate|SharedCandidate|AccountPrepareSharedSnapshot|SetAccountModelRateLimitSnapshot|ApplyAccountSchedulingThreshold|RuntimeMemory|RequestBodyScope|Counters|SettlementCounters|SSEBufferGauge|ReadSampleJSON|CollectorRing|ProfileGate|RedisHistory|RedisInstance|RedisUnavailable)"
    packages = ["./internal/service", "./internal/repository", "./internal/handler", "./internal/handler/admin", "./internal/server/routes"]
    commands = []
    if args.phase in ("unit", "race"):
        commands.append((["go", "test", "-tags=unit", *(["-race"] if args.phase == "race" else []), *packages, "-run", pattern, "-count=1", "-timeout=10m"], env))
        commands.append((["go", "test", "-tags=unit", *(["-race"] if args.phase == "race" else []), "./internal/pkg/runtimeobs", "./internal/pkg/httputil", "./internal/config", "-count=1", "-timeout=10m"], env))
    elif args.phase == "build":
        commands.append((["go", "build", "-o", str(OUT / "sub2api-verified.exe"), "./cmd/server"], env))
    else:
        binary = OUT / "integration-repository.test"
        commands.append((["go", "test", "-tags=integration", "-c", "-o", str(binary), "./internal/repository"], dict(env, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")))
        binary_path = str(binary.resolve()).replace("\\", "/")
        if os.name == "nt":
            binary_path = "/mnt/" + binary_path[0].lower() + binary_path[2:]
        command = ["env", "CI=true", "GOMAXPROCS=4", binary_path,
                   "-test.run=Test(Scheduler|UsageRecordSnapshotDetached|UsageBillingRepositoryApply|CodexEngineBillingStore|ListSchedulableAccountLoads)", "-test.v", "-test.timeout=15m"]
        if os.name == "nt":
            command = ["wsl.exe", "-d", "Ubuntu-24.04", "--exec", *command]
        commands.append((command, env))
    with (OUT / ("final-" + args.phase + ".log")).open("w", encoding="utf-8") as log:
        for command, command_env in commands:
            log.write(json.dumps({"command": command, "GOEXPERIMENT": env["GOEXPERIMENT"], "GOMAXPROCS": 4}) + "\n")
            log.flush()
            result = subprocess.run(command, cwd=BACKEND, env=command_env, stdout=log, stderr=subprocess.STDOUT)
            log.write(f"\nexit_code={result.returncode}\n")
            log.flush()
            if result.returncode:
                raise SystemExit(result.returncode)
    print("Passed " + args.phase, flush=True)


if __name__ == "__main__":
    main()
