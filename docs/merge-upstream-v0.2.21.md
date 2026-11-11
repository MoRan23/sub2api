# Upstream v0.2.21 and prompt-audit enforcement / 合并与验证记录

## Revision and scope / 版本与范围

- Local parent: `d2fc83137e352f69062e59ad8344b38bda7b4d21`.
- Upstream parent: `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484`.
- Ordinary merge: `1aefe903e`; 83 incoming commits, 202 upstream files and 11 textual conflicts.
- Compatibility follow-up: `05b596ec7` (TTL parser constant paths and the missing settings repository method in a test fixture).
- Prompt-audit enforcement: `9f59182a2`, committed separately from the upstream merge. The six pre-existing untracked investigation files remain untouched.
- Large-image observation follow-up: `c42a3e842`, reusing per-call metadata views without changing forwarding bytes, full JSON validation or image accounting.

本轮只修改本仓库并使用模拟上游、隔离数据库验证；未修改 Engine、生产配置或生产数据，未调用真实模型，未部署。

## Compatibility decisions / 兼容处理

- Preserve Engine forwarding and complete-delivery billing gates; disconnected clients do not incur recovered late-usage charges. Shared account candidates, frame ownership and detached usage records remain in place.
- Page-context normalization occurs inside each non-Engine forwarding attempt, derived from the original request. Audit evidence can include page context, while latest-user intent excludes page contents. UTF-8 validation and upstream delayed Messages effort validation are integrated.
- Preserve current-turn WS replay, full-history checks, interrupt handling and authorization isolation. Metadata remains observable without counting as first output. Search-capability changes retain the upstream reconnection behavior.
- Preserve native image routing and parameters. Image turn-ID requests use the upstream heartbeat change without replacing upstream error statuses.
- Carry 30-minute and 12-hour cache-write usage through protocol parsing, records and DTOs; detach 12-hour prices with the existing billing snapshot. Accept TTL buckets only when both nonnegative integer buckets sum to the reported aggregate. Engine aggregate-only usage is not assigned a guessed TTL.
- Keep Daybreak, OAuth generations, attribution, official OpenAI catalog projection, Cyber descriptors, `unified_exec`, gift balances and permanent quota subscriptions. Cyber `max` remains supported; no independent server-side `ultra` tier is added.
- Keep the removed legacy recharge-tier UI removed; accept upstream multiplier formatting and account/API Key UI refresh fixes. No unrelated dependency upgrades.
- Use upstream adaptive body budget: one quarter of effective memory, minimum 1 GiB; explicit values, including zero to disable, take precedence. Production memory parameters are unchanged.
- Regenerate Ent and Wire from merged sources, preserving local providers and their shutdown lifecycle. The new notification worker participates in the existing prompt-audit service lifecycle.

## Enforcement / 处罚功能

See the [Chinese guide](prompt-audit-enforcement.md) and [English guide](prompt-audit-enforcement.en.md) for configuration and migration semantics.

旧关键词全部迁入破甲类别。默认开启警告邮件和自动封禁，最近 24 小时累计 10 次触发；每次被拦截请求最多计一次，邮件默认合并 5 分钟。其他关键词保留匹配内容提示，不参与新处罚。破甲只返回警告与已提交次数；组合仅公开规则组名与次数。管理员仍可查看证据。

事件、计数、用户状态和邮件任务同事务提交。用户行先于处置状态锁定；重复请求与旧周期回调不重复处罚。状态写入沿用鉴权缓存失效和 MCP 授权撤销。快捷解封依据当前封禁归属，管理员显式状态写入撤销旧归属，启用时开启新周期。

已有 WS 连接在下一轮推理前复核用户和 Key 的有效性，覆盖原生连接池、透传和 Engine 桥接。复用该次查询刷新允许更新的计费分组；控制帧、当前轮安全重试及原有路由快照规则保持。管理员批量停用也撤销审计封禁归属，包括重复提交停用；邮件发送前核对最新邮箱，旧收件地址失效时重新领取。

## Database rollout / 数据库上线

Keep upstream migrations under their full filenames, including `249_add_usage_logs_api_key_cost_index_notx.sql` and `250_openai_cache_write_ttl.sql`, alongside existing local migrations. Applied migrations are not rewritten. The concurrent covering index keeps the non-transactional runner and retry behavior.

`281_prompt_audit_enforcement.sql` adds compact state, hit-ledger and notification tables. It does not rewrite historical audit events or retroactively punish users. Migration validation includes a fresh database, an upgrade from the previous complete migration set and repeated startup. Old wallet balances, permanent `NULL` expirations, quota batches, Daybreak settings and usage history are retained.

上线前备份数据库、应用和审计配置，排空旧进程后统一切换，避免新旧版本同时修改规则与用户状态。回退使用对应应用及配置备份；新增表可以保留，旧版不会继续新处罚任务。已停用用户不会因回退自动启用。涉及恢复数据库时使用一致备份，不删除迁移账本。

## Validation / 验证

Environment: Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`; Windows amd64 unit/build runs and WSL Linux isolated PostgreSQL 18.1 / Redis 8.4 integration runs.

| Area | Executed checks |
| --- | --- |
| Backend generation/build | Ent generation, Wire regeneration and backend build |
| Request/protocol merge | OpenAI/apicompat/wsrelay packages; targeted HTTP, Messages, Images, Engine and WS replay/interrupt/search tests and concurrent-path race checks |
| Settings/authentication | Middleware package, token-refresh tests, body-budget configuration and sysutil Linux checks |
| Audit configuration/service | Full securityaudit and admin routes unit tests; classification, empty-list compatibility, priority, evidence protection and targeted race tests |
| Enforcement persistence | Isolated PostgreSQL: threshold 9/10, cross-kind count, concurrent dedupe, administrator exemption, rollback, auth-cache outbox, MCP revocation, ownership/unban, epoch fencing and evidence retention |
| Notifications | Fixed-window merge, latest policy summary, leasing, bounded retries, concurrent SMTP-period additions, nonblocking exhausted-claim cleanup, recipient changes after claim; simulated sender/template tests and race checks |
| User administration | Real repository Update and batch disable: repeated disabled revokes ownership; active resets cycle/cancels old pending mail; unrelated updates preserve state; caller transaction rollback, ordered user locks and MCP revocation |
| Existing WS connections | Native, passthrough and Engine admission tests; disabled user/Key and lookup failures send no new turn upstream; valid turns reuse the lookup for pricing; targeted race and broad WebSocket regressions |
| Migration/billing integration | Fresh install, previous migration set to current, two repeat startups; TTL usage and channel prices round-trip, permanent quota lifecycle, wallet and Daybreak preservation, covering-index validity |
| Frontend | 102 focused audit tests, affected upstream UI tests, TypeScript, targeted ESLint and production build |

The original middleware FastPolicy fixture panic was reproduced on the baseline archive, then repaired by implementing its missing `GetMultiple` behavior. The full middleware suite passes afterward. A Windows symlink-privilege-dependent cgroup test was run successfully in Linux. The whole historical backend suite was not claimed as executed; unrelated known baseline fixtures are outside these targeted runs.

未执行真实 SMTP 投递和生产模型压测。仓库 Actions 按本任务既有设置关闭，未执行远端 CI；本地和隔离数据库结果单独列出，不将跳过的检查计为通过。

## Performance / 性能

The [benchmark report](validation/v0.2.21-benchmarks.md) includes three-round raw results, medians, controlled alternating reruns and allocation-profile attribution. It compares identical synthetic fixtures against `d2fc83137`, without running duplicate audit engines on production requests. Shared-cache retained heap is measured in its dedicated fixture. Process-wide production RSS and production p95 were not sampled in this change.

未命中的审核请求不新增处罚数据库查询或邮件任务。匹配器保持预编译，正文解析继续共享；新增处罚标量不会持有额外请求正文。基准复测记录包含初测波动和较慢样本，完整保留，不以不同批次的最佳值拼接收益。

最终大图观察优化保留重复键、转义键、显式 `null` 和多图计数语义，使用旧实现差分验证。32 MiB WS 三轮中位数相对基线为 +3.66%，每操作仍为 111 次分配；四个小帧场景中位数下降约 2%–6%，分配次数回到基线。缓存长次数定位复测未重现此前耗时上升，完整数据及波动见报告。最终相关转发 race 和后端全包构建通过。

Final large-image differential tests retain duplicate/escaped keys, explicit null and multi-image semantics. The 32 MiB WS median is +3.66% against baseline with 111 allocations per operation; small-frame medians improve by approximately 2%–6% with baseline allocation counts. Longer cache profiling runs do not reproduce the earlier timing increase. All measurements, including earlier slower samples, remain in the report.
