# v0.2.25 merge validation / 合并验证

## Environment and scope

- Baseline: `b3d991119c0f1cf119a077a578709eef7ac4010b`.
- Upstream: `0f6671457132425d1b84f3d80536b53e02942424` (`v0.2.25`).
- Verified final code: `4e2f6e0af`, following normal merge `ec802473d`. Subsequent documentation does not change tested production code.
- Linux Go 1.27.4 under WSL Ubuntu 24.04; `GOEXPERIMENT=greenteagc,jsonv2`, `GOTOOLCHAIN=local`, `GOMAXPROCS=2`, serial package builds (`-p 1`).
- Existing pnpm dependencies and lockfile. No frontend dependency, Ent schema, Wire source, or SQL migration change.
- Tests use local fixtures and disposable databases. No production configuration, Engine checkout, or live model service is changed or invoked.

## Completed checks

| Check | Result | Local execution record |
| --- | --- | --- |
| Header variables, final evidence, WS identity, upstream billing/group sync, structured errors, Compact delivery, repository CAS and relevant handlers/routes | Passed across selected service, repository, admin handler, gateway handler and route tests | `j-xups1l` |
| Admin access boundary, including restricted/full-key group preview | Passed | `j-wnnmrd` |
| Auth middleware, API contracts, billing routes, admin boundaries and batch-create settings regression | Passed | `j-jxyvl8` |
| Isolated billing/group-sync and Engine snapshot integration | Group sync, stale forms, probe CAS, due selection and slim caches passed; the existing proxy-event assertion was corrected and the 18-test Engine/proxy combination passed | `j-9iz22x`, `j-x13rys` |
| Isolated account configuration, credential CAS, shared OAuth and authorization generations | 14 selected suite methods, 25 leaf cases passed; no skips | `j-0dm6hw` |
| Linux production server build | Passed, `CGO_ENABLED=0`, `-trimpath` | `j-d3ipow` |
| Targeted race: header reports, WS pool/transport/Guardian/residency, encrypted-content recovery, Compact and probe concurrency | Passed, 12.706 s | `j-8jkiue` |
| Engine WS multi-turn regression under race | Three repeated runs passed, 1.219 s total | `j-8jkiue` |
| Account forms, header editor, preview cancellation/edit races, credentials, account list and Ops details | 11 files, 427 tests passed | `j-vjp4vv` |
| Locale completeness, frontend type checking and production build | Passed | `j-w6z4j3` |
| Targeted frontend ESLint | 25 affected files, no errors or warnings | `j-hdfljc` |

The selected test pattern in `j-xups1l` compiled the server and middleware packages but selected no tests there; they are not counted as test passes in that row. Existing frontend warnings about the Browserslist dataset, mixed static/dynamic imports, and large chunks remain. Dependencies were not changed to suppress those warnings.

## Failures investigated during validation

- The first isolated integration batch found a global-outbox count in `TestProxyIdentityUpdateInvalidatesProbeAndRejectsInFlightSnapshot`. The exact pre-merge baseline passed this test alone (`j-wvha0y`) but reproduced the same 5/5/6 counts after the Engine capacity/models tests (`j-d30zb4`). The assertion now filters the tested account's `payload.account_ids`; its expected 0/1 counts and payload checks remain. The combined merged run passed (`j-x13rys`). No production behavior was changed to satisfy the assertion.
- The first targeted race run (`j-gak9ni`) found the reader in Sub2's Engine WS bridge accessing a context variable reassigned by the turn loop. The same captured-variable pattern exists at the pre-merge baseline. Connection and turn contexts are now separate; cancellation and complete-delivery settlement rules remain intact. The final targeted race run and three repeated Engine multi-turn runs passed (`j-8jkiue`).
- The next race run (`j-o2eb77`) reported no data races, but a new zero-allocation assertion failed because race instrumentation can discard regexp pool entries. Allocation figures belong to the ordinary benchmark runs; the unit test checks text reuse and redaction semantics without relying on race-build allocation counts.

## Performance

Baseline files were exported from the exact commit above. Existing benchmark cases are paired by identical names and use the same toolchain, inputs and three-run sampling, followed by five alternating runs for outliers. Header templates and the final structured-error sanitizer have separate measurements for newly added paths. The WS acquire allocation regression was eliminated (584 B / 10 allocations, matching baseline); unmatched 512 KiB sanitization dropped from about 3.16 MB to 8 KB per operation. Forwarder timing remains variable: the final median was +8.3%, with overlapping ranges and p=0.548, while allocations were unchanged. See the full [performance report](performance.md).

## Existing failures and CI

The complete service suite was not run for this focused merge. The 26 failures documented for the previous merge remain recorded in [v0.2.24 validation](../merge-v0.2.24/README.md); this report does not claim they have been fixed or newly reproduced.

A read-only GitHub Actions permissions query on **2026-11-24** returned `enabled: false`. Remote CI was not run. Local checks and deployment are separate.

## 中文说明

本次合并采用定向回归，覆盖新请求头变量、最终发送证据、WS 隔离、账号名称同步、结构化错误和 Compact 交付契约。相关 Go 单元、最终 race、隔离 PostgreSQL/Redis 事务验证和后端构建通过；前端 427 项测试、类型检查、生产构建及定向 lint 通过。完整 service 套件的既有失败仍在上一版报告中单列，本次不把未执行的套件记为通过。

GitHub Actions 当前关闭。未部署、未操作生产配置，也未调用真实模型。
