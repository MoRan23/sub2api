# Upstream v0.2.22 merge / 上游合并记录

## Revisions and scope / 基线与范围

- Local parent: `08a02ad7f583a60b539c916213ab1b8923a4ed14`.
- Upstream parent: `15871f904056f548dd874a1d069ffc6cdf777b78` (v0.2.22).
- Merge base: `530c5e3a4`; 186 incoming commits (107 non-merge), 339 upstream files, 55 textual conflict files.
- A normal two-parent merge preserves both histories. Work was isolated from the primary checkout; its existing detector files and investigation artifacts were preserved.

本次仅合并 Sub2API，保留 Engine、Daybreak、授权隔离、官方目录、Cyber 描述符、双钱包、永久订阅、审核处罚及近期 CPU/内存优化。未修改 Engine、生产配置或生产数据，未调用真实模型，未部署。

## Compatibility / 兼容选择

1. **Requests and retries / 请求与重试** — Preserve request-local preparation and raw-body reuse across 429 retries. Each attempt still applies account identity, final mapped model, Daybreak, timezone and integrity policy. Untrusted page/context items share the existing extraction path, without becoming the latest user's operation intent. Tool-schema changes retain copy-on-write ownership and escaped-key semantics. Credential diagnostics build bounded strings only on the relevant failure; no lazy holder retains the original body or Gin context.
2. **Streaming / 流式转发** — Integrate keepalive, first-output failover, native Anthropic and protocol-conversion fixes. Messages uses the existing bounded pre-output staging (64 KiB memory, 8 MiB maximum), not an unbounded string builder. Error payloads own their bytes before a scanner reuses its buffer. Engine complete-delivery billing and no late disconnected-client charges remain unchanged.
3. **Catalog and scheduling / 目录与调度** — Keep immutable shared candidates, ten-second freshness validation, bounded MGET pipelines and cache budgets. Last-used updates use the upstream short-lived timestamp cache with a generation guard against late observations. Model filtering is default-off for generic passthrough; when enabled it filters public source IDs without substituting target descriptors. Engine's synchronized whitelist always applies, including a valid empty list. Shared account capability projection and catalog-cache invalidation are preserved.
4. **Fast tiers / 加速档位** — Preserve account-over-group priority, mutually exclusive forced switches, raw upstream capability evidence, unknown-evidence behavior, unsupported-Ultrafast fallback to priority and Guardian's final override. Pricing-only Ultrafast family recognition is separate from routing capability; GPT-6.1 Sol uses its sixfold price factor. Authentication cache version remains **30**.
5. **Billing / 计费** — Introduce upstream lane batching while retaining independent small command snapshots and each command's quota-event IDs. Batch double-wallet deductions match single-call normal balance → gift balance → normal-balance debt behavior. Failed/panicked batches fall back with their original dedup keys. Grok's precise safety rejection retains atomic billing and its unmultiplied fixed fee. Explicit zero token prices remain zero; Vertex region factors are frozen as scalars and do not multiply that fixed fee.
6. **Concurrency / 并发** — Quota-event locks use stable ID order across batch, ordinary, Grok and Engine paths; deductions still follow the original admitted snapshot order. Shutdown seals request admission, cancels remaining transports after the grace period, waits for handlers, then drains usage workers, billing lanes and their dependencies before closing storage. Inline mandatory tasks and atomic/image operations participate in this barrier.
7. **Statistics / 统计** — Account-filtered user cost remains `actual_cost`; account cost uses the existing fixed-fee-aware formula. Preserve the local nonblocking staged daily rollup builder and revision/token publication fence. Exact retention instants, partial boundary days and civil-day DST boundaries are handled consistently.
8. **Build and UI / 构建与页面** — Regenerate Ent and Wire from merged sources. Use Go **1.27.4** in local verification, Docker and CI definitions, with `GOEXPERIMENT=greenteagc,jsonv2`. Integrate account quota display, Bedrock channel flags, wildcard-input validation and upstream management fixes without unrelated frontend dependency upgrades.

## Migration and rollback / 升级与回退

Upstream `251_group_force_openai_ultrafast.sql` and `251_group_usage_rollup_nonblocking_build.sql` coexist with local migrations under their full filenames. Existing applied files are unchanged. New `305_merge_v022_usage_rollup_compat.sql` installs the final group/subject trigger functions for both fresh-install and upgrade ordering.

305 adds the subject rollup retention floor and preserves exact cutoff timestamps. Source writes use a compatible key-share barrier; invalidation clears the active build token once, preventing repeated per-row state updates during large deletes. Publication checks the revision and token before replacing a bucket.

Because migrations commit per file, an older instance could finish a build between upstream 251 and final 305. The final migration cancels both builders and invalidates their retained range once, including any build already published in that interval. Rebuilding is performed by the existing bounded background worker, not a source-table scan inside the migration. Statistics may temporarily report that they are updating. Source usage records, balances and subscription entitlements are not rewritten.

上线前备份应用、数据库和配置，先排空旧实例，再统一切换。此次验证覆盖空库、既有迁移序列、重复执行、迁移窗口及并发写入。回退时使用匹配的应用与数据库备份；不要删除迁移账本或手工回写汇总水位。需要恢复历史账本时使用一致备份。

## Verification / 验证

See [validation and performance results](validation/v0.2.22-validation.md) for commands, raw records, baseline failures and measured comparisons. Tests use simulated upstreams and isolated PostgreSQL 18.1 / Redis 8.4. Native Windows Go 1.27.4 compiles Linux integration binaries, which run in WSL; this avoids a Linux toolchain download timeout while keeping the same compiler version.

仓库 Actions 按当前任务约定关闭，远端 CI 未执行；本地验证和已知失败单独列出。
