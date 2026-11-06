# Memory performance measurements — 2026-11-06

Shared scheduling-cache savings grow with account reuse across groups. For 2,000 accounts, one-group retained heap changes +9.35%; the 100-group case changes -98.34%. SSE reduces per-frame allocation and repeated parsing, and ordinary queued billing records release full request/response bodies. The tables retain both improvements and regressions.

## Environment and measurement definitions

Initial cache/SSE/billing baseline test binaries were preserved from `8972274c2871fd961dd5a01d0a4c63b76e358add`, and candidate binaries from the memory-optimization working tree on that base. Later groups may use rebuilt binaries and retain their own manifests. The original runner's `source_head` records the checkout at measurement time; the new runner separates `binary_source` from `measurement_checkout_head`. Interpret provenance per build record: measurement checkout HEAD does not identify a preserved binary's source, and these phase measurements are not labelled as the final release commit. Linux amd64 (WSL), AMD Ryzen 7 9700X, Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`.

Each revision has three independent rounds: 100 iterations per cache fixture, 20 per SSE fixture, and one per billing fixture. Tables show baseline → candidate round medians and percentage changes. Spread is `(max − min)/|median| × 100%`; three rounds provide descriptive samples. A reported p95 median summarizes the three in-round p95 values rather than pooling their observations.

All round values, medians, spreads, and process resources are in the [full summary](../backend/benchmarks/results/memory-20261106/performance-summary.md) and [machine-readable JSON](../backend/benchmarks/results/memory-20261106/performance-summary.json).

## Scheduling cache

The same 2,000 accounts are shared by 1/10/100 groups. After constructing the Redis fixture, retained decoded heap is the HeapAlloc difference around explicit GC. Timed reads use the same reflection bridge and local miniredis access on both revisions. ops/s means complete candidate-set reads per second.

| Fixture | Retained MiB | KiB/op | ms/read | p95 ms | reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_1 | 6.21 → 6.791 (+9.35%) | 1,700 → 427.8 (-74.83%) | 3.126 → 2.168 (-30.66%) | 3.888 → 2.459 (-36.74%) | 319.9 → 461.3 (+44.23%) |
| accounts_2000_groups_10 | 85.47 → 10.63 (-87.56%) | 1,700 → 427.8 (-74.83%) | 3.283 → 2.066 (-37.07%) | 4.944 → 2.307 (-53.33%) | 304.6 → 484.1 (+58.91%) |
| accounts_2000_groups_100 | 2,661 → 44.13 (-98.34%) | 1,700 → 427.8 (-74.83%) | 2.859 → 2.098 (-26.61%) | 3.457 → 2.436 (-29.52%) | 349.8 → 476.6 (+36.26%) |

The one-group retained-heap increase reflects fixed master-index and lightweight-view costs. Allocated bytes per read still decline; allocation count is unchanged. Evaluate the one-group timing and spread alongside the multi-group results.

### Sharing ratios and warm/cold reads

This group fixes 2,000 unique accounts and 100 buckets. At 0%/50%/100% sharing, each bucket contains 20/1,010/2,000 members; nonshared accounts are distributed disjointly. An op reads all 100 buckets, so membership counts differ across sharing ratios. Warm mode performs a prior sweep; each cold sweep starts with an empty process cache and populated Redis, with explicit GC after each sweep excluded from timing. Each round has only three sweeps, so its p95 is its maximum sampled sweep.

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 22.32 → 22.35 (+0.12%) | 1.86 → 0.6123 (-67.09%) | 18,304 → 18,305 (+0.01%) | 22.9 → 25.39 (+10.86%) | 4,480 → 4,474 (-0.12%) |
| accounts_2000_groups_100_shared_0_cold | 67.34 → 80.11 (+18.97%) | 13.8 → 16.06 (+16.32%) | 143,549 → 161,334 (+12.39%) | 74.06 → 85.51 (+15.46%) | 1,485 → 1,248 (-15.94%) |
| accounts_2000_groups_100_shared_50_warm | 134 → 147.8 (+10.29%) | 83.56 → 21.06 (-74.80%) | 526,804 → 526,810 (+0.00%) | 156.9 → 152.6 (-2.75%) | 746 → 676.4 (-9.33%) |
| accounts_2000_groups_100_shared_50_cold | 7,025 → 2,868 (-59.18%) | 4,804 → 2,449 (-49.03%) | 8,006,086 → 2,183,582 (-72.73%) | 8,269 → 2,942 (-64.42%) | 14.24 → 34.87 (+144.97%) |
| accounts_2000_groups_100_shared_100_warm | 291.5 → 253.7 (-12.96%) | 166 → 41.78 (-74.83%) | 1,037,004 → 1,037,008 (+0.00%) | 323.7 → 267.3 (-17.41%) | 343 → 394.1 (+14.89%) |
| accounts_2000_groups_100_shared_100_cold | 19,939 → 5,814 (-70.84%) | 9,596 → 4,882 (-49.12%) | 15,871,464 → 4,207,103 (-73.49%) | 24,845 → 6,021 (-75.77%) | 5.015 → 17.2 (+242.95%) |

### sharing-zero100x

Separately retained follow-up group; iterations per fixture per round: [100].
Concurrent host workloads were observed; diagnostic only. 宿主机存在并发负载，仅诊断。

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 20.84 → 20.53 (-1.52%) | 1.86 → 0.5968 (-67.92%) | 18,304 → 18,204 (-0.55%) | 22.27 → 22.13 (-0.61%) | 4,798 → 4,872 (+1.55%) |
| accounts_2000_groups_100_shared_0_cold | 59.06 → 62.67 (+6.11%) | 13.8 → 13.61 (-1.36%) | 143,544 → 151,478 (+5.53%) | 62.43 → 65.21 (+4.44%) | 1,693 → 1,596 (-5.76%) |

| Fixture / metric (ms) | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| warm / ns/op | 20.842889 / 21.456081 / 8.741167; **20.84**; 61.00% | 20.525141 / 20.579614 / 20.134945; **20.53**; 2.17% |
| warm / p95-sweep-ns | 22.26547 / 22.55259 / 13.189276; **22.27**; 42.05% | 21.684164 / 22.130417 / 23.569886; **22.13**; 8.52% |
| cold / ns/op | 59.062927 / 58.866292 / 63.766596; **59.06**; 8.30% | 62.671973 / 61.528073 / 74.020949; **62.67**; 19.93% |
| cold / p95-sweep-ns | 61.709132 / 62.434407 / 71.859755; **62.43**; 16.26% | 65.209339 / 63.860169 / 83.087727; **65.21**; 29.49% |

### sharing-zero100x-exclusive

Separately retained follow-up group; iterations per fixture per round: [100].
Exclusive measurement window; 100 sweeps per fixture per round; retain round spread. 独占窗口，每轮100次，保留波动。

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 20.58 → 20.92 (+1.64%) | 1.86 → 0.5968 (-67.92%) | 18,304 → 18,204 (-0.55%) | 22.55 → 22.54 (-0.04%) | 4,858 → 4,780 (-1.62%) |
| accounts_2000_groups_100_shared_0_cold | 67.48 → 61.94 (-8.22%) | 13.8 → 13.61 (-1.35%) | 143,540 → 151,478 (+5.53%) | 77 → 69.31 (-9.98%) | 1,482 → 1,615 (+8.95%) |

| Fixture / metric (ms) | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| warm / ns/op | 21.262343 / 7.986307 / 20.582708; **20.58**; 64.50% | 21.062394 / 20.921251 / 18.218049; **20.92**; 13.60% |
| warm / p95-sweep-ns | 22.547052 / 11.71055 / 23.798268; **22.55**; 53.61% | 22.53797 / 22.018067 / 23.259194; **22.54**; 5.51% |
| cold / ns/op | 58.622385 / 70.561763 / 67.480509; **67.48**; 17.69% | 61.93693 / 60.103859 / 68.444126; **61.94**; 13.47% |
| cold / p95-sweep-ns | 61.406852 / 91.673395 / 76.999253; **77**; 39.31% | 64.476283 / 69.311305 / 74.265909; **69.31**; 14.12% |

### Host-contended canary (diagnostic only)

Concurrent host workload observed; diagnostic canary only.
This batch observed concurrent host workload and is diagnostic only; it is not pooled with the exclusive 100x retest.

| Fixture | Time | p95 | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 50_warm | -25.633% | -24.322% | -75.732% | -0.019% |
| 50_cold | -66.549% | -71.703% | -49.316% | -72.849% |
| 100_warm | +12.572% | +15.441% | -75.772% | -0.009% |
| 100_cold | -75.655% | -76.904% | -49.386% | -73.555% |

## SSE relay

All payloads are synthetic and the writer discards output; an op is one complete fixture stream. WS denotes Engine's HTTP/SSE-to-client WebSocket adapter; these fixtures do not create live WebSocket peers. Slow-write fixtures sleep 100 µs in each Write, including actual scheduler delay in the result.

| Fixture | ms/stream | MiB/op | allocs/op | p95 ms | streams/s |
|---|---:|---:|---:|---:|---:|
| HTTP/small_256 | 1.116 → 0.3288 (-70.55%) | 0.2055 → 0.01562 (-92.40%) | 3,893 → 304 (-92.19%) | 1.468 → 0.415 (-71.73%) | 895.8 → 3,042 (+239.54%) |
| WS/small_256 | 0.9074 → 0.3666 (-59.60%) | 0.2766 → 0.0239 (-91.36%) | 5,181 → 565 (-89.09%) | 1.044 → 0.4948 (-52.60%) | 1,102 → 2,728 (+147.54%) |
| HTTP/multiline_128 | 0.4449 → 0.1864 (-58.11%) | 0.14 → 0.01211 (-91.35%) | 2,356 → 181 (-92.32%) | 0.6398 → 0.2673 (-58.22%) | 2,248 → 5,365 (+138.70%) |
| WS/multiline_128 | 0.5169 → 0.2025 (-60.82%) | 0.1895 → 0.01643 (-91.33%) | 3,133 → 314 (-89.98%) | 0.6686 → 0.2579 (-61.43%) | 1,934 → 4,938 (+155.24%) |
| HTTP/slow_write_32 | 37.24 → 36.54 (-1.90%) | 0.03268 → 0.008892 (-72.79%) | 532 → 80 (-84.96%) | 37.67 → 36.77 (-2.40%) | 26.85 → 27.37 (+1.94%) |
| WS/slow_write_32 | 37.25 → 36.57 (-1.83%) | 0.04232 → 0.01024 (-75.81%) | 701 → 117 (-83.31%) | 37.82 → 36.73 (-2.88%) | 26.85 → 27.35 (+1.86%) |
| HTTP/terminal_1MiB | 13.63 → 6.624 (-51.40%) | 15.06 → 6.452 (-57.15%) | 144 → 86 (-40.28%) | 17.56 → 8.366 (-52.35%) | 73.37 → 151 (+105.77%) |
| WS/terminal_1MiB | 12.82 → 6.675 (-47.95%) | 17.07 → 6.453 (-62.21%) | 173 → 95 (-45.09%) | 16.58 → 7.873 (-52.50%) | 77.97 → 149.8 (+92.12%) |
| HTTP/terminal_8MiB | 92.38 → 49.44 (-46.49%) | 119.8 → 50.88 (-57.54%) | 153 → 95 (-37.91%) | 100.4 → 53.24 (-46.95%) | 10.82 → 20.23 (+86.87%) |
| WS/terminal_8MiB | 96.45 → 50.78 (-47.35%) | 135.8 → 50.88 (-62.54%) | 182 → 104 (-42.86%) | 105 → 53.07 (-49.47%) | 10.37 → 19.69 (+89.94%) |
| HTTP/terminal_32MiB | 390 → 199.1 (-48.95%) | 511.8 → 197 (-61.52%) | 160 → 101 (-36.88%) | 427.1 → 221.3 (-48.18%) | 2.564 → 5.023 (+95.88%) |
| WS/terminal_32MiB | 415.9 → 206.1 (-50.45%) | 575.9 → 197 (-65.80%) | 189 → 110 (-41.80%) | 450 → 220.6 (-50.97%) | 2.405 → 4.853 (+101.81%) |

### First write

First-write timing runs from local relay entry to the first writer return, including framing and observation. It is reported separately from complete-stream throughput.

| Fixture | First write µs | Total write ms |
|---|---:|---:|
| HTTP/small_256 | 16.9 → 17.13 (+1.37%) | 0.006897 → 0.005107 (-25.95%) |
| WS/small_256 | 11.63 → 20.15 (+73.35%) | 0.006678 → 0.005228 (-21.71%) |
| HTTP/multiline_128 | 10.94 → 13.88 (+26.82%) | 0.00321 → 0.00262 (-18.38%) |
| WS/multiline_128 | 9.68 → 13.13 (+35.64%) | 0.003406 → 0.002712 (-20.38%) |
| HTTP/slow_write_32 | 1,183 → 1,124 (-4.92%) | 36.62 → 36.23 (-1.08%) |
| WS/slow_write_32 | 1,156 → 1,163 (+0.60%) | 36.41 → 36.24 (-0.46%) |
| HTTP/terminal_1MiB | 75.34 → 46.45 (-38.35%) | 0.001097 → 0.0006211 (-43.38%) |
| WS/terminal_1MiB | 66.92 → 35.86 (-46.42%) | 0.001198 → 0.0005176 (-56.79%) |
| HTTP/terminal_8MiB | 71.58 → 57.76 (-19.31%) | 0.001021 → 0.001021 (+0.00%) |
| WS/terminal_8MiB | 78.07 → 65.48 (-16.12%) | 0.00109 → 0.000976 (-10.46%) |
| HTTP/terminal_32MiB | 78.82 → 65.68 (-16.67%) | 0.001024 → 0.0009384 (-8.36%) |
| WS/terminal_32MiB | 78.77 → 63.59 (-19.28%) | 0.001023 → 0.001071 (+4.69%) |

The startup cost is retained: WS small-frame median first write is 11.63 → 20.15 µs (+73.35%); HTTP/WS multiline first write changes +26.82%/+35.64%. These local microsecond-scale startup costs coexist with lower complete-stream duration.

## Ordinary billing backlog

One worker is blocked while 32/128 tasks each receive a distinct 1 MiB body. Retained bytes per task are measured after explicit GC. ns/op includes fixture construction, GC, queue drain and shutdown: it is experiment duration, and experiments/s is only its reciprocal. Neither is business latency or billing TPS. Cumulative B/op still includes initial body creation and therefore does not fall in proportion to retained heap.

Selected inputs for this section: `baseline-billing` → `candidate-typed-final-billing`.

Final typed ownership implementation; ordinary request/response body retention. 最终typed所有权实现。

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 3,770 (-99.64%) | 32.06 → 32.17 (+0.33%) | 11.63 → 14.32 (+23.13%) |
| ws_replay/queued=128 | 1,049,515 → 3,763 (-99.64%) | 128.2 → 128.6 (+0.33%) | 9.123 → 21.14 (+131.77%) |
| systemone_response/queued=32 | 1,049,267 → 3,516 (-99.66%) | 32.05 → 32.15 (+0.31%) | 4.534 → 7.908 (+74.43%) |
| systemone_response/queued=128 | 1,049,266 → 3,516 (-99.66%) | 128.1 → 128.5 (+0.31%) | 7.404 → 20.51 (+176.98%) |
| grok_request/queued=32 | 1,048,867 → 3,823 (-99.64%) | 32.04 → 32.15 (+0.34%) | 21.52 → 24.79 (+15.20%) |
| grok_request/queued=128 | 1,048,866 → 3,819 (-99.64%) | 128.1 → 128.5 (+0.34%) | 75.24 → 72.51 (-3.63%) |

### Earlier backlog experiment (retained separately)

`baseline-billing` → `candidate-billing`; diagnostic results from an earlier measurement, kept separate from the table above.

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 1,240 (-99.88%) | 32.06 → 32.09 (+0.09%) | 11.63 → 16.28 (+39.99%) |
| ws_replay/queued=128 | 1,049,515 → 1,235 (-99.88%) | 128.2 → 128.3 (+0.09%) | 9.123 → 19.7 (+115.94%) |
| systemone_response/queued=32 | 1,049,267 → 958.8 (-99.91%) | 32.05 → 32.07 (+0.07%) | 4.534 → 8.746 (+92.91%) |
| systemone_response/queued=128 | 1,049,266 → 955.6 (-99.91%) | 128.1 → 128.2 (+0.07%) | 7.404 → 20.78 (+180.68%) |
| grok_request/queued=32 | 1,048,867 → 654.8 (-99.94%) | 32.04 → 32.06 (+0.04%) | 21.52 → 25.63 (+19.10%) |
| grok_request/queued=128 | 1,048,866 → 651.6 (-99.94%) | 128.1 → 128.2 (+0.03%) | 75.24 → 75.74 (+0.66%) |

### Earlier backlog experiment (retained separately)

`baseline-billing` → `candidate-packed-billing`; diagnostic results from an earlier measurement, kept separate from the table above.

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 4,408 (-99.58%) | 32.06 → 32.19 (+0.40%) | 11.63 → 13.54 (+16.46%) |
| ws_replay/queued=128 | 1,049,515 → 4,406 (-99.58%) | 128.2 → 128.7 (+0.40%) | 9.123 → 17.04 (+86.79%) |
| systemone_response/queued=32 | 1,049,267 → 4,411 (-99.58%) | 32.05 → 32.18 (+0.40%) | 4.534 → 6.905 (+52.30%) |
| systemone_response/queued=128 | 1,049,266 → 4,412 (-99.58%) | 128.1 → 128.7 (+0.40%) | 7.404 → 18.62 (+151.44%) |
| grok_request/queued=32 | 1,048,867 → 4,459 (-99.57%) | 32.04 → 32.17 (+0.40%) | 21.52 → 22.62 (+5.10%) |
| grok_request/queued=128 | 1,048,866 → 4,460 (-99.57%) | 128.1 → 128.6 (+0.40%) | 75.24 → 70.61 (-6.16%) |

### Earlier backlog experiment (retained separately)

`baseline-billing` → `candidate-packed-final-billing`; diagnostic results from an earlier measurement, kept separate from the table above.

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 4,407 (-99.58%) | 32.06 → 32.19 (+0.40%) | 11.63 → 15.04 (+29.33%) |
| ws_replay/queued=128 | 1,049,515 → 4,404 (-99.58%) | 128.2 → 128.7 (+0.40%) | 9.123 → 17.29 (+89.57%) |
| systemone_response/queued=32 | 1,049,267 → 4,415 (-99.58%) | 32.05 → 32.18 (+0.39%) | 4.534 → 7.256 (+60.04%) |
| systemone_response/queued=128 | 1,049,266 → 4,412 (-99.58%) | 128.1 → 128.7 (+0.40%) | 7.404 → 18.06 (+143.87%) |
| grok_request/queued=32 | 1,048,867 → 4,459 (-99.57%) | 32.04 → 32.17 (+0.40%) | 21.52 → 22.78 (+5.86%) |
| grok_request/queued=128 | 1,048,866 → 4,460 (-99.57%) | 128.1 → 128.6 (+0.40%) | 75.24 → 72.84 (-3.19%) |

### Complete local lifecycle within the candidate revision

This group compares legacy_capture and owned_snapshot inside the same candidate test binary, not two commits. Sixteen producers, four workers and a 128-task queue copy/parse an 8 KiB/1 MiB body, use the actual worker pool, perform identical local price calculation and billing-command construction, then await completion. The timed section has no forced GC, sleeps or simulated network/database latency, and the fixture does not access a real database. Throughput uses parallel elapsed time; p95-e2e includes body handling, enqueue, worker work and wake-up. Parallel ns/op is not per-request latency.

| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |
|---|---:|---:|---:|---:|
| ws_replay/bytes=8192 | 177,080 → 138,437 (-21.82%) | 0.25 → 0.4413 (+76.53%) | 12.79 → 18.53 (+44.84%) | 22.42 → 28.77 (+28.35%) |
| ws_replay/bytes=1048576 | 3,634 → 3,471 (-4.49%) | 19.42 → 20.38 (+4.91%) | 1,029 → 1,035 (+0.56%) | 1,087 → 1,140 (+4.85%) |
| systemone_response/bytes=8192 | 181,147 → 152,664 (-15.72%) | 0.2312 → 0.3582 (+54.96%) | 12.52 → 18.03 (+44.07%) | 21.49 → 25.77 (+19.89%) |
| systemone_response/bytes=1048576 | 3,697 → 3,721 (+0.65%) | 17.66 → 18.79 (+6.43%) | 1,029 → 1,034 (+0.54%) | 1,060 → 1,063 (+0.32%) |
| grok_request/bytes=8192 | 151,419 → 119,003 (-21.41%) | 0.2972 → 0.4369 (+46.97%) | 13.13 → 18.94 (+44.29%) | 25.73 → 32.89 (+27.84%) |
| grok_request/bytes=1048576 | 2,533 → 2,661 (+5.05%) | 22.61 → 25.74 (+13.80%) | 1,029 → 1,035 (+0.56%) | 1,569 → 1,486 (-5.28%) |

Raw-file prefix: `candidate-typed-final-lifecycle`.

### Earlier lifecycle diagnostic (retained separately)

The earlier results from `candidate-lifecycle` remain below. Each comparison is between two modes within its own binary and is kept separate from the selected final group.

Earlier deep-copy projection; diagnostic history. 旧深复制投影，仅保留诊断。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -23.80% | +58.26% | +63.96% |
| ws_replay/bytes=1048576 | +8.89% | -3.97% | +0.80% |
| systemone_response/bytes=8192 | -38.72% | +111.59% | +63.36% |
| systemone_response/bytes=1048576 | +2.72% | -5.98% | +0.77% |
| grok_request/bytes=8192 | -31.34% | +56.10% | +62.80% |
| grok_request/bytes=1048576 | -4.73% | +17.34% | +0.80% |

### Earlier lifecycle diagnostic (retained separately)

The earlier results from `candidate-packed-lifecycle` remain below. Each comparison is between two modes within its own binary and is kept separate from the selected final group.

Packed projection with overlapping host test work; diagnostic only. packed首批有宿主测试竞争，仅诊断。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -26.06% | +59.07% | +48.57% |
| ws_replay/bytes=1048576 | +6.55% | -7.59% | +0.60% |
| systemone_response/bytes=8192 | +22.69% | -34.59% | +49.88% |
| systemone_response/bytes=1048576 | -2.75% | +14.02% | +0.61% |
| grok_request/bytes=8192 | -21.51% | +38.73% | +47.92% |
| grok_request/bytes=1048576 | -0.30% | +19.33% | +0.61% |

### Earlier lifecycle diagnostic (retained separately)

The earlier results from `candidate-packed-final-lifecycle` remain below. Each comparison is between two modes within its own binary and is kept separate from the selected final group.

Exclusive packed projection before typed Account and published auth Group optimization. typed和auth Group优化前的独占packed批。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -18.36% | +60.19% | +48.57% |
| ws_replay/bytes=1048576 | +2.12% | +3.20% | +0.60% |
| systemone_response/bytes=8192 | -16.17% | +58.53% | +49.88% |
| systemone_response/bytes=1048576 | +1.05% | -1.49% | +0.61% |
| grok_request/bytes=8192 | -20.58% | +31.10% | +47.92% |
| grok_request/bytes=1048576 | +1.13% | +18.86% | +0.61% |

### Lifecycle microbenchmark with real authentication L1

This group separately measures the real `APIKeyService.GetByKey` L1-hit path and subsequent worker lifecycle. Every timed request authenticates; a real cache miss publishes the independent price projection before timing, with a repository double only at the storage boundary. It compares legacy_capture/owned_snapshot within one binary, using 16 producers and four workers, without an HTTP handler.

| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |
|---|---:|---:|---:|---:|
| ws_replay/bytes=8192 | 161,276 → 138,682 (-14.01%) | 0.2936 → 0.4246 (+44.61%) | 15.44 → 19.02 (+23.19%) | 24.49 → 28.34 (+15.69%) |
| ws_replay/bytes=1048576 | 3,355 → 3,625 (+8.05%) | 20.14 → 18.9 (-6.14%) | 1,032 → 1,035 (+0.35%) | 1,161 → 1,093 (-5.83%) |
| systemone_response/bytes=8192 | 159,790 → 143,091 (-10.45%) | 0.34 → 0.4273 (+25.67%) | 15.16 → 18.52 (+22.16%) | 24.9 → 27.47 (+10.33%) |
| systemone_response/bytes=1048576 | 3,782 → 3,446 (-8.88%) | 17.91 → 19.76 (+10.35%) | 1,031 → 1,035 (+0.33%) | 1,048 → 1,140 (+8.84%) |
| grok_request/bytes=8192 | 118,057 → 110,140 (-6.71%) | 0.4872 → 0.5195 (+6.64%) | 15.77 → 19.42 (+23.09%) | 33.72 → 36.12 (+7.13%) |
| grok_request/bytes=1048576 | 2,440 → 2,531 (+3.73%) | 23 → 27.62 (+20.10%) | 1,032 → 1,036 (+0.35%) | 1,615 → 1,561 (-3.33%) |

Raw-file prefix: `candidate-typed-final-authenticated`.

### HTTP request and billing settlement lifecycle

Sixteen clients form one closed loop: each waits for both HTTP EOF and UsageLog.Create settlement before issuing another request. This group is separate from service-level fallback/authenticated microbenchmarks. It uses loopback transports and storage-boundary doubles, so it does not measure PostgreSQL performance. GNU time RSS/CPU covers both payload sizes, calibration and warmup.

| Fixture | HTTP req/s | Settled req/s | HTTP p95 ms | Settled p95 ms | Runtime CPU ms/request | Allocated MiB/op |
|---|---:|---:|---:|---:|---:|---:|
| 8192 bytes | 2,403 → 2,426 (+0.96%) | 2,403 → 2,426 (+0.96%) | 11.4 → 11.25 (-1.37%) | 10.78 → 10.59 (-1.73%) | 1.66 → 1.637 (-1.42%) | 0.7735 → 0.7464 (-3.49%) |
| 1048576 bytes | 72.34 → 74.97 (+3.64%) | 72.34 → 74.97 (+3.64%) | 326.6 → 318 (-2.62%) | 321.9 → 312.9 (-2.81%) | 55.02 → 53.11 (-3.47%) | 39.84 → 39.82 (-0.06%) |

Selected HTTP batch: confirm10s. At 8 KiB, HTTP p95 changes -1.371289%, settled p95 -1.732854% and CPU/request -1.419328%; the worst 1 MiB p95/CPU change is -2.621112%. Both sizes remain within 5% for p95/CPU medians in this batch.

| HTTP fixture / metric | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| request_bytes=8192 / p95-http-ns (ms) | 11.402706 / 10.990988 / 11.653247; **11.4**; 5.81% | 11.4803 / 11.246342 / 10.826938; **11.25**; 5.81% |
| request_bytes=8192 / p95-settled-ns (ms) | 10.780941 / 10.32294 / 10.896356; **10.78**; 5.32% | 10.679963 / 10.594123 / 10.145628; **10.59**; 5.04% |
| request_bytes=8192 / runtime-cpu-ns/request (ms) | 1.660434 / 1.599438 / 1.690322; **1.66**; 5.47% | 1.641683 / 1.636867 / 1.592631; **1.637**; 3.00% |
| request_bytes=1048576 / p95-http-ns (ms) | 326.565664 / 332.303579 / 313.960807; **326.6**; 5.62% | 408.710322 / 318.006013 / 307.768302; **318**; 31.74% |
| request_bytes=1048576 / p95-settled-ns (ms) | 321.947293 / 328.298032 / 305.605031; **321.9**; 7.05% | 394.012322 / 312.90534 / 301.79565; **312.9**; 29.47% |
| request_bytes=1048576 / runtime-cpu-ns/request (ms) | 55.022514 / 55.596185 / 52.171344; **55.02**; 6.22% | 57.100847 / 53.113331 / 52.012129; **53.11**; 9.58% |

#### HTTP history: original (2s)

Earlier batch retained separately; not pooled with the selected batch.

| Fixture | CPU change | HTTP p95 change | Settled p95 change |
|---|---:|---:|---:|
| request_bytes=8192 | +5.36% | +5.00% | +6.61% |
| request_bytes=1048576 | -0.00% | +3.79% | +2.61% |

CPU attribution comes from a separate profiled 8 KiB run, outside the benchmark medians. The candidate profile attributes 0.42 seconds / 0.47% cumulative samples to the complete synchronous submitFrozenOpenAIUsageRecord construction-and-enqueue path; SnapshotOpenAIRecordUsageInput accounts for 0.25 seconds / 0.28%. The 0.47% figure is not pure construction. The full path spends most time in HTTP/JSON parsing and system calls; these shares describe that profile run.
[Candidate CPU top](../backend/benchmarks/results/memory-20261106/http-cpu-final-top.txt), [baseline CPU top](../backend/benchmarks/results/memory-20261106/http-cpu-baseline-top.txt), [construction and enqueue detail](../backend/benchmarks/results/memory-20261106/http-cpu-final-enqueue.txt).
Raw .prof files remain in local task artifacts; the delivery contains their text output only.

## Whole-phase process resources

GNU time covers the entire phase process, including all sub-benchmarks, fixture preparation, warmup, GC and teardown. Maximum RSS has a different scope from retained Go heap above; total CPU is the median of per-round user-plus-system sums.

| Phase | Peak RSS MiB | User CPU s | System CPU s | Total CPU s | Wall s |
|---|---:|---:|---:|---:|---:|
| cache | 4,407 → 368.9 (-91.63%) | 41.14 → 7.72 (-81.23%) | 15.58 → 8.47 (-45.64%) | 56.72 → 16.46 (-70.98%) | 42.9 → 11.47 (-73.26%) |
| sse | 556.6 → 369.8 (-33.56%) | 21.07 → 11.02 (-47.70%) | 2.46 → 0.87 (-64.63%) | 23.53 → 11.91 (-49.38%) | 23.58 → 12.88 (-45.38%) |
| billing | 56.09 → 56.69 (+1.07%) | 0.2 → 0.25 (+25.00%) | 0.12 → 0.14 (+16.67%) | 0.31 → 0.39 (+25.81%) | 0.5 → 0.45 (-10.00%) |
| sharing | 4,291 → 370.9 (-91.36%) | 144 → 44.35 (-69.21%) | 57.19 → 44.3 (-22.54%) | 205.3 → 89.35 (-56.48%) | 159.1 → 63.54 (-60.07%) |
| sharing-zero100x | 118.7 → 116.4 (-1.92%) | 4.89 → 4.94 (+1.02%) | 6.34 → 6.73 (+6.15%) | 11.34 → 11.7 (+3.17%) | 9.74 → 10 (+2.67%) |
| sharing-zero100x-exclusive | 118.1 → 117.4 (-0.58%) | 5.1 → 5.16 (+1.18%) | 6.81 → 6.75 (-0.88%) | 11.38 → 11.69 (+2.72%) | 9.68 → 9.97 (+3.00%) |
| http-lifecycle | 173.6 → 178.2 (+2.69%) | 101.7 → 108.7 (+6.89%) | 16.15 → 18.52 (+14.67%) | 117.8 → 127.2 (+7.95%) | 34.1 → 36.83 (+8.01%) |

The billing experiment process changes median maximum RSS by +1.07% and total CPU seconds by +25.81% alongside lower backlogged retained heap. It still constructs every 1 MiB body and runs explicit GC; the resource table includes those costs.

Each lifecycle process runs both ownership modes. Its process resources are reported together, without attributing them to either mode.

| Lifecycle resource | Round 1 / 2 / 3; median; spread |
|---|---|
| max_rss_KiB | 115380 / 121128 / 110136; **115,380**; 9.53% |
| user_CPU_s | 120.28 / 110.81 / 136.59; **120.3**; 21.43% |
| system_CPU_s | 6.42 / 5.27 / 7.11; **6.42**; 28.66% |
| user_plus_system_CPU_s | 126.7 / 116.08 / 143.7; **126.7**; 21.80% |
| wall_s | 33.96 / 31.11 / 38.49; **33.96**; 21.73% |

Authenticated microbenchmark process resources likewise cover both modes and remain separate.

| Authenticated resource | Round 1 / 2 / 3; median; spread |
|---|---|
| max_rss_KiB | 112816 / 109132 / 138408; **112,816**; 25.95% |
| user_CPU_s | 129.12 / 148.78 / 115.3; **129.1**; 25.93% |
| system_CPU_s | 5.49 / 6.06 / 5.44; **5.49**; 11.29% |
| user_plus_system_CPU_s | 134.61 / 154.84 / 120.74; **134.6**; 25.33% |
| wall_s | 35.75 / 41.44 / 32.11; **35.75**; 26.10% |

## Executed verification

Validation logs are separate from the performance binaries. Integration coverage includes shared-cache cross-instance authority and Redis reconnect, group retirement/reopen epochs, snapshots/outbox, scheduling projections, Engine atomic settlement recovery and concurrency, ordinary billing deduplication and detached request-lifetime settlement. Redis reconnect and cache-not-ready messages are deliberately injected test scenarios.

| Scope | Executed command | Environment and evidence |
|---|---|---|
| Repository integration / real PostgreSQL and Redis | <code>CI=true GOMAXPROCS=4 integration-repository.test '-test.run=Test(Scheduler&#124;UsageRecordSnapshotDetached&#124;UsageBillingRepositoryApply&#124;CodexEngineBillingStore&#124;ListSchedulableAccountLoads)' -test.v -test.timeout=15m</code> | Earlier Linux/amd64 integration binary compiled with -tags=integration; testcontainers PostgreSQL 18.1-alpine3.23 and Redis 8.4-alpine; final rerun is listed separately; [PASS](../backend/benchmarks/results/memory-20261106/verification-integration.txt) |
| Frontend locale completeness, TypeScript and production build | <code>pnpm run check:i18n && vue-tsc -b && vite build</code> | frontend directory; invoked by the package build script; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-build.txt) |
| Memory dashboard component tests | <code>pnpm exec vitest run src/views/admin/ops/components/__tests__/OpsMemory.spec.ts</code> | frontend directory; FastCtx job j-puxjvd started 2026-11-06T15:32:12Z; Vitest 2.1.9, 1 file and 7 tests passed, tests 439 ms, total 4.00 s; original job output copied after checking for sensitive content; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-components.txt) |
| Targeted frontend ESLint | <code>pnpm exec eslint src/views/admin/ops/OpsDashboard.vue src/views/admin/ops/components/OpsMemory.vue src/views/admin/ops/components/__tests__/OpsMemory.spec.ts src/api/admin/ops.ts src/i18n/locales/en/admin/ops.ts src/i18n/locales/zh/admin/ops.ts</code> | frontend directory; FastCtx job j-hhz78g exit 0, no diagnostics; result recorded by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-lint.json) |
| Handler billing error request-ID logging race regression | <code>GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage&#124;WrapUsage&#124;UsageRecord)' -count=1 -timeout=5m</code> | backend directory; after restoring request_id on the WebSocket billing error branch; FastCtx job j-nnqqjs exit 0 confirmed by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-billing-log-trace-race.txt) |
| Final unit | <code>python backend/benchmarks/verify-memory.py --phase unit</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-unit.txt) |
| Final race | <code>python backend/benchmarks/verify-memory.py --phase race</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-race.txt) |
| Final build | <code>python backend/benchmarks/verify-memory.py --phase build</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-build.txt) |
| Final integration | <code>python backend/benchmarks/verify-memory.py --phase integration</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-integration.txt) |
| Final handler billing error tracing race regression | <code>GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage&#124;WrapUsage&#124;UsageRecord)' -count=1 -timeout=5m</code> | backend directory; WebSocket error logging restores request_id/model from the owned Result and gateway_request_id/client_request_id from the independent worker context; FastCtx job j-momlwb exit 0 confirmed by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-billing-final-log-trace-race.txt) |

Final integration also includes a real PostgreSQL user-row lock: after confirming that the worker waits on the lock, the test cancels the parent request and clears the original inputs, releases the lock, then verifies one debit, cache synchronization, reservation release and idempotent replay.

After the full unit/race/build/integration checks, the final change restored WebSocket billing error log fields: request_id/model from the owned Result and gateway_request_id/client_request_id from the independent worker context. The successful-request path is unchanged. The same targeted handler race command then passed in 6.514 seconds; both logging regression records are retained.

Separately recorded existing failure: the baseline binary preserved from `8972274c2` reproduces `TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown/missing_configured_proxy`. The assertion expects `GROK_QUOTA_TOKEN_UNAVAILABLE` and receives `ACCOUNT_PROXY_UNAVAILABLE`. The assertion remains unchanged; see the [failure log](../backend/benchmarks/results/memory-20261106/verification-baseline-known-grok-failure.txt).

The frontend build ran locale completeness, TypeScript and Vite production build checks. Its log retains stale Browserslist data, mixed dynamic/static import and bundle-size notices. Lint was not part of this command.

## Upgrade, observe and roll back

1. Preserve the current backend/frontend release artifacts and configuration. Update one instance first and drain ordinary billing tasks through the normal shutdown flow. These memory changes add no database migration and leave the shared Redis snapshot format unchanged. The decoded process cache starts empty.
2. `gateway.scheduling.snapshot_cache_max_bytes` is the per-process soft budget for account masters and bucket indexes: 64 MiB by default, with 0 retaining that default. Keep the existing admission, billing queue and overflow settings; compare cache hits, estimated bytes, evictions and latency at the same load. Snapshot reuse is bounded to 10 seconds, idle eviction to 100 seconds, with cleanup every 10 seconds.
3. As an administrator, open memory observation in Operations with the existing monitoring switch enabled. Select the current instance ID and observe RSS, HeapAlloc/Inuse/Idle/Released, GC, goroutines, caches, active bodies/SSE and queue metrics. Samples arrive every 10 seconds; each instance keeps 360 samples (about one hour), and becomes stale after 30 seconds without a sample. Compare the same instance, workload and window; process restart creates a new ID and resets cumulative counters.
4. After streams or requests finish, `active_sse_buffers`/`active_sse_buffer_bytes` and active-body gauges should fall with active work; queue waiting/running should drain with load. Inspect dropped tasks, synchronous fallbacks, wait/execution histograms and settlement-failure deltas before extending the rollout.
5. For a heap profile, connect to the backend serving the selected instance, enable download, download it for analysis, then disable access. Admin APIs are `/api/v1/admin/ops/runtime/memory` and `/api/v1/admin/ops/runtime/heap-profile/{enable,disable,download}`. Enable/disable JSON and the download query require `instance_id`; only the responding process accepts the operation, with 409 on a process mismatch. Downloads start disabled, expire after 30 minutes and allow one download per process. They stream the existing sampled profile without forcing GC, changing MemProfileRate or creating temporary files.
6. To roll back, stop routing new traffic to the instance, use normal shutdown to finish active streams and billing work, then restore the preserved backend/frontend artifacts and configuration. Restart clears the local cache. Shared Redis snapshots remain usable and observation records expire by TTL; no Redis flush is needed.

Queue semantics: `completed` and `failed` describe worker closure execution; `submitted`, `dropped` and `sync_fallback` describe scheduling. Ordinary `RecordUsage` return outcomes increment `settlement_succeeded`/`settlement_failed` separately, including successful idempotent deduplication. Engine ordinary-billing no-ops are excluded; its durable ledger is observed separately. A closure can return normally after a settlement error, so inspect both groups. The wait histogram covers queued tasks; the execution histogram also covers synchronous fallbacks.

Each published authentication L1 entry additionally owns an independent billing-only Group price projection, which queued tasks reuse. Group objects outside an authentication-cache publication keep the per-submission deep-copy path. The projection retains none of the authentication Group's maps or price pointers. L2 JSON stays unchanged and reconstructs the projection after decoding. Price changes publish a new version through existing L1/L2 invalidation, PubSub and TTL behavior; already queued work keeps its prior version. This residency is bounded by the `api_key_auth_cache.l1_size` entry limit and is outside `scheduler_accounts`/`scheduler_cache_total` estimates and the scheduler's 64 MiB budget.

## Scope and remaining coverage

Performance measurements use the fixed synthetic fixtures above. Cache benchmarks use miniredis; SSE writers discard output and first-write time ends at the local writer return; lifecycle fixtures perform local price calculation and billing-command construction. Real PostgreSQL/Redis are used by the separate correctness integration tests. Real models, external networks, TLS, database throughput and sustained production traffic are outside these timed fixtures.

The cache soft budget uses estimated object bytes, a different quantity from HeapAlloc or RSS. Active caller views may retain an evicted master. The 10-second snapshot reuse limit preserves the visibility window for writes that do not advance revisions. Initial sharing-ratio runs have only three sweeps per round; follow-up measurements retain separate sample counts and binary provenance.

Runtime observation stores numeric snapshots. Redis records expire after 70 minutes and register up to 128 processes, showing truncated when capacity is reached. The current instance retains local history if Redis is unavailable. Heap profiles represent one process's sampled heap; handle downloaded files under the existing administrator access policy.

## Rebuild the summary from raw files

The script reads saved results and does not execute benchmarks. The delivered evidence directory contains only small txt/json/md inputs and summaries, without test binaries.

```sh
python backend/benchmarks/summarize-memory.py --source backend/benchmarks/results/memory-20261106 --output-dir .git/task-artifacts/memory-20261106 --phases cache sse billing sharing sharing-zero100x sharing-zero100x-exclusive --include-lifecycle --lifecycle-prefix candidate-typed-final-lifecycle --authenticated-prefix candidate-typed-final-authenticated --http-prefix candidate --http-tag confirm10s --http-history-tag original --lifecycle-history-prefix candidate-lifecycle --lifecycle-history-prefix candidate-packed-lifecycle --lifecycle-history-prefix candidate-packed-final-lifecycle --lifecycle-note 'candidate-lifecycle=Earlier deep-copy projection; diagnostic history. 旧深复制投影，仅保留诊断。' --lifecycle-note 'candidate-packed-lifecycle=Packed projection with overlapping host test work; diagnostic only. packed首批有宿主测试竞争，仅诊断。' --lifecycle-note 'candidate-packed-final-lifecycle=Exclusive packed projection before typed Account and published auth Group optimization. typed和auth Group优化前的独占packed批。' --candidate-label billing=candidate-typed-final --history-phase billing=candidate --history-phase billing=candidate-packed --history-phase billing=candidate-packed-final --phase-note 'sharing-zero100x=Concurrent host workloads were observed; diagnostic only. 宿主机存在并发负载，仅诊断。' --phase-note 'sharing-zero100x-exclusive=Exclusive measurement window; 100 sweeps per fixture per round; retain round spread. 独占窗口，每轮100次，保留波动。' --phase-note 'billing=Final typed ownership implementation; ordinary request/response body retention. 最终typed所有权实现。'
```

Sharing-ratio and lifecycle measurements are separate groups with their own operation units and scope. This document reports saved measurements; acceptance also depends on protocol, correctness and performance review.

### Reproduce performance and regression checks

The performance runner fixes Go/GOEXPERIMENT/GOMAXPROCS and captures each round with GNU time. Run it in an exclusive CPU window and use a new label to preserve earlier runs. Use `--skip-build` only with the matching preserved test binary and an accurate `--binary-source`. Build the baseline from baseline production sources plus the identical benchmark fixture; do not label a current candidate build as baseline.

```sh
python backend/benchmarks/memory-performance.py --label rerun --phase all
python backend/benchmarks/memory-performance.py --label rerun --phase sharing
python backend/benchmarks/memory-performance.py --label rerun --phase lifecycle
python backend/benchmarks/verify-memory.py --phase unit
python backend/benchmarks/verify-memory.py --phase race
python backend/benchmarks/verify-memory.py --phase build
python backend/benchmarks/verify-memory.py --phase integration
```
