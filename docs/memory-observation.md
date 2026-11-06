# Runtime memory observation

[简体中文](memory-observation.zh-CN.md)

The administrator Ops dashboard includes a Memory observation card for each Sub2API process. It combines Go runtime memory, process RSS, scheduling cache estimates, usage-worker scheduling, logical request-body sizes, and SSE buffer capacities. Samples and counters contain numeric diagnostics and fixed cache names; they do not store request bodies, headers, credentials, prompts, or cache keys.

## Deployment and switches

Memory observation uses the existing Ops switches:

```yaml
ops:
  enabled: true
```

`ops.enabled` defaults to `true` and is also available as `OPS_ENABLED`. The runtime setting `ops_monitoring_enabled` must also be enabled; an unset setting uses the existing enabled default. The sampler reads the published Ops setting in memory. Disabling Ops stops new samples and blocks these administrator endpoints. Heap-profile download is separately **off by default**.

Every application process collects its own sample immediately at startup and then every **10 seconds**. This interval is fixed and independent of `ops_metrics_interval_seconds`; the existing metrics collector's leader lock does not limit memory sampling to one replica. No new database tables or database writes are used for this history.

Each process generates one startup UUID, shared with the Ops metrics collector. A restart creates a new instance ID. Hosts, pod names, account IDs, and request IDs are not part of the identifier. In a load-balanced deployment, `current_instance_id` identifies the process handling the current API request.

## History and availability

| Limit or state | Meaning |
| --- | --- |
| Local history | A fixed ring of 360 samples, approximately one hour at 10-second intervals. |
| Redis history | At most 360 samples per registered instance, with a 70-minute TTL refreshed on each sample. |
| Redis instance directory | At most 128 instances. Existing entries remain until expiration; a full directory declines new registrations while those processes continue their local sampling. |
| `truncated` | A directory-capacity rejection occurred within the past 70 minutes. Some processes may only have local history. The serving process is still included in the response. |
| `stale` | An instance's latest sample is more than 30 seconds old, or it has no sample yet. Stopped instances remain visible until their Redis records expire. |
| `redis_available: false` | The cross-instance history store could not be read or is not configured. The serving process still returns its local history. |
| Unavailable history | The selected instance has no retrievable samples, for example after expiration or a Redis outage. |
| `rss_available: false` | RSS could not be read on that sample. Display it as unavailable rather than treating the numeric placeholder as zero resident memory. |

Redis uses the `ops:runtime:memory:` prefix. All replicas must use the same configured Redis store to share their histories. Selecting an older instance never joins its counters with a new process's history. Local history is lost on process exit; Redis keeps its last samples for the configured fixed retention period.

## Reading the metrics

| Fields | Interpretation |
| --- | --- |
| `rss_bytes` | Resident bytes for the process, read from the operating system. |
| `sys_bytes` | Total memory obtained by the Go runtime, from `/memory/classes/total:bytes`. |
| `heap_alloc_bytes`, `heap_objects` | Bytes and count of live or not-yet-collected heap objects. |
| `heap_inuse_bytes` | Heap object bytes plus unused space in in-use heap spans. |
| `heap_idle_bytes`, `heap_released_bytes` | Idle heap span bytes, including released memory, and the released portion respectively. |
| `stack_inuse_bytes`, `goroutines` | Runtime heap-stack bytes and current goroutine count. |
| `alloc_bytes_total`, `free_bytes_total` | Cumulative allocated and freed heap bytes since process startup. Allocation rate uses the delta between adjacent samples divided by elapsed seconds; the first point has no rate. |
| `gc_cycles`, `gc_pause_seconds` | Cumulative GC cycle count and a fixed-size aggregation of the runtime's cumulative GC pause histogram. |

RSS and Go runtime values describe different memory accounting domains. Read their trends together with workload and cache activity. The sampler reads `runtime/metrics` without forcing a garbage collection.

### Request bodies and SSE

`activity.active_bodies` and `active_body_bytes` count logical inbound bodies in requests currently being handled. A request starts contributing when bytes are read; its size is the largest observed wire-byte count or decoded/normalized body size. Prereads and repeated access to the same body reuse that count. The scope ends when the enclosing request middleware returns. It stores numbers only and forwards the original reader's return values and close behavior. Existing body-size limits remain in force.

This is a logical body-size gauge, not a sum of every allocation or copied representation. The `active_large_bodies` and `active_large_body_bytes` subset uses a **1 MiB** threshold. `body_size_bytes` records one observed size when each nonempty request scope finishes.

For the instrumented SSE relay, `active_sse` counts live streams. `active_sse_buffers` counts nonempty raw/scratch backing buffers, and `active_sse_buffer_bytes` sums their actual **capacities**, including spare capacity. Buffer ownership follows each frame through the reader/writer handoff; growing, discarding, and releasing buffers adjust the gauges. Stream cleanup releases the tracked capacities. `sse_buffer_max_bytes` and `sse_buffer_size_bytes` are a process-lifetime maximum and histogram of observed raw-plus-scratch capacities.

### Scheduler cache

The decoded scheduler account pool and bucket indexes share a default **64 MiB soft budget per process**:

```yaml
gateway:
  scheduling:
    snapshot_cache_max_bytes: 67108864
```

The corresponding environment variable is `GATEWAY_SCHEDULING_SNAPSHOT_CACHE_MAX_BYTES`. Restart the process after changing deployment configuration.

The card reports `scheduler_accounts`, `scheduler_buckets`, and `scheduler_cache_total`. The total row includes both components and carries the shared `max_bytes` budget; do not add it to the component rows again. `estimated_bytes` is an estimate of cache-owned objects and indexes, while RSS and heap measurements cover the wider process. A request already using an evicted immutable snapshot can retain that snapshot until the request finishes.

`entries`, `hits`, `misses`, and `evictions` describe the local cache. The reported `ttl_seconds` is the idle-retention threshold, currently 100 seconds; cache pressure can evict entries sooner.

### Usage worker queue and histograms

`usage_queue` reuses the worker pool's statistics: configured queue `capacity`, `waiting`, `running`, `submitted`, `completed`, `failed`, `dropped`, and `sync_fallback`. These describe scheduling and closure execution. Separate `settlement_succeeded` and `settlement_failed` counters record each ordinary usage billing operation's returned success/error exactly once, including synchronous fallbacks. They are cumulative per-process call outcomes. An idempotent duplicate that returns success also increments the success counter; Engine-owned usage no-ops are excluded. Detailed debit results remain in the existing billing records and error handling.

`wait_ms` covers enqueued work from submission to execution start. `execution_ms` covers execution duration, including synchronous overflow fallbacks. Both use upper bounds `[1, 10, 100, 1000, 10000, 60000]` milliseconds and a final overflow bucket. Size histograms use `[1024, 16384, 65536, 262144, 1048576, 8388608]` bytes plus overflow. Counts are cumulative, disjoint buckets, not cumulative bucket totals.

## Administrator API

All paths below are under `/api/v1/admin/ops`. They use the existing administrator authentication middleware. An administrator JWT is sent as `Authorization: Bearer <token>`; administrator API keys follow the application's normal key permissions. Ordinary gateway keys and non-admin users cannot access these endpoints.

| Method and path | Request | Response |
| --- | --- | --- |
| `GET /runtime/memory` | Optional query `instance_id`; omission selects the serving process. | Standard JSON envelope containing the view described below. |
| `POST /runtime/heap-profile/enable` | JSON `{"instance_id":"<current UUID>"}` | Standard JSON envelope containing `{enabled, expires_at, in_progress}`. |
| `POST /runtime/heap-profile/disable` | Same JSON body. | Same profile-state object. |
| `GET /runtime/heap-profile/download` | Required query `instance_id=<current UUID>`. | Binary gzip-encoded Go heap profile, `application/octet-stream`, attachment filename, and `Cache-Control: no-store`. |

The memory view contains `current_instance_id`, `selected_instance_id`, `sample_interval_seconds`, `history_limit`, `stale_after_seconds`, `redis_available`, `truncated`, `instances`, `history`, and `profile`. Each instance has `instance_id`, `started_at`, `last_seen_at`, and `stale`. Each history sample has `at`, memory/runtime fields, `activity`, `caches`, and `usage_queue`. `profile` always belongs to the serving process, even when history is selected from another instance.

Invalid history IDs return 400. Profile requests for a different process return 409; disabled/expired profile download returns 403; a concurrent download returns 409. Disabled Ops returns 404, and a missing runtime-memory service returns 503. Authentication failures use the existing 401/403 responses.

## Temporary heap-profile download

Use the dashboard to select the current process, enable heap-profile download, download, and disable it when finished. Enabling grants a **30-minute** local permission; re-enabling renews that period. The permission expires automatically and is reset on restart. Disabling prevents new downloads while an already-started transfer can finish.

There is at most **one in-progress heap download per process**. The handler writes the runtime's existing sampled heap profile directly to the HTTP response. It creates no server-side profile file, performs no forced GC, and does not change `runtime.MemProfileRate`. The profile contains runtime allocation stacks and sampled object statistics.

Example using an existing administrator session token:

```bash
BASE_URL='https://your-sub2api.example'
# Set ADMIN_TOKEN to an existing administrator JWT in your local environment.
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

For multiple replicas, use routing affinity or the intended replica's administrator ingress so all three profile operations reach that instance. The server validates the instance ID on every operation. Analyze the downloaded file locally with `go tool pprof heap.pprof`, optionally supplying the matching server executable for symbol/source information.
