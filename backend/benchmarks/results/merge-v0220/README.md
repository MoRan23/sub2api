# v0.2.20 merge regression measurements

This compares the already optimized local parent `b41bb98fb` with the v0.2.20
merge candidate. It is not a comparison against the older unoptimized release.
No model endpoints are contacted.

The [manifest](benchmark-manifest.json), [raw rounds](benchmark-runs.json) and
[all metrics](performance-summary.md) preserve the original three-round sample.
`performance-summary.json` includes individual round values and spreads.
Process resource files contain `/usr/bin/time -v` CPU and peak RSS measurements.
The [extended three-round report](extended-performance-summary.md) retains the
longer cache and multiline runs separately. Cache reads at 100 groups have a
median change of +0.16% in `ns/op`, -10.40% in p95, and +0.05% in retained bytes.

## Reproduction

Use Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`, Linux amd64.
On Windows the runner uses the existing `Ubuntu-24.04` WSL distribution.
Build the `internal/service` and `internal/repository` test packages with
`go test -tags=unit -c`, `GOOS=linux`, `GOARCH=amd64`, `CGO_ENABLED=0` from each
source revision. Preserve the four binaries under
`.git/task-artifacts/merge-v0220/{baseline,candidate}-{service,repository}.test`.
Do not build or run other heavy jobs during timing.

```bash
python backend/benchmarks/results/merge-v0220/run-comparison.py
```

The runner writes new results under `.git/task-artifacts/merge-v0220`; the
published results here remain the delivery record. The summarizer reads the
raw files beside itself. Baseline sources were preserved using `git archive`,
without altering the main working tree or the six pre-existing diagnostic files.

## Interpretation

- Cache: 2,000 unique accounts shared across 1, 10 or 100 groups; 100 reads per
  fixture and round. `decoded-retained-B` is measured around explicit test GC.
- SSE: 20 streams per fixture, synthetic short/multiline data, slow writer,
  1/8/32 MiB image events. `WS` exercises the SSE-to-WS writer adapter, not a
  live network WebSocket. The fixture records the first **up to 1,024** relay
  timings per benchmark invocation; p95 in longer runs describes that initial
  window, not every operation. `ns/op` covers the complete invocation and also
  includes fixture setup/cleanup outside the relay timer.
- Billing: 32/128 queued frozen records referencing synthetic 1 MiB source
  payloads. `retained-B/task` measures memory retained by the queue after source
  release. Timing and B/op also include fixture construction and forced test GC;
  they are not database settlement latency.
- Peak process RSS/CPU includes fixture setup and all subcases in that process.
  No production load or production heap profiles are used.

本记录比较合并前后的已优化实现。原始三轮数据完整保留；小样本时延波动较大的
缓存与 WS 多行帧另做延长复测，独立记录，不混合挑选轮次。计费场景的主要验收量
为任务保留内存；其中合成大正文及测试 GC 的耗时不作为实际扣款延迟。

## Focused SSE investigation

The short multiline sample was repeated for six alternating baseline/candidate
pairs, three seconds each, with CPU affinity `0-3` and `GOMAXPROCS=4`. The
[manifest](sse-focused-manifest.json) and `*-sse-focused-*.txt` preserve all
rounds and ten-second CPU profile summaries. Binary profiles stay in the local
artifact directory rather than the repository.

Before the final trim optimization, median `ns/op` increased 4.84% and the
initial-window p95 increased 4.25%, with unchanged 17,232 B/op and 314 allocs/op.
Profiles localized the extra cost to repeated CRLF trimming in the new event
budget checks. The final implementation checks the byte budget before inspecting
separator content and passes already trimmed lines to `observeTrimmed`; the
byte/line limits, including fragmented lines and CRLF separators, remain tested.

The [final follow-up report](sse-optimization-summary.md) and
[direct-comparison data](sse-direct-summary.json) distinguish all three phases:

| Comparison | Median ns/op change | Initial-window p95 change |
| --- | ---: | ---: |
| Original merge candidate vs parent, six pairs | +4.84% | +4.25% |
| Final trimmed-line implementation vs parent, six pairs | +14.48% | +38.92% |
| Same-window original candidate vs final implementation, three pairs | -3.12% | -13.75% |

All pairs retain 17,232 B/op and 314 allocs/op. The last comparison improves in
each pair, and the profile reduces TrimRight CPU share from 5.42% to 2.16%.
Timing varies materially between batches, including the +14.48% batch retained
above. These are separate comparisons, not a combined estimate of the final
revision's latency change against the parent. All binaries use the same
[build settings](sse-binary-build-settings.json).

To repeat the focused test, build the parent as `baseline-service.test` and the
final merged revision as `optimized-service.test`, then alternate the binaries
with the following command for each of six rounds (Linux):

```bash
taskset -c 0-3 env GOMAXPROCS=4 GIN_MODE=release \
  .git/task-artifacts/merge-v0220/optimized-service.test \
  '-test.run=^$' '-test.bench=^BenchmarkCodexEngineRelay$/^WS$/^multiline_128$' \
  -test.benchmem -test.benchtime=3s -test.count=1 -test.timeout=2m
```

The original `candidate` binary predates the final three-file trim optimization;
it is an intermediate measurement, while `optimized` names the delivered code.
The [verification manifest](sse-optimized-verification.json) records the final
focused race checks and Linux builds. Linux focused tests also passed after a
temporary WSL command-start delay recovered without restarting the distribution.
