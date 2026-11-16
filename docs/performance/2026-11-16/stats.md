# 数据库与统计 / Database and statistics

## 行为 / Behavior

- 迁移 `290_usage_rollup_fenced_builds.sql` 将分组日汇总改为准备、构建、发布三阶段。历史扫描发生在状态行事务之外；发布核对版本、令牌、日期及时区。会话级 advisory lock 限制重复构建，失败释放锁时会丢弃连接。
- 迁移 `291_usage_subject_daily_rollups.sql` 新增独立用户 / API Key 日桶。保留当前平台归属、零费用与 Grok 拒绝计费统计口径；耗时使用总和及非空样本数求加权平均。
- 查询使用同一 Repeatable Read 快照读取水位、日桶及最多昨日 / 当天原始尾段。时区变动、无效水位或过旧水位返回 `USAGE_STATS_INITIALIZING`，不退回全历史原始扫描。
- 继续由已有启动回填和定时汇总入口运行，无需新增开关。`/v1/usage` 摘要保持 best effort，余额 / 额度继续返回；用户仪表盘提示更新中，10 秒后重试，最多自动重试 6 次，可手动刷新。
- 统计 L1 缓存 TTL 10 秒，最多 4,096 项 / 16 MiB，增量 LRU、单飞查询，错误与超大结果不驻留。缓存不包含余额和权限。

Daily builds now prepare and publish under short state-row transactions, with history scans outside those transactions. Revision, token, timezone and date checks fence stale results. Historical mutations and retention invalidate both summaries. User and API Key summaries preserve existing accounting semantics and read daily buckets plus a bounded raw tail in one snapshot. Unready history returns an explicit initialization status while wallet and quota access remain available. A bounded ten-second cache merges identical reads and owns its result slices.

## 验证 / Verification

- Windows Go 1.27.3 targeted unit tests passed (`raw/stats-unit-windows.log`).
- Linux Go 1.27.3 targeted race tests passed (`raw/stats-race.log`). Includes publication ordering, stale results, retention paths, initialization without raw fallback, DST calendar boundaries, cache ownership, cancellation, error and size bounds.
- Isolated PostgreSQL 17 integration tests passed (`raw/stats-integration.log`). Includes repeatable migrations, actual billing/zero-fee/refusal counters, current Composite platform attribution, historical deletion rebuilds, late-insert fencing, unchanged-watermark retention invalidation and existing trigger concurrency tests.
- Dashboard initialization tests passed: 2 tests (`raw/stats-frontend.log`).
- Expanding the race check to existing cleanup tests exposed an unsynchronized test-stub counter. It now uses `atomic.Int32`; the original failure is retained in `raw/stats-race-existing-test-stub.log`.
- Three cache-hit benchmark rounds: 369.9 / 490.4 / 389.5 ns/op, 304 B/op, 4 allocs/op (`raw/stats-race-cache-benchmark.log`).

Full-history comparison uses the same integration benchmark file on `e6de1c8eb` and the updated checkout: 1,000,000 usage rows, 10 users, 120 historical days, 100,000 rows for the queried user. Each round executes 20 uncached repository reads; the service cache is excluded. Raw results are in `raw/stats-history-baseline.log` and `raw/stats-history-optimized.log`. The first optimized run exposed repeated SQL string allocation; immutable prepared query strings remove that work (`raw/stats-history-initial.log` preserves that run).

| 冷查询 / Cold read | 第 1 轮 / Run 1 | 第 2 轮 / Run 2 | 第 3 轮 / Run 3 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|
| `e6de1c8eb` | 85.830 ms | 85.550 ms | 96.589 ms | 9,941–9,943 | 170 |
| 新日汇总 / Daily rollups | 4.337 ms | 4.623 ms | 4.378 ms | 12,197–12,215 | 219 |

中位冷查询耗时降低 **94.9%**。为一致快照和统一平台结果集增加约 2.3 KiB / 次冷查询分配；10 秒内相同主体请求复用缓存（304 B / 次）。这组数据专门测已关闭历史日，不包含大量当日尾段。未在此子任务中测进程峰值 RSS / p95；没有将上述微基准当作端到端峰值结果。

Median uncached query time fell **94.9%**. Consistent snapshot handling and the combined platform result add about 2.3 KiB per cold read; repeated reads within ten seconds use the 304-byte cache-hit path. This fixture measures closed historical days. Process peak RSS and end-to-end p95 were not measured in this subtask.

百万行隔离数据的 `EXPLAIN (ANALYZE, BUFFERS)` 验证：目标用户历史原始记录 **100,000 行**，发布后的日桶 **120 行**；原始尾段从统计日零点开始，本次样本为 **0 行**。原始历史聚合执行 **37.918 ms**，访问 **17,359 个共享缓冲页**（命中 10,799、读取 6,560）；日桶加有界尾段执行 **0.676 ms**，访问 **33 个共享缓冲页**（命中 22、读取 11）。计划分别使用 `usage_user_created` 与 `usage_subject_rollup_user_date` 索引。完整计划见 `raw/stats-integration.log`，单次计划执行时间与三轮接口基准分开记录。

并发短锁验证主动阻塞历史 stage 写入，同时提交当前日期 usage；当前写入 **8.340 ms** 完成，未等待历史扫描结束。随后插入历史记录并解除阻塞，发布栅栏拒绝旧结果，重建后保留两笔记录。该测试使用实际 PostgreSQL 事务和独立连接。

The isolated million-row query plan reads 120 published day buckets instead of 100,000 historical user rows, plus an indexed raw tail beginning at the current day's midnight (zero rows in this fixture). Shared buffer accesses fall from 17,359 to 33; individual plan execution times are 37.918 ms and 0.676 ms. A separate concurrency test deliberately holds the historical stage build: a current usage insert still completes in 8.340 ms. A late historical insert then invalidates the stale build, and the rebuilt result retains both records.

## 复现 / Reproduction

From the backend directory, using Go 1.27.3:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go test -tags unit -race -p 2 ./internal/repository ./internal/service \
  -run 'Test(DashboardAggregationRepository|UsageRollup|UsageStatsCache|SubjectDashboard|SubjectRollupTail|UsageCleanup)' -count=1
SUB2API_TEST_POSTGRES_IMAGE=postgres:17-alpine go test -tags integration -p 2 ./internal/repository \
  -run 'Test(Subject|GroupUsageRollupTrigger|UsageRollupHistory)' -v -count=1 -timeout=8m
go test -tags unit -p 2 ./internal/service -run '^$' -bench BenchmarkUsageStatsCacheHit -benchmem -count=3
SUB2API_TEST_POSTGRES_IMAGE=postgres:17-alpine go test -tags integration -p 2 ./internal/repository \
  -run '^$' -bench BenchmarkUserDashboardHistory -benchtime=20x -benchmem -count=3 -timeout=12m
```

Run the final command in the baseline checkout with the same `usage_dashboard_history_benchmark_test.go` fixture copied there. The integration harness starts isolated PostgreSQL / Redis containers. From the frontend directory:

```bash
pnpm exec vitest run src/views/user/__tests__/DashboardView.initializing.spec.ts
```

数据库回退应恢复相匹配的应用和数据库备份；新增表不作为永久账本，汇总继续随原始记录保留范围变化。未连接生产数据库、模型或支付服务。

Rollback uses matching application and database backups. The summaries remain retention-bound statistics, not a permanent ledger. These checks used isolated test data only.
