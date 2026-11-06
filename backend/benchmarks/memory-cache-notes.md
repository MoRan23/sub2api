# Shared scheduling cache / 共享调度缓存

## 中文

候选账户的精简元数据在进程内按 **账户 ID + Redis 元数据 JSON 的 SHA-256 内容指纹 + 运行时模型映射版本** 共享。相同账户同时存在于多个分组时，只保留一份相同版本的已解码母本和预计算模型映射。分组条目保留成员顺序、内容版本引用及原有 active/revision 标签；分组优先级顺序仍来自原始快照。

内容指纹在元数据加载或刷新时计算，热命中不重复计算。运行时模型映射版本变化会触发新母本构造；并发读取旧母本时，映射失配走只读解析路径。`Account.SchedulingRevision` 仍服务原有调度控制语义，并不覆盖所有元数据改动，因而不用于共享池版本键。

Redis 中的键、JSON 格式、bucket 有序 ID、全局/平台失效标签、epoch fencing 和退休/重开协议保持原协议。每次请求仍检查 Redis 标签并读取最新 LastUsed 侧键；分组条目最长复用 **10 秒**，到期重新读取元数据，保留未推进标签的旧写入及 Redis 恢复场景下的刷新兜底。

| 参数 | 默认/语义 |
| --- | --- |
| `gateway.scheduling.snapshot_cache_max_bytes` | `67108864`（64 MiB），每进程共享池与分组索引合计软预算 |
| `GATEWAY_SCHEDULING_SNAPSHOT_CACHE_MAX_BYTES` | 上述配置的环境变量 |
| 显式配置 `0` | 使用 64 MiB 默认值；负值校验失败 |
| 本地 idle 期限 | 100 秒；活跃读取会更新访问时间 |
| 周期清理 | 每 10 秒检查一次；闲置对象可在下一次检查时回收 |
| 最大标签复用期 | 10 秒，和 idle 期限独立 |

字节计数估算账户结构、嵌套 map/slice、派生映射与分组索引的驻留量，用于缓存准入和淘汰。超预算时按最近访问时间淘汰并预留少量空间；单条对象或分组索引大于预算时，本次请求正常使用但不驻留。请求已持有的对象由 Go 引用维持至请求结束，因此该预算控制缓存拥有的估算驻留量，进程 RSS 还包含请求、Redis 客户端、运行时及其他组件。分组只保存内容引用，池淘汰后不会被分组的强引用继续留住。

`CandidateSet` 中每项是只读 `Account` 引用与请求私有 LastUsed sidecar。筛选、评分、排序和随机打散操作小型 view/索引结构，热路径不分配整组 `Account` 副本。阈值或 Fable 模型限流确需修改某个候选时，只对命中的账户创建请求私有顶层副本；嵌套 Extra/model-rate-limit 使用已有写时复制规则。完整凭据仍在原有 hydration/final-check 阶段加载，完整 `GetAccount` 对象保持可写。

冷加载仍需读取 Redis 中各 bucket 的有序 ID 和元数据；内容池命中可省掉重复 JSON 解码和派生映射构造。这一阶段没有新增 Redis 单账号版本协议，因此跨 bucket 的冷加载元数据网络量仍按既有读取协议产生。并发相同账户内容的构造用 singleflight 合并，idle 扫描按 10 秒节流，避免每插入一个账户都扫描整个池。

旧 `GetSnapshot` / `ListSchedulableAccounts` API 保留原来的可写顶层副本约定，兼容调用仍会分配 Account 切片。迁移后的生产候选入口使用 `GetCandidateSnapshot` / `ListSchedulableCandidates`，新增候选调用应选择新接口。

运行指标使用固定名称：`scheduler_unique_accounts.entries` 是唯一账户数；`scheduler_accounts.entries` 是元数据版本份数；`scheduler_buckets` 是分组条目；`scheduler_cache_total` 给出共享总字节与预算。总量行已包含子项，统计展示应避免再次相加。

验证覆盖 2,000 账户 × 100 分组共享母本、内容变更、新旧请求隔离、LastUsed sidecar、预算淘汰/大条目不驻留、idle 回收、并发读与删除、模型映射版本变化、组生命周期 fencing。真实 Redis 集成测试还覆盖两个缓存实例的成员更新与客户端断线重连。读侧基准在 `internal/repository/scheduler_cache_read_benchmark_test.go`，最终性能数值由统一基准报告给出。

补充矩阵 `BenchmarkSchedulerCacheSharingModes` 位于 `internal/repository/scheduler_cache_sharing_modes_benchmark_test.go`，固定 2,000 个唯一账户和 100 个分组：共享部分出现在每个分组，剩余账户互斥分配。因此 0%／50%／100% 共享率对应每组 20／1,010／2,000 个候选。每个 op 读取全部 100 个分组；`bucket-reads/op`、`account-views/op` 明示工作量，`p95-sweep-ns` 表示整轮延迟。冷模式每轮新建本地缓存并复用预置 Redis；热模式预热全部分组后测量。可使用 `-benchtime=3x` 限制轮数，停止计时后的 GC 不计入冷读时间。不同共享率的总成员数不同，应结合成员工作量比较，原版与新版在同一共享率下执行相同请求。

## English

Slim scheduling metadata is interned per process by **account ID, SHA-256 of the Redis metadata JSON, and the runtime model-mapping version**. An account shared by several groups uses one immutable decoded master per content version, including prepared model mappings. Bucket entries retain ordered membership, content references, and the existing active/revision tags. Group-specific ordering remains the ordering published in the snapshot.

Content identities are computed on metadata loads and refreshes, not on warm hits. A runtime mapping-version change constructs a new master; readers of an older master use a read-only mapping fallback when necessary. `Account.SchedulingRevision` retains its existing scheduling-control meaning. It does not cover every metadata change and is not used as the shared-pool version.

The Redis key layout, JSON representation, ordered bucket IDs, global/platform invalidation tags, epoch fencing, and retire/reopen protocol remain compatible. Each read still checks Redis tags and fetches fresh LastUsed side keys. A decoded bucket is reused for at most **10 seconds** before metadata is reloaded. This freshness bound remains independent of eviction and covers old writers that do not advance tags and Redis restoration scenarios.

| Setting | Default / behavior |
| --- | --- |
| `gateway.scheduling.snapshot_cache_max_bytes` | `67108864` (64 MiB), combined per-process account-pool and bucket-index soft budget |
| `GATEWAY_SCHEDULING_SNAPSHOT_CACHE_MAX_BYTES` | Environment override |
| Explicit `0` | Uses the 64 MiB default; negative values are rejected |
| Idle retention | 100 seconds, refreshed by access |
| Cleanup cadence | 10 seconds; idle entries are removed on a subsequent sweep |
| Maximum tag reuse | 10 seconds, independent of idle retention |

Admission estimates include account structs, nested maps/slices, prepared mappings, and bucket indexes. Budget pressure evicts by last access and leaves some headroom. A single object or bucket index larger than the budget serves the current request without being retained. In-flight readers keep their references until they finish. The budget therefore bounds estimated cache-owned retention; process RSS also includes active requests, Redis clients, the Go runtime, and other components. Buckets retain content keys rather than strong master references, so removing pool entries releases cache ownership.

Each `CandidateSet` item contains a read-only Account reference and request-local LastUsed state. Filtering, scoring, sorting, and shuffling operate on small views/indexes, without copying an entire Account slab on the warm request path. Threshold/Fable updates create a private top-level copy only for affected accounts, using the existing copy-on-write rules for nested Extra/model-rate-limit data. Full credentials are still loaded at the existing hydration and final-check boundaries; `GetAccount` remains request-owned and writable.

Cold reads still fetch bucket IDs and metadata from Redis. Pool hits avoid repeated JSON decoding and derived-map construction across buckets. This phase does not add a per-account Redis version protocol, so cold cross-bucket metadata traffic follows the existing protocol. Singleflight coalesces concurrent construction for identical account content. Idle scans are throttled to the cleanup cadence rather than scanning the growing pool for every inserted account.

Legacy `GetSnapshot` and `ListSchedulableAccounts` preserve their writable top-level copy contract and still allocate Account slices when called. Migrated production candidate readers use `GetCandidateSnapshot` and `ListSchedulableCandidates`; new candidate consumers should use those APIs.

Fixed metric rows distinguish `scheduler_unique_accounts.entries` (unique account IDs), `scheduler_accounts.entries` (metadata versions), `scheduler_buckets`, and `scheduler_cache_total` (combined estimated bytes and budget). Do not add the total row to its component rows again.

Tests cover 2,000 accounts shared across 100 groups, content changes, old/new reader isolation, LastUsed sidecars, budget/oversize/idle behavior, concurrent readers and deletion, runtime mapping changes, and lifecycle fencing. Real Redis integration additionally covers membership updates across two cache instances and client disconnection/reconnection. The read fixture is `internal/repository/scheduler_cache_read_benchmark_test.go`; measured performance belongs in the consolidated benchmark report.

The additional `BenchmarkSchedulerCacheSharingModes` fixture fixes 2,000 unique accounts and 100 groups. The shared fraction appears in every group; the remaining accounts are distributed disjointly. At 0%, 50%, and 100% sharing, each group therefore has 20, 1,010, and 2,000 candidates. Each operation reads all 100 groups. `bucket-reads/op` and `account-views/op` disclose the workload, while `p95-sweep-ns` is the full-sweep latency. Cold mode creates an empty local cache per sweep against prepopulated Redis; warm mode preloads every group. Use `-benchtime=3x` for bounded runs. Between cold sweeps, GC runs with the benchmark timer stopped. Compare old and new implementations at the same sharing fraction; different fractions also change total group membership.
