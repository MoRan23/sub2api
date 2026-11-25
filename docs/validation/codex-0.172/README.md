# Codex 0.172 validation / 验证记录

Date: **2026-11-25** (Asia/Shanghai). Baseline: **561ed4714**. Source reference: local Codex **0.172.0** archive. See [compatibility notes](../../codex-0.172-compatibility.md) and [commands](commands.md).

## Checks / 检查

Environment: Go **1.27.4**, `GOEXPERIMENT=greenteagc,jsonv2`, WSL Ubuntu 24.04, AMD Ryzen 7 9700X; frontend on Windows. Only fixtures and isolated containers were used.

| Check | Result | Evidence |
|---|---|---|
| Service compatibility, finalizers, metadata, telemetry, five bridge rejection routes | 202 top-level tests passed | [service-unit.log](service-unit.log) |
| HTTP/WS tracing, interrupt ordering, bridge cancellation, coverage and catalog race | 48 top-level tests passed | [service-race.log](service-race.log) |
| Complete apicompat package, including new standalone/image regressions | Unit passed; final race passed | [apicompat-race.log](apicompat-race.log) |
| Account metadata PostgreSQL→Redis; byte-exact public catalog snapshot | Passed with PostgreSQL 18.1 / Redis 8.4 | [catalog-integration.log](catalog-integration.log) |
| Admin settings API contracts, Linux server build and version smoke | Passed | [server-contract-build.log](server-contract-build.log) |
| Fingerprint metadata frontend | 18 tests passed | [frontend-fingerprint-tests.log](frontend-fingerprint-tests.log) |
| Telemetry API, bilingual page and locale keys | 27 tests passed | [frontend-telemetry-tests.log](frontend-telemetry-tests.log) |
| Frontend type checking, production build and scoped lint | Passed | [typecheck](frontend-typecheck.log), [build](frontend-build.log), [lint](frontend-lint.log) |

The build also reran its existing three locale-key checks. Its existing Browserslist age, mixed static/dynamic import and large-chunk notices remain. The first lint invocation used repository-relative paths after switching into `frontend`; it failed to locate files. The corrected relative-path command passed, without a source-code change. [Initial invocation](frontend-lint-initial.log).

## Review fixes / 复查修正

- HTTP trace observations require actual Responses-finalizer projection and matching final outbound headers; a valid inbound trace on another HTTP path is not reported as sent.
- Anthropic rejects missing/null standalone text fields and file-only images in system/developer messages, alongside ordinary message/tool-output handling.
- New standalone provenance mapping uses structural placeholders instead of reconverting and encoding image data.
- Native WS fixtures retain `phase:"partial_answer"`, `end_turn:false` and unknown fields byte-for-byte before exercising existing interrupt/next-turn ordering.
- Frontend telemetry has an explicit signal allowlist; it was extended together with the backend coverage table so new diagnostics are visible.

## Performance / 性能

Baseline and current test binaries used the same Go toolchain and experiments, `GOMAXPROCS=1`, CPU affinity 2, and 500 ms per benchmark. Baseline/current runs alternated three times after builds and other checks finished. Values below are the three-run medians.

| Existing path | Baseline ns/op | Current ns/op | Change | Allocations/op |
|---|---:|---:|---:|---:|
| Normalize 64 KiB | 54,051 | 54,382 | +0.61% | 165 → 165 |
| Normalize 1 MiB | 515,157 | 534,190 | +3.69% | 165 → 165 |
| Normalize 8 MiB | 4,139,607 | 3,482,175 | −15.88% | 165 → 165 |
| Fingerprint 64 KiB | 55,979 | 57,416 | +2.57% | 156 → 156 |
| Fingerprint 1 MiB | 520,384 | 485,251 | −6.75% | 156 → 156 |
| Fingerprint 8 MiB | 4,006,151 | 3,480,972 | −13.11% | 156 → 156 |
| Existing Chat bridge 64 KiB | 509,902 | 519,317 | +1.85% | 155 → 155 |
| Existing Chat bridge 1 MiB | 7,394,069 | 7,368,753 | −0.34% | 181 → 181 |

No measured median regressed by more than 5%. Normalization remains about 9.7 KB/op and fingerprint extraction about 8.3 KB/op across body sizes; small byte differences reflect benchmark amortization. The existing bridge remains about 1.25 MB / 19.02 MB for the two fixtures. The unchanged scanning paths' faster samples are reported as measurements, not attributed to a new scan optimization.

Cached trace projection measures **0 B / 0 allocations** when absent and **32 B / 2 allocations** with valid parent/state. Median times are 35.27 ns and 189.5 ns. This microbenchmark measures cached projection rather than complete HTTP processing.

Standalone bridge fixtures separately measure 64 KiB text and 256 KiB image content for Chat, Messages and Messages with source mapping. These newly supported semantics do not have an equivalent successful baseline. Raw results retain both conversion and mapping costs; source mapping does not repeat the image conversion/encoding pass.

Raw results: [baseline service](baseline-service-benchmark.log), [current service](current-service-benchmark.log), [baseline bridge](baseline-bridge-benchmark.log), [current bridge](current-bridge-benchmark.log), [trace projection](trace-benchmark.log), [standalone conversion/mapping](standalone-bridge-benchmark.log).

## Delivery boundaries / 交付边界

No schema migration, dependency upgrade, production configuration change, Engine edit, real model request or deployment. Existing untracked investigation files are preserved. Full repository suites were not run; checks target the changed contracts and their adjacent paths. Remote Actions state was not verified because `gh` is unavailable in this environment; local checks above establish this delivery's validation.

升级由管理员按现有发布流程进行，无新增开关或配置要求。此次没有存储格式迁移，回退可使用上一应用版本；遥测 v4 聚合快照和既有模型资产保持原样。
