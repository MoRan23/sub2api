# 调度与模型目录 / Scheduling and model catalogs

基线为 `e6de1c8eb`。本项不需要迁移或 Wire 接线，不改变准入、授权槽位、模型映射、重试或计费规则。

Baseline: `e6de1c8eb`. No database migration or Wire registration is needed. Admission, authorization slots, routing, retries and billing remain unchanged.

## 实现 / Implementation

- Redis 每条 MGET 最多 128 个 Key，每波最多八条命令。2,000 个账号的元数据或 LastUsed 读取从 16 次顺序往返变成两波。结果顺序和缺失值保留，任何一波失败不返回部分成功结果。
- 同桶、同元数据版本的解码加载以及同写入代际的数据库回源分别合流。等待者独立取消，最后一个等待者退出后取消源查询。空结果不增加负缓存时间；每个请求仍使用独立 CandidateView / LastUsed。
- 影子母账号按唯一 ID 批量读取最小健康投影。Redis 缺失或损坏成员只补查对应数据库投影；候选通过后的最终检查仍重新读取。
- 模型目录使用无令牌、无代理关系图的账号投影，一次查询派生可见成员和目录成员；全部成员可见时直接共享不可变切片。只有真实选中的目录来源加载完整账号并解析授权，最多四个不同凭据所有者。
- 投影、Composite 每组精确键索引、固定源投影和最终 JSON/ETag 共用 `ModelCatalogStore`：15 秒 TTL、64 MiB 估算预算、最多 512 条，过大结果正常返回但不驻留。失效通知增加发布代际，迟到结果不能重新驻留。
- 最终目录键包含分组策略、投影内容版本、可见成员、OS、客户端版本、真实来源缓存身份、授权代际及源正文版本。缓存命中发生在真实来源授权解析之后。304 复用最终 ETag；内部只读源视图和最终响应借用不可变字节，不复制完整目录。
- Composite 未知型号只查已有索引，不增加缓存条目。精确大小写、空映射排除和跨平台歧义保持原行为。

Redis batches retain ordered results and use at most eight 128-key commands per wave. Shared loads retain independent cancellation and do not cache empty scheduler fallbacks. Shadow health reads use a minimal projection and retain fresh final admission checks. Model projections, exact Composite indexes, fixed-source projections and encoded responses share a 64 MiB / 512-entry / 15-second store. Source authorization precedes representation reuse; stale publishers are fenced. Unknown model lookups do not grow the cache.

## 三轮对照 / Three-run comparisons

环境：Windows amd64，AMD Ryzen 7 9700X，Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，`GOMAXPROCS=4`。Redis 为本机 WSL 内独立测试实例，测试 Key 使用独立前缀并在结束时清理。

The serial MGET and per-model ownership scan reproduce the baseline algorithms in paired benchmarks. Catalog comparison measures the existing full builder against the encoded 304 cache with the same 2,000-account fixture. Timings measure these operations; end-to-end request p95, steady process heap and peak RSS were not measured by these microbenchmarks.

| 场景 / Case | 基线三轮 / Baseline | 优化后三轮 / Updated | B/op 前 → 后 | allocs/op 前 → 后 |
|---|---|---|---|---|
| 2,000 Key Redis MGET | 6.728 / 6.867 / 6.958 ms | 1.625 / 1.607 / 1.618 ms | ~183,991 → ~184,722 | 6,050 → 6,070 |
| 2,000 账号 Composite 归属热查询 | 1.291 / 1.089 / 1.184 ms | 19.68 / 27.40 / 19.74 ns | ~2,480,000 → 0 | 8,000 → 0 |
| 2,000 账号目录条件响应 | 2.965 / 3.007 / 3.251 ms | 31.34 / 36.62 / 31.77 µs | ~1,464,657 → ~27,342 | 84–85 → 39 |

Redis 往返耗时约下降 76%，以每次约 731 字节和 20 次小分配换取两波 pipeline；目录条件响应分配量约下降 98%。索引指标是已构建索引的热查询，不包含首次投影加载成本。

Redis latency fell by about 76%, with roughly 731 extra bytes and 20 small allocations per operation. Conditional catalog response allocation fell by about 98%. Index numbers are warm lookups and exclude initial projection construction.

## 验证记录 / Validation records

- `raw/scheduler-targeted-tests.log`：新组件、调度、目录及 Composite 定向单元测试。
- `raw/scheduler-race.log`：取消、共享读取、版本隔离、并发刷新及投影测试的 race 结果。
- `raw/scheduler-postgres.log`：隔离 PostgreSQL 单连接临时表验证，检查投影列、凭据排除和父账号状态。
- `raw/scheduler-benchmark.log`：三轮完整对照原始输出。
- `raw/scheduler-broad-regression.log`、`raw/scheduler-baseline-existing-failures.log`：扩展回归及相同测试在原基线的失败对照。既有失败包含两个 OAuth 401 授权 mock、shadow installation profile、Codex display snapshot mock 和 Grok catalog CAS mock。
- 保留的 `*-initial.log` / `*-intermediate.log` 记录早期编译缓存读取失败及已修复的缓存清除测试失败；最终结果以无后缀的对应日志为准。

## 复现 / Reproduction

在 `backend` 目录运行；以下 DSN 仅指向专用测试实例。PowerShell 用户需使用相应环境变量语法；本次通过 Git Bash 执行。

```bash
export PATH=/d/gosdk/go1.26.5/bin:$PATH
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4

go test -p 1 -tags unit ./internal/pkg/sharedflight ./internal/service ./internal/repository \
  -run 'TestIndependentCancellation|TestLastWaiter|TestCompletedEmpty|TestCancelStops|TestLoadPanic|TestModelCatalogStore|TestCompositeOwnershipIndex|TestCatalogProjection|TestCatalogSharedProjection|TestPinnedCatalogSource|TestCatalogSourceView|TestCatalogSourceHydration|TestGroupCatalogRepresentation|TestScheduler|TestShadowParentsBatch|TestShadowHealth|TestResolveCompositeModelOwnership|TestOpenAIGroupCatalog' -count=1

go test -race -p 1 -tags unit ./internal/pkg/sharedflight ./internal/service ./internal/repository \
  -run 'TestIndependentCancellation|TestLastWaiter|TestCompletedEmpty|TestCancelStops|TestLoadPanic|TestModelCatalogStore|TestCompositeOwnershipIndex|TestCatalogProjection|TestCatalogSharedProjection|TestPinnedCatalogSource|TestCatalogSourceView|TestCatalogSourceHydration|TestGroupCatalogRepresentation|TestSchedulerCandidate|TestSchedulerCacheSnapshotMemo(Delete|Concurrent)|TestSchedulerMGet|TestSchedulerLateMissing|TestShadowHealth|TestShadowParentsBatch' -count=1

TEST_REDIS_URL='redis://127.0.0.1:56381/0' go test -p 1 -tags unit \
  ./internal/service ./internal/repository -run '^$' \
  -bench 'BenchmarkCompositeOwnershipComparison|BenchmarkGroupCatalogRepresentationComparison|BenchmarkSchedulerMGetComparison' \
  -benchmem -benchtime=500ms -count=3

TEST_SCHEDULER_PG_DSN='postgres://postgres:local_perf_test@127.0.0.1:55435/postgres?sslmode=disable' \
  go test -p 1 -tags unit ./internal/repository -run '^TestCatalogAndShadowProjectionsPostgres$' -count=1
```

回退使用本项之前的应用版本即可，未修改持久化格式。已有目录和调度缓存格式保持兼容，进程本地派生缓存重启后重建。

Rollback uses the previous application build. Persistent formats are unchanged; process-local derived caches rebuild after restart.
