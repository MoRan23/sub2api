# Codex 0.169.0 validation / 验证记录

Date: 2026-11-13. Baseline: `efb71831f`. Reference: supplied local Codex
0.169.0 source. Windows amd64, Ryzen 7 9700X, Go 1.27.3,
`GOEXPERIMENT=greenteagc,jsonv2`, benchmark GOMAXPROCS 16. Fixtures use synthetic
text and local HTTP/WS servers; no real model, Engine or production calls.

## Verification results / 验证结果

| Check | Result |
|---|---|
| Affected service + handler regression | PASS (19.242s / 0.119s) |
| Standalone context service + handler race | PASS (7.683s / 1.225s) |
| Guardian / fingerprint race | PASS (6.636s) |
| HTTP bridge / native interrupt race, 3 runs | PASS (40.480s) |
| All `TestCodexTelemetry` race tests | PASS (17.837s) |
| Physical pool Dial, prewarm, redirect failure race, 3 runs | PASS (1.630s) |
| Isolated PostgreSQL / Redis integration | PASS (8.627s) |
| Backend `go build ./...` | PASS |
| Frontend component / locale tests | 84 passed, 3 files |
| Frontend type checking, targeted ESLint, production build | PASS |

主要回归覆盖：Guardian 与强制 Fast、OAuth／API Key／Engine 边界、平铺元数据、
物理 WS 握手及连接隔离；独立上下文与真实工具结果混合；HTTP 桥接在等待响应头、
流执行及写入失败时的取消与有界清理；建连回调幂等、授权比较、失败重试、重定向
后的实际请求头、队列与策略代际；遥测新增字段、来源和历史恢复。

The database suite started isolated PostgreSQL 18.1 and Redis 8.4 containers
under WSL Ubuntu-24.04 with `CI=true`; these checks executed, rather than skipping
when Docker was absent from the Windows host. Tests cover v3 connect aggregate
recovery, sealed payload/metadata immutability after lease recovery, cross-instance
policy fencing and rollback, new critical-path nulls after unsealed turn recovery,
existing v1/v2 aggregate upgrades and three-system persistence. Test containers
were cleaned up.

扩大扫描还复现了以下基线已有失败，未按通过计入：

- `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback`：
  历史辅助请求测试替身缺少 `GetByID` 实现，触发 nil panic。
- `TestCodexContextWindowSyncRoundTrip`：模型目录测试夹具未提供有效授权账号。
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution`：
  旧的账号重载次数／错误断言不匹配当前基线行为。
- `TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop` 和
  `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory`：
  旧断言未包含基线已存在的身份与默认指令行为。
- Broad history scanning also stopped at a stale
  `TestForwardCodexHistoryNotesReusesResponsesStickyBinding` fixture. The broader
  package scan is not reported as a full-suite pass.

Two test-only repairs retain their original assertions: the orphan-output HTTP
guidance test now invokes its per-attempt validator instead of failing first in
an empty scheduler; the native interrupt disconnect test waits until its reader
has observed disconnect before injecting a terminal frame. Neither repair
weakens runtime tool validation or changes native interruption behavior.

## Three-round performance comparison / 三轮性能对比

The same benchmark fixture was copied into an archived, unmodified baseline.
After an initial 400ms run and a sequential 1s paired run showed timing drift,
CPU profiles located over 92% of the normalization cost in the unchanged JSON
validator/parser. A 3s profile pair measured 1 MiB at 456.75 → 458.82 µs (+0.45%).
We then alternated the already-built baseline and updated test binaries for
three rounds, 1s per benchmark, with no other task test jobs running.
All stages' raw results are retained below.

以下为最后三轮交替对比的中位数；计时是本机微基准，不包含上游模型延迟。

| Scenario | Before ns/op | After ns/op | Time change | Before → after B/op | Before → after allocs/op |
|---|---:|---:|---:|---:|---:|
| Normalize 64 KiB | 40,108 | 40,628 | +1.30% | 9,728 → 9,728 | 165 → 165 |
| Normalize 1 MiB | 437,776 | 448,898 | +2.54% | 9,721 → 9,722 | 165 → 165 |
| Normalize 8 MiB | 3,664,767 | 3,403,343 | −7.13% | 9,736 → 9,734 | 165 → 165 |
| Fingerprint 64 KiB | 202,110 | 203,008 | +0.44% | 158,094 → 158,331 | 190 → 193 |
| Fingerprint 1 MiB | 2,700,641 | 2,735,258 | +1.28% | 2,125,425 → 2,125,562 | 192 → 195 |
| Fingerprint 8 MiB | 21,414,827 | 20,214,930 | −5.60% | 16,807,375 → 16,807,773 | 196 → 199 |
| WS forwarder hot path | 75,841 | 75,526 | −0.42% | 33,267 → 33,279 | 847 → 847 |
| WS event envelope | 435.5 | 370.8 | −14.86% | 456 → 456 | 4 → 4 |
| Warm pool acquire | 1,981 | 1,925 | −2.83% | 603 → 600 | 10 → 10 |

新增 Guardian 指纹摘要是固定大小的小对象。大正文规范化分配数保持不变，指纹
分配增量为数百字节，未增加整份正文副本。三轮交替中位数最大耗时增长为 2.54%；
早期顺序运行的 9–13% 波动未稳定复现，未据下降值宣称整体吞吐收益。
Standalone raw shape checks additionally measured 0 B/op and 0 allocs/op for
64 KiB, 1 MiB and 8 MiB in all three rounds.

Raw results: [initial baseline](codex-0169/baseline-initial.txt),
[initial updated](codex-0169/after-initial.txt),
[sequential baseline](codex-0169/baseline-paired.txt),
[sequential updated](codex-0169/after-paired.txt),
[alternating baseline](codex-0169/baseline-alternating.txt),
[alternating updated](codex-0169/after-alternating.txt),
[standalone](codex-0169/standalone.txt).

## Reproduction / 复现

Run from `backend/`, with the same Go toolchain and experiment flags:

```bash
go test ./internal/service ./internal/handler -run '(CodexTelemetry|Codex0169|Guardian|Standalone|HTTPBridge|NativeInterrupt|NativeControl|OpenAIWSConnPool|OpenAIRequestIntegrity|CodexWireProfile|Codex.*Transform|Fingerprint)' -count=1
CGO_ENABLED=1 go test -race ./internal/service -run 'TestCodexTelemetry' -count=1
CGO_ENABLED=1 go test -race ./internal/service -run '(HTTPBridge|NativeControl|NativeInterrupt|Ingress.*Interrupt)' -count=3
CGO_ENABLED=1 go test -race ./internal/service -run '(Codex0169Pool|Codex0169FailedDial|WSResidencyCrossOriginRedirectAndActualSnapshot)' -count=3
go test -run '^$' -bench 'Benchmark(Codex0169Compatibility|OpenAIWSForwarderHotPath|OpenAIWSPoolAcquire|OpenAIWSEventEnvelopeParse)$' -benchmem -benchtime=1s -count=3 ./internal/service
CI=true go test -tags integration ./internal/repository -run 'TestCodexTelemetry0169Postgres|TestCodexTelemetryPostgresMetricV1UpgradeAcrossInstancesAndRestart|TestCodexTelemetryPostgresRuntimeThreeSystemsAcrossRestart' -count=1 -v -timeout=15m
go build ./...
```

Frontend: run the two fingerprint component tests and
`src/i18n/__tests__/localeKeyCompleteness.spec.ts`, `vue-tsc --noEmit`, ESLint on
the changed frontend files, and the normal production build.

No production deployment was performed. Remote CI was not verified: GitHub's
workflow API returned HTTP 403 (unauthenticated rate limit) during delivery.
The results above are executed local and isolated-container checks.
