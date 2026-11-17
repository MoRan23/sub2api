# 限流重试 CPU 与分配放大 / Rate-limit retry amplification

基线：`a34cb7c77`。本次只读排查生产，修改、测试均在本地完成；不调整重试窗口、次数、授权校验、计费或遥测保留规则。

## 现场证据

- 2026-11-17 北京时间 16:49 的单次容器采样：Sub2API CPU 102.63%，PostgreSQL 122.97%；Sub2API 容器内存 2.365 GiB。100% 表示一个逻辑 CPU。
- 已核对运行二进制包含上一轮新函数 `extractContentModerationInputs` 和 `codexTelemetryReviewFieldFromView`，不是仍运行旧代码。
- 最近三分钟：1,241 次 429 同账号重试、4 次 503 重试，涉及 251 个请求，最高已到第 15 次尝试。465 个新入站请求共约 890 MiB，正文 P95 约 11.91 MiB，最大约 70.36 MiB。
- 两次 20 秒只读 CPU 采样得到 3,541 / 3,845 个样本、无丢失。`Forward` 出现在 61.4% / 55.3% 的调用链中；时区处理为 8.0% / 12.3%。JSON 扫描、解码、编码和复制仍是主要开销。调用链占比相互包含，不能相加。
- 连续 8 次数据库活动快照中，7 次出现遥测容量淘汰查询。池接近 4,096 槽时，重试不断产生新的 attempt，原本每次仅淘汰所需差额。通用计划可能把父 turn 的相关查询改成跨池哈希扫描。
- 计费队列没有积压；调度缓存约 110 KiB。这轮没有把它们当成主要原因。

CPU 原始采样和数据库计划位于 `raw/retry-cpu-sampling-*.json`、`raw/retry-db-*.log/json`。数据库对照只执行 SELECT 和不执行语句的 EXPLAIN；未对生产 DELETE 使用 EXPLAIN ANALYZE。

## 修改

1. **时区只在当前尝试正文应用一次。** 内部冻结来源与生成补丁不再提前构造默认目标正文，再 Undo、重建并重新 Apply。首次入站扫描可复用；公开 Prepare/WithTarget 的原有契约保留。逐次账号目标、来源校验和最终完整性检查仍执行。
2. **原生 OAuth 输入按实际变化决定重编码。** 已规范 input 不再无条件标记 Modified，输入项按需复制。只有已成功解码、无重复键且 Unicode 无需修复时复用原字节；重复/转义同名键、无效 UTF-8、孤立代理字符保持旧的解码再编码处理。不缓存跨账号的可变 map 或最终正文。
3. **空图片检查与工具 schema 避免重复走过大正文。** 正常非空 Base64 用保守负筛跳过树遍历；有转义或可疑尾部时继续旧判定。工具 schema 保留 JSON、UTF-8、深度和作用域校验，跳过重复输入遍历，并用字节搜索处理长字符串。
4. **兼容重试摘要延迟计算。** HTTP 只有真正进入字段兼容恢复时才建立正文摘要和去重集合，429 不再为此重复全文 SHA-256。共享重试预算不变；WS 继续立即保存小摘要，避免长连接持有原正文。
5. **OpenCode 只留 session 标量。** Gin 不再为这个用途保存完整正文；普通 OAuth 和确定非 OpenCode 的 API Key 跳过捕获，跨尝试清理旧值。头部、原始会话、转换后正文和账号覆盖的优先级不变。
6. **遥测淘汰限定父 turn 查找。** 明确当前池参数，并使用 `OFFSET 0` 保持相关单行查询，防止 PostgreSQL 将其改为跨池/全池哈希扫描。容量、淘汰量、排序、active/protected 保护、过期时间和采样去重语义不变；没有迁移或生产配置修改。

## 本地基准

Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，`GOMAXPROCS=4`，Windows/amd64、Ryzen 7 9700X。每项同进程运行旧实现与新实现三轮，默认 `-benchtime=100ms`，下表为中位数。

| 阶段与合成输入 | 原耗时 → 新耗时 | 原分配 → 新分配 |
|---|---:|---:|
| 时区，32 MiB，8 次尝试 | 1,966.97 → 649.63 ms | 1,611,277,192 → 537,121,600 B/op |
| 时区，8 MiB，8 次尝试 | 612.21 → 201.50 ms | 403,316,088 → 134,466,368 B/op |
| OAuth 转换，8 MiB 文本，15 次尝试（含解码及复用校验） | 623.01 → 413.02 ms | 797,198,176 → 629,236,120 B/op |
| 空图片检查，32 MiB 图片 | 80.34 → 3.15 ms | 33,562,624 → 0 B/op |
| Schema 无需修复，32 MiB 图片 | 120.36 → 23.96 ms | 872 → 187 B/op |

这些是具体阶段的合成基准。线上整体 CPU、峰值内存和延迟收益，需要部署本批修改后在相近请求体量与重试强度下再采样。

首批 100ms 基准中的 8 MiB 图片、5 次转换曾出现 24.7% 的耗时回退，且单轮只有一次操作；原始数据完整保留。待其他编译/基准退出后，使用 `-benchtime=1s -count=3` 复测：旧 148.46/146.19/139.82 ms，新 102.38/102.72/101.68 ms，中位 146.19 → 102.38 ms（降低约 30.0%），分配 273,368,722 → 209,776,884 B/op。没有复现稳定回退。

隔离 PostgreSQL 18 的满池基准使用 32 个池，每池 4,096 条活动（128 条各含 8 KiB 随机状态的 turn，加 3,968 条 attempt），强制通用计划，并比较相同候选集。三轮旧查询 218.03/181.47/190.97 ms，新查询 25.01/25.00/22.57 ms；中位下降 86.9%。旧计划读取所有池的 4,096 条父 turn，新计划按本池与 turn key 查找。该数据来自隔离合成库，不是生产 DELETE 的执行测量。

## 验证与复现

受影响 service 单元测试与 race 覆盖时区、原文复用、重复键、Unicode、空图片、schema、OpenCode、兼容重试、OAuth 身份路径；handler 保留混合账号和逐次重试准备测试。SQL 使用本机 WSL Docker 的隔离 PostgreSQL 18，测试当前池与其他池同名 turn、active/protected 保护、淘汰顺序、过期清理和 generic plan。

结果：定向 service 单测通过（1.330s），race 通过（25.143s），handler 回归通过（6.082s），最后的 Grok/OpenCode 地址判定增量测试通过；数据库 integration、三轮基准和后端构建通过。第一次开发测试中的非法 fixture 和嵌套 metadata 重复父键差异已修复，回归包含这两个边界。

```bash
cd backend
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
filter='Test(OpenAIResponsesRetryLazyHash|.*OpenCode.*|.*RejectedField.*|OpenAIEmptyBase64InputImage|OpenAIResponsesToolSchema|CodexInputFilterCPUParity|CodexTransformCPUParity|CodexNativeTransformDoesNotForceSerialization|OpenAICodexRawReuse|OpenAICodexNoopWire|.*CodexOAuthTransform|.*CodexInput|.*RequestTimezone|.*Timezone|OpenAIIdentityPath|OpenAIOAuthIdentity)'
go test -p 2 -tags unit ./internal/service -run "$filter" -count=1
CGO_ENABLED=1 go test -p 2 -race -tags unit ./internal/service -run "$filter|TestCodexTelemetryRuntime" -count=1
go test -p 2 -tags unit ./internal/service -run '^$' \
  -bench '^(BenchmarkOpenAIResponsesRetryLazyHash|BenchmarkCodexTransformRetryCPU|BenchmarkCodexRawReuseGuard|BenchmarkOpenAIRequestTimezoneRetryCPU|BenchmarkOpenAIRequestTimezoneFieldScan|BenchmarkOpenAIEmptyBase64PredicateCPU|BenchmarkOpenAIResponsesToolSchemasCPU)$' \
  -benchmem -benchtime=100ms -count=3
go test -p 2 -tags integration ./internal/repository \
  -run 'TestCodexTelemetry(CompletedRuntime|IncrementalExpiry|IncrementalEviction|RuntimeSlots)' -count=1 -v
go test -p 2 -tags integration ./internal/repository -run '^$' \
  -bench '^BenchmarkCodexTelemetryCompletedRuntimeEvictionPlan$' -benchmem -benchtime=5x -count=3
go test -p 2 -tags unit ./internal/service -run '^$' \
  -bench '^BenchmarkCodexTransformRetryCPU$/^8MiB$/^image_true$/^attempts_5$' -benchmem -benchtime=1s -count=3
go build -p 2 ./cmd/server
```

较广的 OpenAI/Codex 单元选择遇到既有 `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback/503` 的 mock `GetByID` 空指针。已用 `a34cb7c77` 临时 Go overlay 单独复现相同调用链，保留失败与基线日志；没有修无关逻辑。前端没有修改，未运行前端检查。远端 CI 状态未核验，不将本地检查描述为远端通过。

## English

The deployed previous fixes were present. Under a burst of 1,241 rate-limit retries in three minutes, request preparation still repeatedly scanned/decoded large bodies. Two live CPU samples put Forward at 61.4% and 55.3% inclusive; database activity repeatedly showed telemetry capacity eviction.

This follow-up removes redundant timezone body construction, avoids serializing unchanged canonical OAuth input, speeds up conservative empty-image/schema checks, defers HTTP compatibility-retry hashing, and retains only a bounded OpenCode session scalar. Account-specific processing, authorization, integrity validation, retry budgets, billing and WS ownership remain intact. The telemetry SQL keeps parent-turn checks correlated to a single pool/key instead of allowing a generic plan to hash turns across pools.

Three-round synthetic measurements show the 32 MiB/eight-attempt timezone stage falling from 1.61 GB to 537 MB allocated per operation. The isolated full-pool SQL median fell from 190.97 ms to 25.00 ms. A noisy short OAuth benchmark was rerun without competing builds at one second per sample: its median improved from 146.19 ms to 102.38 ms. These stage measurements are separate from deployment-level results.

Focused tests, race, handler regressions, isolated PostgreSQL tests and the backend build passed. A broader selection stopped on a pre-existing history-notes mock panic, reproduced independently on the baseline. Raw evidence and both failure logs are retained alongside this report. There are no frontend changes or frontend checks in this batch. Remote CI was not verified. No production settings, model calls, Engine changes or deployment were performed.
