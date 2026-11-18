"""Three alternating rounds; fixed per-revision binaries, common data and CPU affinity."""
import argparse
import ctypes
import datetime
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True, type=Path)
    parser.add_argument("--merged", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--working-directory", required=True, type=Path)
    parser.add_argument("--mask", type=lambda value: int(value, 0), default=0x55)
    parser.add_argument("--case", action="append", help="Run only the named case(s).")
    args = parser.parse_args()
    if os.name != "nt":
        parser.error("This recorded run uses Windows CPU affinity.")
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.GetCurrentProcess.restype = ctypes.c_void_p
    kernel.SetProcessAffinityMask.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
    if not kernel.SetProcessAffinityMask(kernel.GetCurrentProcess(), args.mask):
        raise ctypes.WinError(ctypes.get_last_error())
    binaries = {"baseline": args.baseline.resolve(), "merged": args.merged.resolve()}
    env = dict(os.environ, GOMAXPROCS="4")
    args.output.mkdir(parents=True, exist_ok=True)
    cases = {
        "stream": ("^Benchmark(RawChatCompletionsFlushBatch|OpenAIReasoningRewrite300)$", "2s"),
        "usage": ("^Benchmark(GatewayService_ParseSSEUsage.*|ParseClaudeUsageFromResponseBody)$", "2s"),
        "catalog": ("^BenchmarkOpenAIServiceTierCachedLargeCatalog$", "2s"),
        "retry": ("^BenchmarkCodexTransformRetryCPU/8MiB/image_false/attempts_15/sparse$", "2s"),
        "lifecycle": ("^BenchmarkUsageRecordLifecycleAuthenticated/ws_replay/bytes=1048576/owned_snapshot$", "2s"),
        "retention": ("^BenchmarkUsageRecordQueueRetainedHeap/.*/queued=128$", "1x"),
    }
    if args.case:
        unknown = set(args.case) - cases.keys()
        if unknown:
            parser.error(f"Unknown cases: {sorted(unknown)}")
        cases = {name: cases[name] for name in dict.fromkeys(args.case)}
    for round_id in range(1, 4):
        order = ["merged", "baseline"] if round_id == 2 else ["baseline", "merged"]
        for variant in order:
            for case, (pattern, duration) in cases.items():
                command = [str(binaries[variant]), "-test.run=^$", "-test.bench=" + pattern,
                           "-test.benchtime=" + duration, "-test.count=1", "-test.benchmem"]
                path = args.output / f"{case}-{variant}-{round_id}.log"
                with path.open("xb") as out:
                    out.write((f"UTC={datetime.datetime.now(datetime.timezone.utc).isoformat()}\n"
                               f"affinity={args.mask:#x}; GOMAXPROCS=4; round={round_id}; order={order}\n"
                               f"command={command!r}\n").encode())
                    out.flush()
                    subprocess.run(command, cwd=args.working_directory, env=env,
                                   stdout=out, stderr=subprocess.STDOUT, check=True)
                if not any(line.startswith("Benchmark") and "ns/op" in line
                           for line in path.read_text(encoding="utf-8").splitlines()):
                    raise RuntimeError(f"Missing result: {path}")
                print(f"{round_id}/3 {variant} {case}: complete", flush=True)


if __name__ == "__main__":
    main()
