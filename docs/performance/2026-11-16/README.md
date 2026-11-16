# Sub2API 全项目性能优化 / Project performance optimization

基线：`e6de1c8eb`。本次涵盖日汇总、后台任务、调度缓存、模型目录、协议转发、监控查询、按需页面及服务端异步 Excel。调度准入、授权隔离、计费、审核判定和断连语义沿用原规则。全部验证使用合成数据、隔离 PostgreSQL / Redis 或本地模拟服务。

Baseline: `e6de1c8eb`. This change covers daily rollups, background admission, scheduling and catalog caches, streaming conversion, monitoring reads, lazy UI and server-side Excel exports. Existing admission, authorization isolation, billing, moderation and disconnect semantics remain in place. Validation uses synthetic data and isolated/local services.

## 实施内容 / Changes

1. 分组汇总拆为短事务准备、事务外构建、版本核验发布。用户与 API Key 使用新增日桶和有界原始尾段；水位未就绪时返回 `USAGE_STATS_INITIALIZING`，余额和额度继续可用。只读统计缓存 10 秒，最多 4,096 条 / 16 MiB。
2. 归因任务默认集群并发 8 个账号，每账号仍有三个并行探针，取消保留执行租约直到探针退出。历史清理局部化并限时轮转。未变化的审核配置复用编译结果。订阅提醒改为窄查询、持久 outbox 和集群两封并发发送，SMTP 使用 45 秒预算。
3. Redis 每波八条 128-key MGET；同版本加载合并并支持独立取消。影子账号健康检查只取必要投影。目录投影、Composite 索引及编码表示共用 15 秒 / 64 MiB / 512 条缓存，保留真实来源授权检查和失效栅栏。
4. Anthropic 四条缓冲路径使用增量累加器；WS 比较直接跳过大 input，按正文版本复用结果；标准 SSE delta 在完整校验后走无分配提取。原始转发、终态及用量逻辑保留。
5. 监控概览跳过新版页面不使用的旧归因列表，并缓存非用户化汇总 10 秒；权限与用户倍率仍每次处理。V1 使用批量时间窗摘要，分组表只取当前页。用户浮层单实例，账号重型弹窗首次打开才加载。
6. Excel 改为后台持久任务和直接下载。保留 37 列及精度，按 1,000 行游标读取，只加载四类关联名称；ZIP/XML 流式输出，不持有整个工作簿。浏览器不再加载 XLSX 依赖或累计整份 Blob。

The individual reports explain the implementations, fixtures and commands:

- [Database and statistics / 数据库与统计](stats.md)
- [Background tasks / 后台任务](background-tasks.md)
- [Scheduling and catalogs / 调度与目录](scheduler-catalog.md)
- [Monitoring and UI / 监控与页面](monitor-ui.md)
- [Process measurements / 进程测量](process-measurements.md)
- [Raw records / 原始记录](raw/)

## 配置与部署 / Configuration and rollout

归因设置新增 `max_concurrent_jobs`，默认 8，允许 1–64；旧客户端省略时保留当前值。其他缓存和统计无需新开关。新增导出配置：

```yaml
usage_export:
  directory: /app/data/usage-exports
  max_rows: 200000
```

环境变量为 `USAGE_EXPORT_DIRECTORY`、`USAGE_EXPORT_MAX_ROWS`。目录必须私有、可写且持久；多个实例的执行和下载节点必须挂载同一共享卷。容器重建不能丢弃该目录。单文件最多 256 MiB，总预留配额 2 GiB，磁盘最低空闲 1 GiB。文件保留 24 小时。

每实例一个导出 worker，集群两个；每用户一个活动任务，等待队列最多 20。任务运行上限 30 分钟、排队一小时，租约 60 秒、心跳 10 秒，失联最多重试一次。所有尝试的文件预留持久保存，失联和删除失败时继续占用配额，成功删除才释放。已结束任务可从用量页重新下载或删除。

下载期间同一文件持有独立短租约，每 10 秒续期，最长 30 分钟；取消或续期失败会关闭文件。下载中的删除请求进入“删除中”，关闭下载后由后台清理，文件与配额在此之前继续保留。

Use a private persistent shared directory on every exporting/downloading replica. The defaults are one worker per replica, two cluster-wide, one active job per user, twenty queued jobs, 200,000 rows, 256 MiB per file, 2 GiB reserved storage, and 1 GiB minimum free disk. Files expire after 24 hours. Jobs have a 30-minute execution / one-hour queue deadline, 60-second leases with ten-second heartbeats, and one recovery attempt. Lost artifacts retain their reservations until removed.

Each file permits one active download lease, renewed every ten seconds with a 30-minute download deadline. Cancellation or failed renewal closes the descriptor. Deleting an actively downloaded file marks it for cleanup; its bytes remain reserved until the download closes and cleanup removes it.

导出接口使用 `usage.read`，任务仅创建身份可见。受限管理 Key 不导出用户、Key、账户或分组名称。创建、领取及下载会复核当前权限；JWT 密码变更、Key 轮换或权限撤销会使对应旧身份失效。下载票据 60 秒、一次性使用，应用日志脱敏票据路径；反向代理应对 `/api/v1/usage-exports/download/` 同样隐藏路径中的票据。

Exports retain `usage.read` and creator isolation. Restricted administrative keys get the existing redacted label projection. Permission checks occur at creation, claiming and download. Password/key/policy changes invalidate the corresponding frozen identity. Download tickets expire after 60 seconds and are consumed once; application access logs redact them. Apply the same redaction to reverse-proxy logs.

## 迁移与回退 / Migrations and rollback

向前追加迁移：

- `290_usage_rollup_fenced_builds.sql`
- `291_usage_subject_daily_rollups.sql`
- `295_attribution_execution_capacity_notx.sql`
- `296_subscription_reminder_outbox.sql`
- `297_subscription_reminder_scan_notx.sql`
- `303_usage_export_jobs.sql`
- `304_usage_export_keyset_indexes_notx.sql`

`*_notx.sql` 使用现有迁移器的非事务执行与并发索引恢复机制。升级前保留应用和数据库备份，并准备共享目录。启动后历史统计按日期后台回填；未达到可用水位时页面显示“统计更新中”，不在请求中扫描全部历史。原始数据已清理的历史不会补造，汇总继续跟随保留期。

All migrations are additive. The existing runner handles nontransactional concurrent indexes and retries. Back up the application/database and provision the shared directory before rollout. Historical summaries rebuild in the background; unready statistics return an explicit initialization response rather than a full-history request-time scan. Retained raw-history boundaries remain authoritative.

回退应停止新版 worker 后恢复匹配的应用与数据库备份，并保留/回收对应共享目录文件。不要让旧版任务领取器与新版租约、汇总构建同时运行。未执行自动部署，未改变生产配置。

For rollback, stop new workers and restore matching application/database backups; retain or clean the corresponding export artifacts. Avoid mixed old/new queue and rollup builders during transition. This task performs no automatic deployment or production configuration changes.

## 验证口径 / Measurement scope

Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，并发设置与数据在各对照组内固定。每个基准三轮。Windows 与 Linux 的原始记录分别标注平台，同一对照不混用平台数据。目录缓存与索引的热查询数据单独注明，不包含首次构建；累计分配 B/op 与存活堆、峰值 RSS 分开记录。

Measurements use Go 1.27.3 with the existing experiments and fixed per-comparison concurrency/data. Three runs are retained. Platform, warm-cache assumptions, allocations, retained heap and peak RSS are reported separately. Synthetic operation timings and p95 are not presented as production endpoint latency.

已有扩展回归失败在基线复现，清单及原始对照见调度报告。最终集成验证、构建结果及未执行项目见 `verification.md`。远端 CI 状态在推送后核实记录。
