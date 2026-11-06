# Ordinary usage billing lifetime / 普通 usage 计费对象生命周期

## 中文

此次改动覆盖现有 `UsageRecordWorkerPool` 的普通 usage 提交边界。Engine 的 confirmed-events 持久任务、ledger、事件模型和价格协议沿用原链路；Engine 普通 `RecordUsage` 的空操作仍保留。

17 个 handler 提交点统一经过 `submitFrozenUsageRecord` 或 `submitFrozenOpenAIUsageRecord`，在入队前生成独立计费投影。覆盖 OpenAI Responses / Messages / WS / Chat Completions / embeddings / images / search，Anthropic 与兼容入口，Gemini 原生与 cached-content 存储费，SystemOne，以及 Grok 音频和视频。`gateway_handler.go` 和 `openai_gateway_handler.go` 内分别包含多个入口，其余入口位于对应 handler 文件。

投影只复制 `RecordUsage` 消费的 usage、模型、媒体计数、已有计价时刻、渠道归属、请求指纹和业务字段。账户、用户、API key、分组、订阅使用显式字段白名单；需要的嵌套定价 map、interval 指针、时间价格、媒体 map/slice 和通知收件人均独立复制，字符串拥有自己的存储。完整认证图、凭据 token、Gin、完整 headers、WS replay、SystemOne response body 和 Grok request body 不跨队列边界。上游请求 ID 从 headers 同步提取，Grok payload hash 在提交前计算。API key 的 Key 值仍用于原有认证缓存失效。

这不是新的计价协议。入队只冻结已确定字段；用户分组专属倍率、渠道/全局价格查询、影子账户父账户配置和既有媒体价格 hydration 仍在原 worker 的 `RecordUsage` 时点执行。请求原先已提供的 `PricingAt` 保持原值，未提供时仍使用原有回退规则。

为降低小正文请求的固定分配成本，input、result 和常见业务对象内嵌在同一自有 owner 中；数字与可选字符串指针使用自有标量存储，超出固定容量时使用独立扩展存储。字符串统一复制到 `strings.Builder` 构造的自有区域，返回后不再写入。计费 Account 使用私有 typed backing 保存 plan、模式、配额与通知值，避免通用 Credentials/Extra map 的分配与数值装箱；普通账户仍沿用原 getter 路径，负零、NaN、无穷值和历史数字编码的结果保持一致。影子账户继续读取 worker 时点的父配置，并保留物理请求已确定的 plan。真实认证缓存发布时创建独立只读计费 Group，后续任务可复用该版本；其他 Group 按需单独分配并深复制全部价格字段。常见 owner 不再内嵌未使用的 Group。

任务只保留两个独立追踪字符串 `ClientRequestID` / `RequestID`，在 worker 自身的 background timeout context 上恢复；不保留父 context 的 value 链、取消和 deadline。预留句柄在提交前取得，执行完成或 panic 通过原 `done()` 释放，未入队通过 abandon 释放；handler 完成与任务持有之间的所有权交接保持一致。缺池/停止池的同步 fallback、显式 drop/sample 溢出策略、媒体 mandatory 提交、Grok 失败 claim 释放以及原请求 ID/指纹幂等规则全部沿用。

队列完成数和业务结算调用结果分开统计：`settlement_succeeded` / `settlement_failed` 在两条普通 `RecordUsage` 返回后各计一次，排队与同步 fallback 使用同一闭包。成功表示方法返回 nil，包含幂等重复和原有不扣费模式的成功返回，不能将其直接当作实际扣费笔数。Engine 普通入口空操作排除在该计数之外；持久事件 ledger 不在此次计数范围。计数器不保留 error 内容或请求数据。

在途预估的两处账户模型映射读取也改用 `ListSchedulableCandidates`，直接读取只读候选 view，避免为整组账户分配可写副本；模型映射候选与去重语义保持一致。

### 基准方法

`internal/service/usage_record_retention_benchmark_test.go` 中的 `BenchmarkUsageRecordQueueRetainedHeap` 使用真实 worker pool，固定 1 个 worker 并用 gate 阻塞执行。每个 case 入队 32 或 128 个任务，每任务各自持有独立的 1 MiB 输入，分别模拟 WS replay、SystemOne response body 和 Grok request body。基线与候选使用相同输入、队列大小、worker 数和溢出设置；候选经过生产投影边界。

在建立空队列后与积压完成后分别 GC 并读取 `runtime.MemStats.HeapAlloc`，差值报告为 `retained-B/task` 和 `retained/payload`。结束时释放 gate、停止池，并核对任务全部完成。该 fixture 使用最小业务主体，量化的是正文因排队而延长的生命周期；完整生产记录的大小取决于其定价与通知配置。`B/op` / `allocs/op` 表示累计分配量，`retained-B/task` 表示 GC 后存活量；`ns/op` 包含 GC，不用作计费延迟或吞吐指标。

基线二进制在生产修改前由统一测试保存。三轮基线/候选数值和资源记录见统一基准报告，本文不另填未经统一测量的数据。复现实例（从 `backend` 目录运行）：

```sh
go test ./internal/service -run '^$' -bench '^BenchmarkUsageRecordQueueRetainedHeap$' -benchtime=1x -count=3 -benchmem
```

补充的完整生命周期基准在 `internal/service/usage_record_lifecycle_benchmark_test.go`。它在同一二进制中比较 `legacy_capture` 与生产投影 `owned_snapshot`，固定 16 个生产者、4 个 worker 和 128 队列容量，覆盖三种正文的 8 KiB / 1 MiB 输入。计时包含正文复制、JSON 校验、模型/usage 提取、入队和本地价格查询/计费命令构造；没有模拟网络/数据库等待、sleep 或人为忙循环。每次请求从正文处理开始，等 worker 完成回传后记录 p95。强制 GC、延迟排序和清理位于计时区间之外；运行时自然 GC 仍属于测量成本。

新增 `BenchmarkUsageRecordLifecycleAuthenticated` 在两种所有权模式的每次请求中都执行真实 `GetByKey` L1 命中；计时前经一次真实 miss 发布认证缓存及独立分组快照，未预设内部冻结标记。原有 `BenchmarkUsageRecordLifecycle` 保留未发布 Group 的完整深复制 fallback，对照数据分别记录。p95 的终点是本地 worker 落账步骤完成并唤醒生产者。

该基准报告 `requests/s`、`p95-e2e-ns`、`B/op`、`allocs/op`，以及 Go runtime 的 user + GC + scavenger CPU 估计 `runtime-cpu-ns/op`；进程 CPU 由统一运行器另外采集。建议固定 GOMAXPROCS，使用 `-bench '^BenchmarkUsageRecordLifecycle$' -benchtime=2s -count=3 -benchmem`。它衡量受控本地生命周期，最终性能结论使用这组延迟/吞吐与保留堆结果共同判断。

### 回归证据

- `usage_record_snapshot_test.go`：原对象修改后投影保持独立；OpenAI、图片、搜索、音频、视频、订阅、simple、Anthropic、Gemini、SystemOne 的 usage log 与计费命令等价；实际 worker 时点仍读取实时倍率与父账户配置；Engine 普通链保持空操作。
- handler context/reservation 测试：提交后不再访问父 context，父取消不传递到 worker，追踪 ID 保留，panic 与 abandon 保持预留的单次释放。既有队列测试覆盖正常提交、停止池同步 fallback、丢弃策略和 mandatory 媒体路径。
- `TestUsageRecordSnapshotDetachedBillingIntegration`（`repository`、`integration` tag）：真实 PostgreSQL/Redis 普通 API key 账户，冻结后修改原 usage/User/Account/API key 并取消父 context，独立 worker 仍按冻结数据写日志和扣费；预留在任务持有期间继续生效，释放后清空；重复结算不再扣费或新增日志。统一集成运行已通过。
- `TestInflightEstimate_AccountMappingUsesCandidateViews`：两条预估入口使用候选接口，复用只读账户引用，精确/通配映射与去重结果一致。

投影/worker/handler 定向 unit 与 race、默认 tag 的投影/context 回归、inflight mapping race 均已通过。新增结算计数精确 race 也已通过，覆盖两主干、排队/停止/缺池和成功/错误组合，并验证 Engine 空操作不计数。

## English

This change covers ordinary usage submissions to the existing `UsageRecordWorkerPool`. Engine confirmed-events jobs, their durable ledger, event model and pricing protocol keep their existing path. The ordinary Engine `RecordUsage` no-op is preserved.

All 17 handler submission sites now use `submitFrozenUsageRecord` or `submitFrozenOpenAIUsageRecord` to build an owned billing projection before enqueueing. Coverage includes OpenAI Responses, Messages, WS, Chat Completions, embeddings, images and search; Anthropic and compatible routes; Gemini native and cached-content storage charges; SystemOne; and Grok audio/video. The main gateway handler files contain multiple submission sites.

The projection copies only fields consumed by billing: usage, models, media counts, an already-established pricing time, channel attribution, request fingerprints and business settings. Explicit allowlists project Account, User, APIKey, Group and Subscription. Nested pricing maps, interval pointers, time pricing, media maps/slices and notification recipients are copied independently, and retained strings own their storage. Full authentication graphs, credential tokens, Gin, complete headers, WS replay, SystemOne response bodies and Grok request bodies do not cross the queue boundary. The upstream request ID is extracted synchronously from headers; Grok payload hashing completes before submission. The API key value remains available for existing authentication-cache invalidation.

Pricing timing is unchanged. Submission freezes already-known fields only. User/group rate overrides, channel/catalog lookup, shadow-parent billing settings and existing media-price hydration still run at the original worker-time `RecordUsage` boundary. Existing `PricingAt` values and missing-value fallback behavior are preserved.

To reduce fixed allocation cost for small bodies, the input, result and common business objects are embedded in one owned allocation. Optional scalar pointers use owned backing storage, with independent overflow storage when needed. Strings are copied into an owned `strings.Builder` arena that is no longer written after construction. Billing Accounts use private typed backing for the plan, mode, quota and notification values, avoiding generic Credentials/Extra map allocation and numeric boxing. Ordinary Accounts retain their original getter path; negative zero, NaN, infinities and legacy numeric encodings preserve their results. Shadow billing still reads current parent settings in the worker while preserving the physical request's established plan. Real authentication-cache publication creates an independent read-only billing Group that later tasks can reuse. Other Groups allocate their complete deep-copy fallback separately, so the common owner no longer embeds an unused Group.

The task owns only two trace strings, `ClientRequestID` and `RequestID`, restored onto the worker's own background timeout context. It retains no parent context value chain, cancellation or deadline. Reservation ownership is acquired before submission and released by the original `done()` on completion or panic; abandonment releases a task that is not executed. Missing/stopped-pool synchronous fallback, configured drop/sample overflow, mandatory media submission, Grok failure claim release and existing request-ID/fingerprint idempotency remain intact.

Queue completion and settlement-call outcomes are distinct. `settlement_succeeded` / `settlement_failed` count each ordinary `RecordUsage` return once; queued and synchronous fallback execution share the same closure. Success means the method returned nil, including idempotent duplicates and existing no-charge modes, so it is a call-outcome count rather than a count of actual debits. Engine ordinary no-ops are excluded, and the durable Engine ledger is outside this counter. Counters retain neither error contents nor request data.

The two inflight-estimate account-mapping readers also use `ListSchedulableCandidates`. They read immutable candidate views instead of allocating writable copies of a whole account group, while preserving mapped-model and deduplication behavior.

### Benchmark method

`BenchmarkUsageRecordQueueRetainedHeap` in `internal/service/usage_record_retention_benchmark_test.go` uses the real worker pool with one worker blocked on a gate. Each case queues 32 or 128 tasks with distinct 1 MiB inputs representing WS replay, a SystemOne response body or a Grok request body. Baseline and candidate keep the input sizes, queue sizes, worker count and overflow configuration identical; the candidate crosses the production snapshot boundary.

GC and `runtime.MemStats.HeapAlloc` measurements run before enqueueing and after the backlog is established. The difference yields `retained-B/task` and `retained/payload`. Cleanup releases the gate, stops the pool and checks that every task completed. Minimal business subjects isolate payload lifetime; a full production record also depends on its pricing and notification configuration. `B/op` and `allocs/op` describe cumulative allocation, while retained heap describes memory still live after GC. Benchmark `ns/op` includes GC and is not a billing-latency or throughput measurement.

Baseline binaries were saved before production changes. The consolidated benchmark report owns the three-run baseline/candidate numbers and resource records. The command above reproduces the retention fixture from the `backend` directory.

The complementary lifecycle fixture in `internal/service/usage_record_lifecycle_benchmark_test.go` compares `legacy_capture` and the production `owned_snapshot` builder in one binary. Both use 16 producers, 4 workers and queue capacity 128, with 8 KiB and 1 MiB inputs for all three payload shapes. Timed work includes body copying, JSON validation, model/usage extraction, submission and local pricing/command construction. There are no simulated network/database waits, sleeps or artificial busy loops. Per-request p95 starts before body processing and ends after worker completion reaches the producer. Forced GC, latency sorting and cleanup are outside timing; natural runtime GC remains part of the measured cost.

The separate `BenchmarkUsageRecordLifecycleAuthenticated` runs a real `GetByKey` L1 hit on each request in both ownership modes. An actual miss publishes the cache and its independent billing Group before timing; the fixture never assigns a private frozen marker. The original `BenchmarkUsageRecordLifecycle` retains the complete fallback for unpublished Groups, with separate results. The p95 endpoint is local worker settlement completion and producer wake-up.

It reports `requests/s`, `p95-e2e-ns`, `B/op`, `allocs/op` and the Go runtime user + GC + scavenger CPU estimate `runtime-cpu-ns/op`. Collect process CPU externally in the consolidated runner. Use fixed GOMAXPROCS and `-bench '^BenchmarkUsageRecordLifecycle$' -benchtime=2s -count=3 -benchmem`. This controlled local lifecycle supplies latency/throughput evidence alongside the retained-heap measurement.

### Regression evidence

- Snapshot tests verify independence after source mutation and matching usage logs/billing commands for OpenAI, image, search, audio, video, subscription, simple mode, Anthropic, Gemini and SystemOne. They also verify worker-time rate/parent reads and the Engine no-op.
- Handler tests check that queued work never reads the parent context after submission, retains trace IDs, survives parent cancellation and releases reservations once on panic/abandon. Existing worker tests cover normal queueing, stopped-pool fallback, configured dropping and mandatory media behavior.
- `TestUsageRecordSnapshotDetachedBillingIntegration` uses real PostgreSQL and Redis with an ordinary API key account. It mutates source usage/User/Account/APIKey data after freezing, cancels the parent, then verifies detached billing, logs, reservation retention/release and duplicate-call idempotency. The consolidated integration run passed.
- `TestInflightEstimate_AccountMappingUsesCandidateViews` checks both estimate readers, shared immutable references and exact/wildcard model deduplication.

Targeted unit/race suites, default-tag snapshot/context tests and inflight mapping race checks passed. The focused settlement counter race suite also passed: both billing services, queued/stopped/missing-pool execution, success/error returns and exclusion of Engine ordinary no-ops.
