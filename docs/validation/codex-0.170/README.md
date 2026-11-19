# Codex 0.170 validation / 验证记录

日期 / Date: 2026-11-19。基线 / Baseline: `db4705f5a0278fac34bd66b5cbdca74619d8cd7b`。

实现提交 / Implementation commits:

- `61fc18abf` — `feat(openai): adapt Codex 0.170 reasoning and transport`
- `45b19ebe5` — `feat(telemetry): observe Codex reasoning replay`

本目录只收录本轮 Codex 0.170 适配的验证。协议测试使用模拟上游；数据库验证使用临时隔离容器；未调用真实模型、未修改 Engine 或生产配置、未部署。复现命令见 [commands.md](commands.md)，行为与升级说明见 [兼容说明](../../codex-0.170-compatibility.md)。

This directory contains only this adaptation's validation. Protocol tests use mock upstreams; database tests use isolated temporary containers. Commands are in [commands.md](commands.md). No deployment or production mutation is part of this work.

## 环境 / Environment

- Go **1.27.4**，`GOEXPERIMENT=greenteagc,jsonv2`，由当前 `backend/go.mod` 工具链解析。
- Windows amd64，AMD Ryzen 7 9700X；原始基准记录的并发后缀为 `-16`。
- 集成验证：WSL Ubuntu 24.04.3、Go 1.27.4 linux/amd64、Docker 29.4.0、PostgreSQL `18.1-alpine3.23`、Redis `8.4-alpine`。
- 前端：仓库锁文件和已有 pnpm 脚本；没有为本轮额外升级依赖。Vitest 2.1.9、Vite 5.4.21。

## 已完成检查 / Completed checks

| 范围 / Area | 结果 / Result | 原始记录 / Raw log |
|---|---|---|
| 类型化强度专题首轮 | 通过，4.492 s；后续交叉修复纳入最终联合复测 | [effort-initial-tests.log](raw/effort-initial-tests.log) |
| Chat/Messages 转换包测试、五个 continuation 入口、缓存键 | 通过；包 0.740 s、服务定向 3.217 s | [bridge-final-tests.log](raw/bridge-final-tests.log) |
| 协议转换 race、service 包编译、三轮桥接与音频基准 | 通过；service 输出明确为编译检查 `[no tests to run]` | [bridge-race-build-bench.log](raw/bridge-race-build-bench.log) |
| Guardian、线程 originator、连接池 | 通过，10.867 s | [guardian-identity-pool-tests.log](raw/guardian-identity-pool-tests.log) |
| Guardian／身份隔离 race | 通过，7.243 s | [guardian-identity-race.log](raw/guardian-identity-race.log) |
| Guardian 最终 race／WS 后续轮 429 | 通过，7.382 s；覆盖共用 FIFO 的 4,096 条总上限 | [guardian-final-race.log](raw/guardian-final-race.log) |
| 最终 Guardian／HTTP 入口与用量强度快照 | 通过，service 3.895 s、handler 0.122 s | [guardian-http-final-tests.log](raw/guardian-http-final-tests.log) |
| Replay、HTTP/WS 采集、metrics/schema、coverage、模型证据 | 通过，4.365 s | [telemetry-tests.log](raw/telemetry-tests.log) |
| 同范围遥测 race | 通过，13.747 s | [telemetry-race.log](raw/telemetry-race.log) |
| 归一化／指纹三轮对照 | 通过，14.420 s；既有路径分配次数不变 | [current-normalization-fingerprint-guardian-bench.log](raw/current-normalization-fingerprint-guardian-bench.log) |
| Guardian delta 最终热路径 | 通过，3.475 s；使用借用 ID 后固定 0 B / 0 allocs | [guardian-final-bench.log](raw/guardian-final-bench.log) |
| PostgreSQL／Redis 最终集成、Linux 后端构建 | 最新强度缓存边界／TTL／快路径变更后，7 项通过，9.416 s；Linux `cmd/server` 构建通过 | [最终集成与构建](raw/postgres-redis-final-linux-build.log)；另保留[首轮集成](raw/postgres-redis-integration.log) |
| 最终联合 Go 测试、Windows 后端构建 | 四个包通过：service 55.282 s、handler 1.646 s、DTO 0.117 s、apicompat 0.093 s；Windows `cmd/server` 构建通过 | [combined-final-tests-windows-build.log](raw/combined-final-tests-windows-build.log) |
| 强度／HTTP／WS 桥接最终 race | 通过：service 1.740 s、handler 7.015 s | [effort-bridge-final-race.log](raw/effort-bridge-final-race.log) |
| 强度解析三轮尺寸基准与交替配对基准 | 通过；64 KiB–8 MiB 固定分配，配对耗时见下表 | [尺寸基准](raw/effort-final-size-bench.log)、[交替配对](raw/effort-alternating-pair-bench.log) |
| 前端相关测试 | 当前 8 个测试文件、116 个不同案例均通过；下面日志包含部分重复复测 | [初轮](raw/frontend-initial-tests.log)、[用量与覆盖](raw/frontend-usage-coverage-tests.log)、[最终策略](raw/frontend-policy-final-tests.log)、[最终用量](raw/frontend-usage-final-tests.log) |
| 前端类型检查、生产构建 | 平台范围修复后再次通过，Vite 34.04 s | [最终 typecheck/build](raw/frontend-typecheck-build-final.log)；另保留[首轮 typecheck](raw/frontend-typecheck.log)、[首轮 build](raw/frontend-build.log) |
| 前端定向 ESLint | 退出码 0，无输出 | [初轮](raw/frontend-lint.log)、[最终](raw/frontend-lint-final.log) |

隔离集成覆盖迁移 306 重复执行、长强度字段保存、Redis continuation 强度往返／隔离／损坏／过期、遥测连接状态与策略代次、已封批重试、v3 → v4 升级及跨重启六类 replay 计数去重。

The isolated suite covers migration replay and long values, Redis continuation effort isolation/corruption/expiry, connection telemetry epochs and sealed retries, plus v3-to-v4 replay recovery across process restarts.

## 既有失败与验证边界 / Existing failures and validation scope

- Windows 的 `TestUsageExportDownloadLeaseCloseAndCancellation` 四个子项（`close`、`cancel`、`expired`、`renew-failed`）在 `usage_export_download_test.go:65` 失败；已在独立基线目录 `db4705f5a` 复现，见 [基线失败](raw/baseline-windows-export-failure.log)。
- 五项既有失败在独立 `db4705f5a` 基线全部复现：[baseline-existing-five-tests.log](raw/baseline-existing-five-tests.log)。分别为模型别名默认强度、空目录转换、两个 OAuth 401 fixture、Anthropic 完整历史裁剪。最终联合复测显式跳过这五项；名称和复现命令见 [commands.md](commands.md)。
- [初轮联合测试](raw/combined-initial-tests.log) 保留排查记录；本轮回归修复后的最终联合定向测试及 Windows／Linux 后端构建均通过。
- 本次按受影响范围验证，没有执行全项目所有测试。上方列出对应实现提交，文档与验证材料独立提交。
- 远端 CI 未执行／未核验。本机 `gh` 不在 PATH，无法通过该 CLI 核查运行状态；此前会话约定 Actions 关闭，本轮未重新确认该设置。推送后不等待远端 CI。

Frontend logs contain existing Browserslist age warnings and Vite dynamic/static import and chunk warnings. The initial component run included a test-stub Vue warning; the subsequent focused component runs pass. These messages are retained in the raw records.

## 性能 / Performance

同一工具链与合成数据各跑三轮。下表比较三轮中位数；所有单轮数据在 `raw/`。本轮没有采集整服务峰值 RSS、生产 p95 或真实模型吞吐；这里记录的是本地函数基准。

The tables use medians of three local runs with the same toolchain and synthetic inputs. Every individual run is preserved. They measure local functions, not production RSS, p95 or model throughput.

| 已有路径 / Existing path | 大小 | 基线耗时 ns/op | after 耗时 ns/op | 基线 B/op / allocs | after B/op / allocs |
|---|---:|---:|---:|---:|---:|
| Normalize | 64 KiB | 67,178 | 44,944 | 9,726 / 165 | 9,725 / 165 |
| Fingerprint | 64 KiB | 64,026 | 53,895 | 8,284 / 154 | 8,282 / 154 |
| Normalize | 1 MiB | 684,980 | 538,655 | 9,723 / 165 | 9,717 / 165 |
| Fingerprint | 1 MiB | 694,391 | 503,752 | 8,289 / 154 | 8,284 / 154 |
| Normalize | 8 MiB | 5,704,491 | 3,918,735 | 9,874 / 165 | 9,819 / 165 |
| Fingerprint | 8 MiB | 7,586,382 | 4,069,120 | 8,419 / 154 | 8,298 / 154 |
| Existing Chat bridge | 64 KiB | 479,056 | 451,345 | 1,278,269 / 160 | 1,278,638 / 163 |
| Existing Chat bridge | 1 MiB | 6,421,110 | 6,356,375 | 19,039,459 / 210 | 19,033,106 / 210 |

基线：[归一化与指纹](raw/baseline-normalization-fingerprint.log)、[Chat 桥接](raw/baseline-chat-bridge.log)。最终桥接见 [bridge-race-build-bench.log](raw/bridge-race-build-bench.log)。字段级联合值实现替代了开发过程中的整请求 MarshalJSON 试验；上表是最终实现，1 MiB 请求分配与基线持平。

归一化／指纹的三轮分配次数保持不变，没有增加随正文大小增长的分配。耗时受本机同时运行任务影响，单轮波动明显，表中较低耗时仅记录本轮测量，不作为稳定加速比例。Normalization/fingerprint allocation counts are unchanged; local timing is noisy, so the lower medians are recorded without claiming a stable speedup.

| 新增路径 / Added path | 数据大小 | 三轮耗时范围 | 每次分配 |
|---|---:|---:|---:|
| Inline audio payload | 1 MiB | 453.734–476.532 μs | 32 B / 1 alloc |
| Effective reasoning with large audio | 见基准 fixture | 265.641–273.426 μs | 168 B / 8 allocs |
| Prepared effort without configuration update | 64 KiB / 1 MiB / 8 MiB | 43.627–46.194 μs / 586.681–678.060 μs / 4.871–5.057 ms | 560 B / 11 allocs |
| Prepared effort with configuration update | 64 KiB / 1 MiB / 8 MiB | 68.161–71.982 μs / 1.001–1.081 ms / 7.679–8.321 ms | 608 B / 13 allocs |
| Guardian delta observation | 64 KiB / 1 MiB / 8 MiB | 59.44–65.65 ns | 0 B / 0 allocs |
| Reasoning replay summary | 空输出 | 630.0–704.4 ns | 64 B / 1 alloc |
| Reasoning replay summary | 64 KiB 输出 | 25.973–29.651 μs | 64 B / 1 alloc |
| Reasoning replay summary | 1 MiB 输出 | 416.001–454.595 μs | 64 B / 1 alloc |
| Reasoning replay summary | 8 MiB 输出 | 3.256–4.164 ms | 64 B / 1 alloc |

Replay 原始记录见 [telemetry-bench.log](raw/telemetry-bench.log)。正文从空输出增至 8 MiB 时，摘要分配保持 64 字节和一次；扫描时间随输入长度增长，没有新增整份正文分配。

Guardian 最终数据来自借用 ID 优化后的 [guardian-final-bench.log](raw/guardian-final-bench.log)；早先联合基准中的 32 B / 1 alloc 已被消除，保留早期原始日志供核对。

### 强度配置预过滤配对测量 / Paired configuration-prefilter measurement

无配置更新时先做只读字节预过滤，省去额外结构遍历；包含配置标记或 Unicode 转义时仍走结构解析。使用 1 MiB 相同输入，在同一迭代交替执行有／无预过滤版本，以减少跨批次时钟和系统负载差异。表中百分比按每一对计算，单位均为 ns/op。

| 输入 | 轮次 | 结构扫描 | 预过滤后 | 耗时变化 |
|---|---:|---:|---:|---:|
| 无 configuration update | 1 | 547,667 | 204,653 | -62.63% |
| 无 configuration update | 2 | 473,833 | 185,905 | -60.77% |
| 无 configuration update | 3 | 434,912 | 158,287 | -63.60% |
| 有 configuration update | 1 | 335,087 | 361,497 | +7.88% |
| 有 configuration update | 2 | 334,055 | 350,006 | +4.78% |
| 有 configuration update | 3 | 338,102 | 344,223 | +1.81% |

无配置路径三轮下降中位数为 62.63%；有配置路径有额外预过滤，耗时增量中位数为 4.78%，单轮范围 1.81%–7.88%，分配相同。该配对比较针对本轮新解析实现中的预过滤分支。此前分开批次测得有配置耗时相差约 45%，因此追加交替配对测量；报告以配对结果判断该分支，不据独立批次声称稳定加速或回退。

The alternating pairs compare the prefilter with structural-only scanning inside the new parser. Without a configuration update, the median reduction is 62.63%; with an update, median overhead is 4.78% (1.81%–7.88% across runs), with identical allocation. Separate batches had shown a roughly 45% timing difference, so the paired measurements were added to control execution-order/load variation.

保留 [预过滤前记录](raw/effort-before-prefilter-bench.log)、[分开执行的对照](raw/effort-sequential-pair-bench.log) 和 [最终交替配对](raw/effort-alternating-pair-bench.log)。最早单独的 `ConfigurationLargeInput` 基准未在 fixture 构造后重置计时，因此其 B/op 包含 fixture 开销，不用于分配对比；最终该基准修正后为 40 B / 3 allocs。`PreparedLargeInput` 的 560／608 B 固定分配数据有效。

## 原始记录来源 / Provenance

| 文件 | 本轮 FastCtx job |
|---|---|
| baseline-normalization-fingerprint.log | `j-urnghw` |
| baseline-chat-bridge.log | `j-8pax6v` |
| effort-initial-tests.log | `j-dxjyki` |
| effort-before-prefilter-bench.log / effort-final-size-bench.log | `j-7a2r3t` / `j-s0r3kr` |
| effort-sequential-pair-bench.log / effort-alternating-pair-bench.log | `j-wk491a` / `j-vbce58` |
| current-normalization-fingerprint-guardian-bench.log | `j-1xlmv5` |
| bridge-race-build-bench.log / bridge-final-tests.log | `j-xfsztl` / `j-1fblom` |
| guardian-identity-pool-tests.log / guardian-identity-race.log | `j-7xcu6a` / `j-uybloj` |
| guardian-http-final-tests.log / guardian-final-bench.log | `j-do57q8` / `j-ip8p7a` |
| guardian-final-race.log | `j-ezmsx1` |
| telemetry-tests.log / telemetry-race.log / telemetry-bench.log | `j-jrmnne` / `j-ml49ur` / `j-2ch907`，命令重定向的原始文件 |
| frontend-initial-tests.log / frontend-usage-coverage-tests.log | `j-qgmc6m` / `j-0vul3b` |
| frontend-policy-final-tests.log / frontend-usage-final-tests.log | `j-8090gp` / `j-6d28am` |
| frontend-typecheck.log / frontend-build.log | `j-qoaa3a` / `j-u36xae` |
| frontend-typecheck-build-final.log | `j-477363` |
| frontend-lint.log / frontend-lint-final.log | `j-x810ax` / `j-9mqdtu` |
| combined-initial-tests.log / baseline-windows-export-failure.log | `j-7fpd2r` / `j-mjurmc` |
| baseline-existing-five-tests.log | `j-upj5t4` |
| combined-final-tests-windows-build.log | `j-s5o35f` |
| effort-bridge-final-race.log | `j-wcy1r7` |
| postgres-redis-integration.log / linux-toolchain.log | `j-zexqgb` / `j-a9izj8` |
| postgres-redis-final-linux-build.log | `j-48l4jf` |
