# Request integrity and telemetry performance evidence

The final comparison shows lower allocation for repeated identical bodies
and frozen timezone rewrites, and removes historical-activity loading from
small telemetry transactions. At 100,000 permanent PostgreSQL markers, the
measured transaction falls from 273.15 ms / 94.38 MB to 7.53 ms / 20.2 KB.
The content-heavy model-transformation workload retains essentially the same
live heap and allocated bytes; results below distinguish those workloads.

Deployment ordering, rollback, and the separate image-model change are
documented in [UPGRADE-20261105.md](UPGRADE-20261105.md).

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

## Request integrity comparison

Each cell is **baseline → candidate**. These are three-run medians from the
saved pre-optimization binary and the final candidate binary. Full raw data,
including one-check cases and every size, is in `baseline-bench.txt`,
`candidate-bench.txt`, and `comparison-medians.json`.

| Case (constructor + 5 checks) | Time/op | B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| 64 KiB identical content | 482,569 → 84,033 ns | 1,253,616 → 264,032 | 209 → 39 |
| 4 MiB identical content | 30,157,075 → 4,709,528 ns | 79,700,236 → 16,779,104 | 245 → 45 |
| 4 MiB identical ignored metadata | 29,609,875 → 4,841,576 ns | 79,700,304 → 16,779,120 | 257 → 47 |
| 4 MiB model transformed | 29,479,325 → 25,206,325 ns | 79,715,608 → 79,717,224 | 525 → 525 |
| 8 MiB identical content | 59,808,250 → 10,080,880 ns | 159,391,984 → 33,556,320 | 251 → 46 |
| 8 MiB model transformed | 50,640,150 → 53,228,850 ns | 159,407,384 → 159,409,000 | 531 → 531 |
| 8 MiB + 1 byte content (explicit skipped result) | 407.9 → 360.6 ns | 920 → 936 | 7 → 7 |
| Parallel 1 MiB model transformed | 3,027,041 → 2,309,933 ns | 19,946,815 → 19,948,426 | 513 → 513 |

Identical 4 MiB bodies with five checks allocate 78.9% fewer bytes and take
84.4% less time in this run. For large content with a changed model, allocated
bytes remain approximately constant; the 8 MiB median time increased 5.1%.

The supplemental timezone comparison runs the frozen legacy checker and
current checker in the same final executable, with actual transformations
verified before timing. Metadata padding holds inspected text below the
timezone scanner limit. Both ingress and wire fit the stated body size.

| Actual frozen timezone rewrite, 5 checks | Time/op, legacy → current | B/op, legacy → current | Allocs/op |
| --- | ---: | ---: | ---: |
| 4 MiB | 77,755,550 → 52,246,333 ns | 163,647,408 → 79,756,096 | 1,578 → 1,378 |
| 8 MiB | 146,667,000 → 106,091,700 ns | 327,224,304 → 159,446,960 | 1,579 → 1,374 |

These actual timezone rewrites allocate about 51.3% fewer bytes. The older
`candidate-timezone.txt` exploratory run is superseded by the final values in
`candidate-bench.txt`.

## Memory-store transaction comparison

| Memory-store transaction | Time/op, baseline → candidate | B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| 1,000 thread markers + 16 sampling rows | 656,637 → 278.6 ns | 1,224,341 → 512 | 11,223 → 6 |
| 10,000 thread markers + 16 sampling rows | 7,071,389 → 230.2 ns | 11,088,116 → 512 | 110,282 → 6 |
| 100,000 thread markers + 16 sampling rows | 129,118,700 → 225.6 ns | 102,379,224 → 512 | 1,100,739 → 6 |

The callback changes only the pool business timestamp. This isolates the
eliminated whole-map clone/validation cost; PostgreSQL mutation and actual
runtime Begin/Finish measurements follow.

## PostgreSQL comparison

`BenchmarkCodexTelemetryPostgresHistory` ran under WSL Ubuntu 24.04 on
Linux/amd64, with Docker 29.4 and temporary testcontainers instances of
`postgres:18.1-alpine3.23` and `redis:8.4-alpine`, using the same Go toolchain,
JSON experiment, and four-processor limit. It used three repetitions of ten
transactions per case. Permanent history is half `thread` and half `metrics` rows, with 32
fixed transient `turn` rows. Every transaction reads and updates one existing
turn; fixture insertion, EXPLAIN, and cleanup are outside the measured loop.

| Permanent history | Time/op, baseline → candidate | B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| 1,000 | 10,238,351 → 6,979,965 ns | 1,258,064 → 20,211 | 13,767 → 348 |
| 10,000 | 23,804,249 → 6,802,088 ns | 10,727,568 → 20,201 | 130,861 → 348 |
| 100,000 | 273,147,438 → 7,527,087 ns | 94,383,381 → 20,208 | 1,301,537 → 347 |

| Permanent history | Queries/op, baseline → candidate | Rows/op | Returned B/op |
| --- | ---: | ---: | ---: |
| 1,000 | 8 → 9 | 1,036 → 6 | 55,225 → 267 |
| 10,000 | 8 → 9 | 10,036 → 6 | 536,726 → 267 |
| 100,000 | 8 → 9 | 100,036 → 6 | 5,441,727 → 267 |

The driver wrapper counts query/exec calls, returned rows, and the lengths of
returned string/byte-slice values. Returned bytes exclude protocol framing and
scalar values. EXPLAIN confirms that baseline pool loading returns the whole
activity history. The final 100,000-marker point lookup uses the existing
composite primary key. The final due query still selects the older due index
with Incremental Sort; index selection varies by plan and cardinality. The
new transaction obtains only the requested activity instead of materializing
history. Raw measurements and complete plans are in `baseline-postgres.txt`,
`baseline-postgres-explain.txt`, and `candidate-postgres.txt`.

The actual asynchronous service `Begin` + `Finish` integration test also
measured the final PostgreSQL implementation under 1,000 and 100,000 history
markers:

| History | Query/exec calls | Returned rows | Returned value bytes |
| --- | ---: | ---: | ---: |
| 1,000 | 37 | 18 | 25,521 |
| 100,000 | 38 | 19 | 25,545 |

Its asynchronous notifications can repeat a small amount of work, accounting
for the one-call/one-row difference. Returned data and query counts stay at
the same scale as permanent history grows 100-fold. These measurements are
in `candidate-postgres-tests.txt` and exercise actual runtime scheduling,
unlike the small transaction benchmark.

The equivalent benchmark command, run from `backend` on a Docker-capable
Linux host, is:

```bash
CI=true GOTOOLCHAIN=auto GOEXPERIMENT=jsonv2 GOMAXPROCS=4 \
  go test -tags=integration ./internal/repository -run '^$' \
  -bench '^BenchmarkCodexTelemetryPostgresHistory$' -benchmem -v \
  -benchtime=10x -count=3
```

The actual run used a separately saved `baseline-repository.test` executable
compiled before the storage refactor. The harness automatically stops its
temporary containers after execution.

## Process memory workload

The opt-in test holds eight independent 1 MiB request states, forces GC with
those states still reachable, and runs 400 checks concurrently in groups of
five. It records Go heap after GC, GC count, cumulative allocation, and the
largest heap observed by a 1 ms sampler. The PowerShell runner independently
samples process working set and private bytes every 10 ms. These are observed
sampled peaks; pprof's heap sizes are statistical allocation samples.

Each process workload was observed once. It uses content-heavy model
transformation, so its checks cannot use the identical-byte fast path.

| Measurement | Baseline | Candidate |
| --- | ---: | ---: |
| Additional live Go heap for eight retained states after GC | 15,728,800 B | 15,728,928 B |
| Live Go heap after concurrent checks and GC | 24,105,096 B | 24,106,000 B |
| Sampled peak Go heap | 103,637,664 B | 98,570,720 B |
| Cumulative allocation during workload | 1,293,682,920 B | 1,293,812,312 B |
| GC cycles during workload | 32 | 31 |
| Sampled peak process working set | 135,356,416 B | 145,854,464 B |
| Sampled peak process private bytes | 162,271,232 B | 173,449,216 B |

Retained heap and cumulative allocation are essentially unchanged in this
workload. The sampled Go heap peak is lower and sampled OS process peaks are
higher in the candidate run. Both `inuse_space` profiles retain most request
memory in `bytes.Clone` and JSON token strings. Lower allocation in the
identical-body and timezone benchmarks is a workload-specific result.

## Behavior coverage

`TestOptimizationIntegrityReference` compares every observation field with a
frozen pre-optimization checker over five repeated checks. Cases cover
unchanged input, metadata, authorized model and timezone rewrites, duplicate
keys, malformed ignored metadata, trailing tokens, numeric spelling,
normalization, preserved unknown fields, recovery annotations, and the depth
and node boundaries. Both baseline and final candidate differential runs
passed; candidate coverage includes all four actual-timezone body sizes.

Both saved executables passed six existing mock gateway
regressions: encrypted-content recovery per physical attempt, account failover
with frozen observation state, observation toggle payload/response equivalence,
client cancellation after partial streamed output, HTTP stream relay over the
WebSocket bridge, and five WebSocket retries followed by HTTP fallback. The
runner repeats the same selection for candidates; raw output is in
`baseline-gateway-regression.txt` and `candidate-gateway-regression.txt`.
These are behavior regressions; the process memory workload exercises
checker retries and concurrency directly.

The final expanded integrity/gateway/WebSocket/image/reference suite passed,
as did `go build ./...`, focused service race tests (24.158 s), and PostgreSQL
telemetry, account deletion, migration 275, and repeated-startup integration
tests. The integration run executed against temporary databases and completed
without skipped or failed cases. Relevant raw logs include
`candidate-race-tests.txt` and `candidate-postgres-tests.txt`.
