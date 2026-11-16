# 验证记录 / Verification record

基线：`e6de1c8eb`。本记录汇总定向测试、隔离数据库验证、构建和三轮性能测量结果。各模块复现命令见 [统计](stats.md)、[后台任务](background-tasks.md)、[调度与目录](scheduler-catalog.md)、[监控与页面](monitor-ui.md)、[进程测量](process-measurements.md) 及 [转发命令](raw/forwarding-commands.log)。

Baseline: `e6de1c8eb`. This record summarizes targeted tests, isolated database checks, builds and three-run performance measurements. Reproduction commands and the underlying module reports are linked above.

## 环境与口径 / Environment and scope

- Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`、`GOMAXPROCS=4`；包编译并发按各报告为 1 或 2。Windows 与 Linux 的比较分别保留，不混算不同平台数据。
- 数据使用本地合成请求、独立 PostgreSQL／Redis 和模拟 SMTP。统计使用 PostgreSQL 17；归因集成日志使用 PostgreSQL 18.1 / Redis 8.4；提醒使用独立 schema 和 8 个连接。不连接生产数据库、不调用真实模型。
- 下列通过项是定向测试。重叠测试集不相加作为唯一用例总数。构建或 lint 成功但无标准输出的记录保留为空文件，执行结果由对应模块报告记录。

Checks use Go 1.27.3 and the existing experiments, with fixed fixtures and concurrency within each comparison. PostgreSQL/Redis fixtures and simulated transports are isolated from production. Passing targeted suites are listed individually; overlapping suites are not added into a unique test count. Silent successful commands retain empty logs and their outcome in the module report.

## 已通过 / Passed checks

| 模块 / Module | 已完成验证 / Completed coverage | 原始记录 / Raw record |
|---|---|---|
| 日汇总与统计 / Rollups and statistics | Windows 定向单元；Linux race；准备、发布栅栏、迟到写入、保留期失效、DST、有界尾段、缓存所有权与取消 | [unit](raw/stats-unit-windows.log), [race](raw/stats-race.log) |
| 统计数据库 / Statistics database | 隔离 PostgreSQL 迁移重复执行、真实计费统计口径、历史删除重建、迟到写入栅栏、未降低水位时的保留期失效、并发触发器及百万行查询计划 | [integration and EXPLAIN](raw/stats-integration.log) |
| 仪表盘初始化 / Dashboard initialization | 2 条前端测试：统计初始化提示与恢复 | [frontend](raw/stats-frontend.log) |
| 后台任务 / Background tasks | 归因并发配置及取消 drain；审核快照复用、串行发布、处罚分批清理；SMTP greeting/AUTH 取消和截止时间 | [unit](raw/background-unit.log), [race](raw/background-race.log) |
| 归因数据库 / Attribution database | 5 项集成测试：集群名额、取消期间保留租约、账号互斥、局部维护、100 条／215 分钟／API Key 锚点／活动租约保护 | [PostgreSQL](raw/background-attribution-integration.log) |
| 订阅提醒 / Subscription reminders | 独立 schema，迁移重复执行、4 个并发领取者共享 2 个名额、续期／撤销／收件人复核、租约恢复、旧 claim 禁止发送、停用重试、失败记录保留 | [PostgreSQL](raw/background-reminder-integration.log) |
| 归因配置页面 / Attribution settings | 2 个测试文件、94 条测试；定向 ESLint | [frontend](raw/background-frontend.log), [lint](raw/background-eslint.log) |
| 调度与目录 / Scheduling and catalogs | sharedflight 独立取消、版本隔离、缓存容量及失效、目录来源和权限隔离、共享投影、Composite 索引、Redis pipeline | [unit](raw/scheduler-targeted-tests.log), [race](raw/scheduler-race.log) |
| 账号投影 / Account projections | PostgreSQL 投影列、排除凭据、母账号健康状态 | [PostgreSQL](raw/scheduler-postgres.log) |
| 转发与 XLSX 编码 / Forwarding and XLSX encoding | Anthropic 缓冲与 SSE、WS 比较、原始 JSON、协议终态及 XLSX 流式编码、37 列、精度、转义、大小边界、关闭与错误行为 | [unit](raw/forwarding-unit.log), [race](raw/forwarding-race.log), [commands and coverage](raw/xlsx-commands.log) |
| 导出生命周期 / Export lifecycle | 最终 service/repository race 全通过；覆盖持有者隔离、取消、有界写入、失联与过期租约、提交不确定性、下载租约、删除与配额、游标及索引 | [final race](raw/xlsx-final-race.log) |
| 后端最终构建与集成 / Final backend build and assembly | `go build ./cmd/server` 成功；配置、admin access、日志脱敏、handler 和路由定向测试通过；删除与下载租约最终两项 race 通过 | [final build and tests](raw/final-build-tests.log) |
| 监控后端 / Monitoring backend | V1 批量窗口、V2 共享缓存、授权后裁剪、当前页分组摘要及容量范围；缓存和取消 race | [unit](raw/monitor-ui-go-tests.log), [race](raw/monitor-ui-race.log) |
| 监控与管理页面 / Monitoring and administration UI | 16 个测试文件、175 条测试；后续取消与在途复用调整另跑 3 文件、17 条 | [targeted suites](raw/monitor-ui-tests.log), [final cancellation suites](raw/monitor-ui-cancellation.log) |
| 导出删除中状态 / Export deleting state | 最后 6 条页面测试、类型检查和定向 lint 通过 | [6 frontend tests](raw/monitor-ui-export-deleting.log), [typecheck](raw/monitor-ui-typecheck.log), [lint](raw/monitor-ui-export-deleting-lint.log) |
| 前端静态检查和构建 / Frontend static checks and build | 类型检查、定向 lint、生产构建成功 | [typecheck](raw/monitor-ui-typecheck.log), [lint](raw/monitor-ui-lint.log), [cancellation lint](raw/monitor-ui-cancellation-lint.log), [production build](raw/monitor-ui-build.log) |

统计集成中，目标用户的 100,000 条历史原始记录聚合为 120 个日桶；相同样本的共享缓冲访问从 17,359 页降至 33 页。主动阻塞历史 stage 构建时，当前日期用量写入在 8.340 ms 完成；随后迟到历史写入使旧发布失效，重建保留两笔记录。详情及 SQL 计划见上述统计集成日志。

The statistics integration fixture reads 120 day buckets instead of 100,000 historical rows for the target user, reducing shared-buffer accesses from 17,359 to 33. A current-day usage insert completes in 8.340 ms while the historical stage build is deliberately blocked. A subsequent historical insert fences the stale publication; rebuilding retains both records.

导出生命周期的单元与数据库检查、最终 race 均已通过。删除处理中保持文件及配额保护，下载租约关闭、取消、过期或续租失败均有回归覆盖。最终两项删除／下载竞态复查与后端构建共用 [final-build-tests.log](raw/final-build-tests.log)；构建命令成功时不产生标准输出。

Export lifecycle unit/database checks and the final race suite passed. Coverage includes deletion-time file/quota protection and download lease closure, cancellation, expiry and renewal failure. The last two deletion/download race checks and the successful silent backend build share [final-build-tests.log](raw/final-build-tests.log).

## 性能记录 / Performance records

| 场景 / Scenario | 已完成记录 / Recorded measurement | 记录与说明 / Evidence |
|---|---|---|
| 用户历史统计 / User history | 同一百万行数据集、三轮未缓存查询：85.830 / 85.550 / 96.589 ms → 4.337 / 4.623 / 4.378 ms | [baseline](raw/stats-history-baseline.log), [optimized](raw/stats-history-optimized.log), [report](stats.md) |
| 10,000 条审核规则刷新 / 10,000-rule refresh | 三轮 unchanged reload：约 7.4–8.1 ms、8.9 MB、48,485 分配 → 485–615 ns、1,392 B、4 分配 | [baseline](raw/background-config-baseline.log), [optimized](raw/background-config-optimized.log) |
| Redis、Composite、条件目录 / Redis, Composite, conditional catalog | 同程序保留旧算法对照，三轮；目录与索引标明热查询 | [comparisons](raw/scheduler-benchmark.log), [report](scheduler-catalog.md) |
| 协议转发和 XLSX / Forwarding and XLSX | 同程序旧／新算法三轮；XLSX 每次流式写入 1,000 行 | [benchmarks](raw/forwarding-bench.log), [fixture and commands](raw/forwarding-commands.log) |
| 监控概览缓存 / Monitor overview cache | 100 组、18 格、三轮冷路径／缓存路径；命中分配约下降 29.4%，第三轮计时反向波动保留 | [benchmark](raw/monitor-ui-bench.log), [interpretation](monitor-ui.md) |
| 用户浮层 DOM / User-tooltip DOM | 100 用户、1,000 分组、三轮：初始 3,244 个元素，打开一个浮层 3,745 个 | [frontend fixture](raw/monitor-ui-tests.log), [report](monitor-ui.md) |
| 进程存活堆／RSS／CPU／吞吐／p95 / Process metrics | 三轮串行完成；每个场景和实现使用独立进程，预热后执行 32 次；覆盖文本、工具 JSON、16 MiB WS 比较及流式 XLSX | [raw measurements](raw/process-measurements.log), [report](process-measurements.md) |
| 200,000 行 Excel 上限规模 / Default export row limit | 三轮独立进程，各预热一次和执行三次；存活堆约 6.6 MiB，峰值 RSS 约 105 MiB | [full-size measurements](raw/export-fullsize-process.log), [scope and results](process-measurements.md) |

微基准的 B/op 是累计分配量，DOM 样本是 jsdom 元素规模。进程存活堆、峰值 RSS 和 p95 在最后一项独立汇总。各报告均保留测量平台、样本和冷／热路径口径。

B/op is cumulative allocation; the DOM fixture measures jsdom element growth. The completed process report separately records retained heap, peak RSS, CPU and operation p95. Each report states its platform, fixture and cold/warm assumptions.

## 基线已知失败 / Failures reproduced on the baseline

扩展回归发现下列测试失败，并在未优化基线 `e6de1c8eb` 运行相同测试复现。定向新增测试的通过结果与这组扩展回归分别记录。

The following expanded-regression failures also reproduce on the unchanged baseline. They are tracked separately from passing targeted checks.

| 测试 / Test | 记录的原因 / Recorded issue |
|---|---|
| `TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization` | mock 未配置 OAuth account-state repository，冷却状态断言失败 / missing OAuth-state mock dependency |
| `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization` | 同一 mock 依赖缺失，重新授权状态断言失败 / same missing dependency |
| `TestApplyOpenAIInstallationIDForOutboundShadowUsesAuthorizedParentProfile` | shadow installation profile resolution / ensure-call 断言不一致 |
| `TestUpdateExtraCodexDisplaySnapshotsAvoidSchedulerOutbox` | credits/referral 的 refresh、invalidate、mixed-update 子用例 mock 预期不匹配 |
| `TestGrokCatalogCASRefreshesSingleAccountOutsideTransaction` | 授权投影 SQL 超出既有 mock 预期，刷新次数为 0 而非 1 |

原始对照：[扩展回归](raw/scheduler-broad-regression.log)、[基线复现](raw/scheduler-baseline-existing-failures.log)。

## 已修复或环境中断的检查 / Resolved and interrupted checks

- 扩展 race 曾发现既有清理测试桩计数器的未同步访问，已改用 `atomic.Int32`；最后 [stats-race.log](raw/stats-race.log) 通过。原失败保留于 [stats-race-existing-test-stub.log](raw/stats-race-existing-test-stub.log)。
- 调度 `*-initial.log`、`*-intermediate.log` 保留早期编译缓存读取错误和已修复的缓存清除测试失败；最终定向测试与 race 使用上表无后缀日志。
- [xlsx-lifecycle-race.log](raw/xlsx-lifecycle-race.log) 的 service 部分通过；repository 部分遇到 WSL PostgreSQL 连接被远端关闭，整次命令失败。恢复后 [xlsx-final-race.log](raw/xlsx-final-race.log) 全部通过，早期环境中断记录继续保留。
- 前端构建成功，同时报告旧 Browserslist 数据、部分静态／动态导入重叠及大于 500 kB 的 chunk 警告；未为消除警告进行额外依赖升级。

An existing cleanup test-stub race was fixed and the final statistics race run passed. Intermediate scheduler logs retain earlier resolved failures. WSL PostgreSQL interrupted an earlier export lifecycle run; the final rerun passed both service and repository checks. The production frontend build succeeded with Browserslist, mixed-import and large-chunk warnings retained in its log.

## 交付状态与验证范围 / Delivery status and verification scope

| 项目 / Item | 当前状态 / Current status |
|---|---|
| 最终前端生产构建 / Final frontend production build | 通过 / Passed；最后删除状态调整后重新构建，见 [frontend-final-build.log](raw/frontend-final-build.log) |
| 全仓库全部测试 / Exhaustive repository-wide suites | 未完整执行 / Not run exhaustively；已执行范围是上表定向及记录的扩展回归 |
| 真实浏览器 CPU／内存和端到端压力 / Browser CPU/memory and end-to-end load | 未执行 / Not executed；已有性能记录使用组件／jsdom、微基准和独立进程场景 |
| 生产模型、生产数据库、支付、部署 / Production model/database/payment/deployment | 按范围未执行 / Excluded by task scope |
| 提交、推送及远端 CI / Commit, push and remote CI | 已将 7 个分项提交正常推送至 `origin/dev`，代码与验证提交为 `c67655fd8`。推送后 GitHub API 返回 workflow runs 与 check runs 均为 0；本次未执行远端 CI。原始响应：[Actions](raw/remote-actions.json)、[checks](raw/remote-check-runs.json)。本记录的后续提交仅补交付状态。 / Seven implementation/validation commits pushed normally; GitHub returned no workflow or check runs for `c67655fd8`. The follow-up record commit only updates delivery status. |
