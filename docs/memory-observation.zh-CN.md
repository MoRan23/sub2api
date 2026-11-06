# 运行时内存观测

[English](memory-observation.md)

管理员 Ops 仪表盘提供“内存观测”卡片，可按 Sub2API 进程查看 Go 运行时内存、进程 RSS、调度缓存估算、用量任务队列、逻辑请求体大小和 SSE 缓冲区容量。采样与计数仅保存数值诊断和固定缓存名称，不保存正文、请求头、凭据、提示词或缓存键。

## 部署与开关

内存观测复用现有 Ops 开关：

```yaml
ops:
  enabled: true
```

`ops.enabled` 默认 `true`，对应环境变量 `OPS_ENABLED`。运行时设置 `ops_monitoring_enabled` 也需开启；设置未保存时沿用现有的启用默认值。采样器读取内存中发布的 Ops 设置。关闭 Ops 后停止新增采样，并拒绝访问这些管理员接口。堆快照下载使用单独的临时开关，**默认关闭**。

每个应用进程启动后立即采样，随后每 **10 秒**采样一次。此间隔固定，不受 `ops_metrics_interval_seconds` 控制；原有指标采集器的 leader lock 不会将内存采样限制在一个副本上。历史数据无需新增数据库表或写入数据库。

每个进程启动时生成一个 UUID，与 Ops 指标采集器共用。重启后获得新的实例 ID。ID 不含主机名、Pod 名、账户 ID 或请求 ID。负载均衡环境中的 `current_instance_id` 表示本次 API 请求实际到达的进程。

## 历史与可用状态

| 限制或状态 | 含义 |
| --- | --- |
| 本地历史 | 固定 360 点的环形缓冲区，10 秒采样下约为一小时。 |
| Redis 历史 | 每个已登记实例最多 360 点，每次采样刷新 70 分钟 TTL。 |
| Redis 实例目录 | 最多 128 个实例。达到上限时保留已有记录直到过期，暂不登记新实例；新实例继续保存本地历史。 |
| `truncated` | 过去 70 分钟内发生过实例目录满额，部分节点可能仅有本地历史。当前处理请求的进程仍会出现在响应中。 |
| `stale` | 实例最新采样距今超过 30 秒，或尚无采样。停止的实例保留到 Redis 记录过期。 |
| `redis_available: false` | 跨实例历史存储读取失败或未配置；当前进程继续返回本地历史。 |
| 历史不可用 | 所选实例没有可读取的采样，例如记录已经过期或 Redis 故障。 |
| `rss_available: false` | 本次无法读取 RSS。界面显示不可用，数值占位零不按真实驻留内存处理。 |

Redis 键使用 `ops:runtime:memory:` 前缀。需要共享历史的副本应使用同一个已配置 Redis 存储。旧实例和重启后新实例的累计指标分别展示。本地历史随进程退出而消失，Redis 按固定保留期保存最后一批样本。

## 指标含义

| 字段 | 含义 |
| --- | --- |
| `rss_bytes` | 操作系统统计的进程驻留内存字节数。 |
| `sys_bytes` | Go 运行时从系统取得的内存总量，对应 `/memory/classes/total:bytes`。 |
| `heap_alloc_bytes`、`heap_objects` | 存活或尚未被回收的堆对象字节数与数量。 |
| `heap_inuse_bytes` | 堆对象字节数，加上已使用堆 span 内尚未占用的空间。 |
| `heap_idle_bytes`、`heap_released_bytes` | 空闲堆 span 字节数（包含已归还部分），以及其中已归还系统的部分。 |
| `stack_inuse_bytes`、`goroutines` | 运行时堆栈内存字节数与当前 goroutine 数。 |
| `alloc_bytes_total`、`free_bytes_total` | 进程启动以来累计分配与释放的堆字节数。分配速率用相邻采样差值除以实际时间间隔，首点没有速率。 |
| `gc_cycles`、`gc_pause_seconds` | 累计 GC 周期数，以及将运行时累计 GC 暂停直方图合并到固定桶后的结果。 |

RSS 与 Go 运行时指标使用不同的内存统计口径，结合流量和缓存活动观察趋势。采样器使用 `runtime/metrics`，不强制触发 GC。

### 请求体与 SSE

`activity.active_bodies`、`active_body_bytes` 表示正在处理的请求中已观察到的逻辑入站正文。开始读取字节后才计入，大小取累计 wire 读取量与解码、规范化后正文大小的最大值。预读取和重复读取同一正文共用计数，最外层请求中间件返回时释放数值统计。观测包装器只保存数字，原样转发底层读取结果和 Close；原有正文限额保持生效。

该指标统计逻辑正文大小，正文副本和中间分配的内存另由堆/RSS 趋势反映。`active_large_bodies` 与 `active_large_body_bytes` 统计其中 **不小于 1 MiB** 的正文。每个非空请求作用域结束时，向 `body_size_bytes` 记录一次最终观察大小。

对于已接入观测的 SSE relay，`active_sse` 表示活跃流数量。`active_sse_buffers` 统计非空 raw/scratch 底层缓冲区个数，`active_sse_buffer_bytes` 按实际 **capacity** 累加，包含尚未使用的容量。缓冲区随帧在读取与写出协程间交接，扩容、丢弃与释放时更新计数；流清理结束后释放其全部跟踪容量。`sse_buffer_max_bytes` 和 `sse_buffer_size_bytes` 分别记录进程启动以来观察到的 raw 加 scratch 容量最大值与分布。

### 调度缓存

解码后的调度账户池与 bucket 索引共用默认 **每进程 64 MiB 软预算**：

```yaml
gateway:
  scheduling:
    snapshot_cache_max_bytes: 67108864
```

对应环境变量为 `GATEWAY_SCHEDULING_SNAPSHOT_CACHE_MAX_BYTES`。修改部署配置后重启进程生效。

卡片展示 `scheduler_accounts`、`scheduler_buckets` 与 `scheduler_cache_total`。总量行包含前两项，并携带共享 `max_bytes` 预算，汇总时只使用总量行。`estimated_bytes` 估算缓存拥有的对象和索引，RSS、堆指标反映更广的进程内存。被缓存驱逐的不可变快照，如仍被某个请求使用，会在该请求释放引用后才能回收。

`entries`、`hits`、`misses`、`evictions` 都是本地缓存统计。`ttl_seconds` 为闲置保留阈值，当前为 100 秒；缓存压力可使条目提前被驱逐。

### 用量任务队列与直方图

`usage_queue` 复用现有 worker pool 的统计：配置队列容量 `capacity`、等待数 `waiting`、运行数 `running`、提交数 `submitted`、执行完成数 `completed`、失败数 `failed`、丢弃数 `dropped`、同步回退数 `sync_fallback`。这些字段描述任务调度和闭包执行。独立的 `settlement_succeeded` 与 `settlement_failed` 按普通用量计费操作返回的成功或错误结果各记录一次，包含同步回退，按进程累计调用结果。幂等去重后返回成功也计为成功调用；Engine 接管用量后的 no-op 路径不计入。实际扣费详情仍由既有计费记录和错误处理提供。

`wait_ms` 统计成功入队任务从提交到开始执行的等待时间。`execution_ms` 统计执行时长，包含队列溢出后的同步回退。时间桶上界为 `[1, 10, 100, 1000, 10000, 60000]` 毫秒，末桶保存更大值。大小桶上界为 `[1024, 16384, 65536, 262144, 1048576, 8388608]` 字节，末桶保存更大值。各桶计数自进程启动累计，桶间互斥。

## 管理员 API

以下路径均以 `/api/v1/admin/ops` 为前缀，复用现有管理员认证中间件。管理员 JWT 通过 `Authorization: Bearer <token>` 传递；管理员 API key 遵循现有密钥权限。普通网关 key 与非管理员用户无法访问。

| 方法与路径 | 请求 | 响应 |
| --- | --- | --- |
| `GET /runtime/memory` | 可选 query `instance_id`，省略时选择当前处理请求的进程。 | 标准 JSON 信封，`data` 为下述内存视图。 |
| `POST /runtime/heap-profile/enable` | JSON `{"instance_id":"<当前进程 UUID>"}`。 | 标准 JSON 信封，`data` 为 `{enabled, expires_at, in_progress}`。 |
| `POST /runtime/heap-profile/disable` | 同上。 | 同上。 |
| `GET /runtime/heap-profile/download` | 必填 query `instance_id=<当前进程 UUID>`。 | gzip 编码的二进制 Go heap profile；`application/octet-stream`、附件文件名与 `Cache-Control: no-store`。 |

内存视图包含 `current_instance_id`、`selected_instance_id`、`sample_interval_seconds`、`history_limit`、`stale_after_seconds`、`redis_available`、`truncated`、`instances`、`history` 和 `profile`。实例包含 `instance_id`、`started_at`、`last_seen_at`、`stale`。采样包含 `at`、内存与运行时字段、`activity`、`caches`、`usage_queue`。即使选择其他实例的历史，`profile` 始终表示当前处理请求的进程状态。

历史查询 ID 格式错误返回 400；profile 目标与当前进程不一致返回 409；下载开关关闭或过期返回 403；已有下载进行中返回 409；关闭 Ops 返回 404；运行时内存服务未提供返回 503。认证失败沿用现有 401/403 响应。

## 临时堆快照下载

在卡片中选择当前进程，启用堆快照下载，下载后关闭。每次启用授予 **30 分钟**的本地权限，再次启用会续期；到期或进程重启后自动关闭。手动关闭会阻止新下载，已经开始的传输可以完成。

每个进程同时最多进行 **一个堆快照下载**。处理器将运行时现有采样堆快照直接写入 HTTP 响应，不创建服务端临时文件，不强制 GC，也不修改 `runtime.MemProfileRate`。快照包含运行时分配调用栈与对象采样统计。

使用已有管理员 JWT 的示例：

```bash
BASE_URL='https://your-sub2api.example'
# 在本地环境中将 ADMIN_TOKEN 设为已有管理员 JWT。
INSTANCE_ID=$(curl --fail --silent --show-error \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$BASE_URL/api/v1/admin/ops/runtime/memory" | jq -r '.data.current_instance_id')

curl --fail --silent --show-error \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  --data "{\"instance_id\":\"$INSTANCE_ID\"}" \
  "$BASE_URL/api/v1/admin/ops/runtime/heap-profile/enable"

curl --fail --show-error \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$BASE_URL/api/v1/admin/ops/runtime/heap-profile/download?instance_id=$INSTANCE_ID" \
  --output heap.pprof

curl --fail --silent --show-error \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  --data "{\"instance_id\":\"$INSTANCE_ID\"}" \
  "$BASE_URL/api/v1/admin/ops/runtime/heap-profile/disable"
```

多副本部署需通过会话粘滞或目标副本的管理员入口，使这些 profile 操作到达同一实例。服务端会逐次校验实例 ID。下载后可在本地使用 `go tool pprof heap.pprof` 分析，也可传入匹配版本的服务端可执行文件，以获取符号和源码信息。
