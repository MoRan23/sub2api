# Upstream v0.2.23 merge / 上游合并记录

## Baseline and scope / 基线与范围

- Local parent: `e47c186b6ff1c1bcbb3635507c93eee0863bd40e`.
- Upstream parent: `2af3f1e15c98c893a89255a037f551046883b8c0` (v0.2.23).
- Previous upstream: `15871f904056f548dd874a1d069ffc6cdf777b78` (v0.2.22).
- Incoming history: 32 commits, including 20 non-merge commits; 90 upstream files and 15 initial text conflicts.
- Ordinary two-parent merge; no history rewrite. Existing detector changes and investigation files in the primary checkout are preserved.

本次保留 Engine、Daybreak、授权隔离、统一官方目录、Cyber、双钱包、永久订阅、Grok 安全拒绝计费，以及此前重试正文、审核、调度缓存和异步计费优化。仅修改 Sub2API；不调用真实模型，不修改生产配置，不部署。

## Model catalog / 模型目录

GPT-6.2 Sol and Astra join the existing complete official fallback catalog as raw JSON resources. Unknown fields, explicit nulls, public-name-first mapping, Cyber overrides and final `shell_type="unified_exec"` are preserved. The templates are decoded once during initialization. Strict aliases and low/medium/high/xhigh/max reasoning handling are added without introducing a server-side Ultra reasoning tier.

保留账号测试的默认 `gpt-5.6-sol`、现有教程默认型号和不设置默认思考等级的行为。OpenCode 预设新增 6.2 Sol 500k、Astra 272k；目录字段来自完整资源，不以旧合成字段覆盖。模型名称识别和价格家族识别分开。

Upstream's Pro-plan entitlement inference is deliberately not adopted. Forced Ultrafast continues to require the selected account's fresh raw upstream catalog evidence; public fallback descriptors and Pro labels are not authorization. Existing account/group precedence, unsupported-Ultrafast Fast fallback and Guardian's final tier removal remain in place.

## Billing and cache usage / 计费与缓存用量

- Add nullable channel `ultrafast_multiplier`. An explicit positive finite value overrides the model default. Missing values retain default pricing; free Fast/Ultrafast only remove the corresponding user surcharge.
- GPT-6.2's default Ultrafast multiplier is 3 before `2027-03-01T08:00:00Z` and 6 afterwards. Existing request `PricingAt` controls delayed Engine confirmation pricing.
- Carry the multiplier through independent billing snapshots and preallocated scalar storage. Mutating/replacing the source card after enqueueing cannot change a queued request's price.
- Preserve authoritative Anthropic cache totals, explicit zero versus absence, native and converted TTL fields, and cache-only delta reconciliation. A positive aggregate remainder without classified TTL uses short-term cache pricing; reported raw usage is not rewritten to invent TTL evidence.
- Keep Anthropic 5m/1h distinct from OpenAI/Engine 30m/12h. Verified Engine TTL data remains supported; absent/inconsistent detail uses existing aggregate handling. Complete-delivery and idempotency conditions remain unchanged, with no late disconnected-client charge.
- Preserve Grok's exact rejection recognition, mandatory atomic settlement and fixed-fee isolation from rate multipliers.

## Streaming and performance / 流式与性能

Integrate deadline-based stream-idle handling and the existing bounded pre-output switch budget. Keepalive does not count as first output. Abandoned attempts close their body and do not leak response headers. Engine cleanup and local retry preparation retain their separate lifecycle.

Raw Chat Completions flush once before waiting for another upstream read, including final buffered output. Grok safety rejection flushes immediately and terminates reading; a trailing DONE/completed event cannot make that attempt successful. DeepSeek placeholder insertion and Ollama reasoning normalization use the upstream single-pass JSON edit rather than repeatedly rewriting a large body.

同一份约 1 MiB／300 轮正文、流帧和重试数据用于合并前后三轮串行基准，原始记录与分配结果见验证报告。此次没有修改 429 窗口、选号规则或 Go 内存参数。

## Migration and rollback / 升级与回退

`252_channel_pricing_ultrafast_multiplier.sql` coexists with the existing local 252 migration by full filename. It adds one nullable `NUMERIC(12,6)` column and a positive-value constraint. Existing cards retain NULL/default semantics. The migration is repeatable and does not overwrite configured multipliers. No Ent schema or Wire dependency graph changed, so generated code was not rewritten.

升级前备份数据库及应用，按现有发布流程切换。新列影响渠道价卡 SQL，所有实例应运行匹配版本；回退使用匹配的应用和数据库备份。迁移不会更改钱包、订阅权益或历史账单。

## Validation / 验证

See [validation report](validation/v0.2.23-validation.md) and [reproduction commands](validation/v0.2.23/commands.md). Local tests use simulated upstreams and isolated PostgreSQL/Redis. Go 1.27.4 with `GOEXPERIMENT=greenteagc,jsonv2` is retained from the local baseline; this merge does not further upgrade local dependencies.

按既有约定提交并正常推送 `origin/dev`，不自动部署。远端 Actions 不作为本次已通过的验证；本地结果及未执行项目在验证报告中分别列出。
