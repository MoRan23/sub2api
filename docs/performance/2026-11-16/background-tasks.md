# 后台任务 / Background tasks

## 行为与配置 / Behavior and configuration

- 归因配置增加 `max_concurrent_jobs`，默认 8、范围 1–64，表示集群同时执行的账号任务数。每账号仍并行三个探针，每实例最多持有 64 个执行槽。取消先停止实际请求，全部请求退出后才释放数据库执行租约；降低上限只影响新领取。旧管理客户端省略该字段时保留当前值。
- Attribution adds `max_concurrent_jobs` (default 8, range 1–64), shared across replicas. Each account still uses three concurrent probes; each replica has 64 local admission slots. Cancellation retains and renews execution capacity until physical probes drain. Lowering the limit affects new claims only; omitted fields from older clients retain the current setting.

- 完成、取消和入队仅清理相关账号；每分钟轮转维护在 2 秒内按 50 个账号独立提交，每批最多删除 1,000 条。保留最近 100 条、215 分钟内有效检测、API Key 窗口前锚点和仍持有执行租约的终态任务。
- Completion, cancellation and enqueue prune only affected accounts. Minute maintenance uses independent batches of 50 accounts, at most 1,000 deletions each, within two seconds. Retention protects the latest 100 jobs, valid results in 215 minutes, older API-key verdict anchors and terminal jobs with live execution leases.

- 提示词审计仍每 5 秒读取配置。原始 JSON 未变时共享已编译不可变快照，风险开关仅更新门控；读取或解密失败继续按既有规则恢复。保存和加载使用相同发布锁。
- Prompt audit keeps its five-second refresh. Unchanged JSON reuses compiled immutable rules; risk-gate changes update only the gate. Existing read/decryption recovery behavior remains. Save and reload share publication ordering.

- 订阅提醒数据库只筛选有限期订阅的 7／3／1 天区间，使用到期时间＋ID 的 200 条游标页，批量写入专用 outbox。集群最多两封并发发送，单封预算 45 秒、租约两分钟、最多五次尝试。发送前检查收件人、到期日、撤销与有效期；暂停提醒不会消耗重试次数。SMTP 拨号、TLS 和读写继承取消及截止时间。
- Subscription reminders scan only finite subscriptions in the existing 7/3/1-day intervals, with expiry/ID cursors and 200-row projection pages. A durable outbox permits two concurrent deliveries cluster-wide, with a 45-second call budget, two-minute leases and five attempts. Recipient, renewal, revocation and expiry are rechecked before sending. Ownership is atomically checked and renewed before SMTP, so a stale claimant cannot send after lease recovery. Pausing reminders does not consume retries. SMTP dialing, TLS and I/O honor context cancellation/deadlines.

- 提醒沿用通知模板及已有发送幂等标记。扫描、并发领取及正常重试去重；SMTP 接收后、发送标记落库前进程崩溃的边界保持既有至少一次投递语义。
- Templates and existing notification delivery markers remain authoritative. Scanning, claiming and normal retries deduplicate. A crash after SMTP acceptance but before durable acknowledgment retains the existing at-least-once delivery boundary.

- 处罚历史每小时在 5 秒预算内清理，每个表每批最多 1,000 条、最多 20 批，保持 31 天与引用保护，并使用跳过已锁定行的删除。
- Hourly enforcement cleanup uses a five-second budget, up to 20 batches of 1,000 rows per table, retaining the 31-day/reference protections and skipping locked rows.

## 迁移 / Migrations

- `295_attribution_execution_capacity_notx.sql`: attribution admission indexes, built concurrently.
- `296_subscription_reminder_outbox.sql`: durable reminder outbox, no credential or business-body columns.
- `297_subscription_reminder_scan_notx.sql`: partial expiry/ID index, built concurrently.

## 三轮配置刷新比较 / Three-run refresh comparison

Baseline: `e6de1c8eb`; Linux amd64, Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`; 10,000 synthetic keyword rules, unchanged persisted JSON.

| Version | Time per refresh, three runs | B/op | allocs/op |
| --- | --- | --- | --- |
| Baseline | 7.467 / 7.440 / 8.068 ms | 8,902,867 / 8,902,925 / 8,902,860 | 48,485 |
| Optimized | 615.4 / 520.3 / 484.9 ns | 1,392 | 4 |

分配字节减少约 99.984%，编译及全量配置复制从未变化的刷新路径移除。此项是配置刷新微基准；并发编译会影响耗时波动，原始三轮结果均保留。

Allocated bytes fall by about 99.984%; unchanged refreshes no longer compile or clone the complete configuration. These are configuration-refresh microbenchmarks; concurrent compilation can affect timing variation. All three raw runs are retained.

## 复现 / Reproduction

定向后端单元与竞态测试通过；归因 PostgreSQL 的并发、取消租约、局部维护及保护记录测试通过。提醒 PostgreSQL 测试使用独立 schema、8 个连接，覆盖四个并发领取者共享两个名额、租约恢复、旧执行者退出、续期／撤销／收件人复核、停用重试及最终失败保留，全部通过。前端两套归因测试共 94 条及定向 ESLint 通过。原始结果位于 `raw/background-*.log`。

Targeted backend unit/race checks, attribution PostgreSQL integration checks, reminder PostgreSQL checks, 94 frontend tests and targeted ESLint passed. Reminder tests use a separate schema and eight connections, covering concurrent admission, lease recovery and stale-owner suppression, renewal/revocation/recipient checks, paused retries and retained terminal failures. Raw results are in `raw/background-*.log`.

```bash
cd backend
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go test -tags unit -p 2 ./internal/securityaudit ./internal/service \
  -run 'Test(PromptConfig|PromptCleanup|ConfigManager|Attribution|ModelAttribution|SubscriptionExpiry|SMTP)' -count=1
go test -race -p 2 ./internal/securityaudit ./internal/service \
  -run 'Test(PromptConfigReload|PromptCleanup|AttributionParallel|AttributionCancelReleases|SMTPContext)' -count=1
go test -p 2 ./internal/securityaudit -run '^$' \
  -bench BenchmarkPromptConfigUnchangedReload -benchmem -benchtime=1s -count=3
go test -tags integration -p 2 ./internal/repository \
  -run 'TestAttribution(Capacity|CancelledLease|LocalPrune|QueueLeases)|TestChannelMonitorV2OverviewAttributionPruning' -count=1
# Set SUBSCRIPTION_REMINDER_TEST_DSN to an isolated PostgreSQL test database.
# The test creates and drops its own schema; no SMTP server is contacted.
go test -tags integration -p 2 ./internal/service -run TestSubscriptionReminderOutboxPostgres -count=1
cd ../frontend
pnpm exec vitest run src/components/admin/account/__tests__/modelAttribution.spec.ts src/api/admin/__tests__/modelAttribution.spec.ts
pnpm exec eslint src/api/admin/modelAttribution.ts src/views/admin/ModelAttributionView.vue src/i18n/locales/modelAttribution.ts src/components/admin/account/__tests__/modelAttribution.spec.ts
```

基线比较使用同一份 `prompt_config_reload_reuse_test.go` 放入基线工作树，运行 `-run '^$'` 仅执行基准。

For the baseline, copy the same `prompt_config_reload_reuse_test.go` into the baseline worktree and run only the benchmark with `-run '^$'`.
