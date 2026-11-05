# Request integrity and telemetry performance evidence

This revision records the pre-optimization baseline only.

## Reproduction

Run from the repository root with PowerShell:

```powershell
./backend/benchmarks/optimization-performance.ps1 -Label candidate
# Re-run the preserved baseline executable without rebuilding old source:
./backend/benchmarks/optimization-performance.ps1 -Label baseline -SkipBuild
```

The runner fixes `GOTOOLCHAIN=auto`, `GOEXPERIMENT=jsonv2`, and
`GOMAXPROCS=4`. The module selected Go 1.27.3 on Windows/amd64 with an AMD
Ryzen 7 9700X. Benchmark measurements use three repetitions and a 100 ms
target duration; the tables report medians. Inputs and telemetry markers are
synthetic, with no model or production traffic.

Raw output, environment details, preserved test binaries, and heap profiles
are retained locally in `.git/task-artifacts/performance-20261105/`.
`baseline-service.test.exe` was built before production optimization edits.

The integrity matrix measures construction of one request state followed by
one or five checks at 64 KiB, 1 MiB, 4 MiB, 8 MiB, and 8 MiB + 1 byte.
It includes content padding, ignored metadata padding, an independently
authorized model transformation, and a timezone-scanned body. Parallel cases
use independent request states at four logical processors.

The general matrix retains its original fixture: timezone cases with input
text above the scanner's 1 MiB limit exercise the limited-scan path.
`BenchmarkOptimizationTimezoneProjection` separately verifies an actual
frozen timezone transformation at each size by padding ignored metadata,
keeping both ingress and wire within the requested size. It compares the
frozen legacy checker and current checker in the same executable.

The telemetry benchmark isolates memory-store transaction overhead: each
pool has 1,000, 10,000, or 100,000 permanent thread markers and exactly 16
sampling transients; the callback only updates the pool's business timestamp.
This measures historical-activity transaction cost independently of runtime
scheduling and PostgreSQL execution.

## Baseline

| Case (constructor + 5 checks) | Time/op | B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| 64 KiB content | 482,569 ns | 1,253,616 | 209 |
| 4 MiB content | 30,157,075 ns | 79,700,236 | 245 |
| 4 MiB ignored metadata | 29,609,875 ns | 79,700,304 | 257 |
| 4 MiB model transformed | 29,479,325 ns | 79,715,608 | 525 |
| 8 MiB content | 59,808,250 ns | 159,391,984 | 251 |
| 8 MiB + 1 byte content (explicit skipped result) | 407.9 ns | 920 | 7 |
| Parallel 1 MiB model transformed | 3,027,041 ns | 19,946,815 | 513 |

| Memory-store transaction | Time/op | B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| 1,000 thread markers + 16 sampling rows | 656,637 ns | 1,224,341 | 11,223 |
| 10,000 thread markers + 16 sampling rows | 7,071,389 ns | 11,088,116 | 110,282 |
| 100,000 thread markers + 16 sampling rows | 129,118,700 ns | 102,379,224 | 1,100,739 |

## Process memory workload

The opt-in test holds eight independent 1 MiB request states, forces GC with
those states still reachable, and runs 400 checks concurrently in groups of
five. It records Go heap after GC, GC count, cumulative allocation, and the
largest heap observed by a 1 ms sampler. The PowerShell runner independently
samples process working set and private bytes every 10 ms. These are observed
sampled peaks; pprof's heap sizes are statistical allocation samples.

Baseline observed once:

| Measurement | Bytes / count |
| --- | ---: |
| Additional live Go heap for eight retained states after GC | 15,728,800 B |
| Live Go heap after concurrent checks and GC | 24,105,096 B |
| Sampled peak Go heap | 103,637,664 B |
| Cumulative allocation during workload | 1,293,682,920 B |
| GC cycles during workload | 32 |
| Sampled peak process working set | 135,356,416 B |
| Sampled peak process private bytes | 162,271,232 B |

The preserved baseline `inuse_space` profile attributes the largest retained
allocations to `bytes.Clone` and JSON token strings.

## Behavior coverage

`TestOptimizationIntegrityReference` compares every observation field with a
frozen pre-optimization checker over five repeated checks. Cases cover
unchanged input, metadata, authorized model and timezone rewrites, duplicate
keys, malformed ignored metadata, trailing tokens, numeric spelling,
normalization, preserved unknown fields, recovery annotations, and the depth
and node boundaries. The original baseline differential run passed.

Gateway streaming/cancellation and real PostgreSQL integration results are
recorded separately from these focused benchmarks. The process workload here
exercises checker retries and concurrency directly.
