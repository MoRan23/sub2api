"""Run the V4 representative Go benchmarks on fixed Windows CPU affinity.

Pass separately compiled baseline and merged service test binaries. This
driver neither compiles code nor calls production services. It creates a new
UTF-8 log for each measurement and refuses to overwrite existing logs.
"""

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
    parser.add_argument("--working-directory", type=Path, default=Path.cwd())
    parser.add_argument("--mask", type=lambda value: int(value, 0), default=0x55)
    args = parser.parse_args()
    if os.name != "nt":
        parser.error("The recorded V4 run uses Windows process affinity.")
    if args.mask <= 0:
        parser.error("--mask must be a positive CPU affinity mask.")

    binaries = {"baseline": args.baseline.resolve(), "merged": args.merged.resolve()}
    for variant, binary in binaries.items():
        if not binary.is_file():
            parser.error(f"Missing {variant} test binary: {binary}")
    output_dir = args.output.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.GetCurrentProcess.restype = ctypes.c_void_p
    kernel32.SetProcessAffinityMask.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
    if not kernel32.SetProcessAffinityMask(kernel32.GetCurrentProcess(), args.mask):
        raise ctypes.WinError(ctypes.get_last_error())

    env = os.environ.copy()
    env["GOMAXPROCS"] = "4"
    cases = {
        "schema": "^BenchmarkOpenAIResponsesToolSchemasCPU/image/8MiB/repair=false/current$",
        "transform": "^BenchmarkCodexTransformRetryCPU/8MiB/image_false/attempts_15/sparse$",
    }
    for round_index in range(1, 4):
        variants = ["merged", "baseline"] if round_index == 2 else ["baseline", "merged"]
        for variant in variants:
            for case, pattern in cases.items():
                output = output_dir / f"retry-bench-{case}-v4-{variant}-{round_index}.log"
                command = [str(binaries[variant]), "-test.run", "^$", "-test.bench", pattern,
                           "-test.benchtime=3s", "-test.count=1", "-test.benchmem"]
                with output.open("xb") as handle:
                    timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
                    header = (f"Started UTC: {timestamp}\nAffinity mask: {args.mask:#x}; "
                              f"GOMAXPROCS=4; order={variants}; round={round_index}\n")
                    handle.write(header.encode("utf-8"))
                    handle.flush()
                    subprocess.run(command, cwd=args.working_directory, env=env,
                                   stdout=handle, stderr=subprocess.STDOUT, check=True)
                with output.open(encoding="utf-8") as handle:
                    if not any(line.startswith("Benchmark") and "ns/op" in line for line in handle):
                        raise RuntimeError(f"No benchmark result was produced: {output}")


if __name__ == "__main__":
    main()
