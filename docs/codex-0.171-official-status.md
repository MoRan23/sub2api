# OpenAI official status and Codex 0.171 / 官方状态与兼容说明

Baseline: `3b3554c8b`. Codex reference: the local `D:\Code\codex\codex` source tree, version **0.171.0**.

## 中文

### 官方状态

渠道监控 V2 页顶部显示 APIs、ChatGPT、Codex 的官方状态；分组概览与详细统计共用此卡片。现有监控总开关和 V2 模式控制入口，无需另外开启订阅。所有能进入该页面的登录用户读取同一份公开状态，分组卡片继续使用本站真实请求指标。

- 当前状态来自 `https://status.openai.com/api/v2/components.json`；事件来自 `https://status.openai.com/feed.atom`。
- 每个产品增加官网历史状态条：过去 90 天加今天，共 91 格。历史来自官网所用的公开 `app.incident.io/api/status_page_content/status.openai.com` 目录及其 `component_impacts` 接口，按官方目录中参与可用率展示的组件归组；可用率使用官方分组聚合值。
- 历史按 UTC 日聚合，每日采用最严重状态。绿色正常、黄色维护或性能下降、红色故障、灰色无数据；悬停、键盘聚焦或触屏点击可查看日期及状态。午夜结束的故障不影响下一天，持续故障只统计至本次同步时刻。官网会按访问者时区展示，本站日期在提示中明确标注 UTC。
- 后台每 120 秒刷新，多个实例通过 Redis 租约共享刷新结果。接口 `GET /api/v1/channel-monitor-v2/official-status` 只读进程内快照，不随用户访问发起外网请求。
- 按官方组件身份归入三个产品。已知其他产品排除；未知组件和无法归类的公告保留相应未知状态或综合事件，不用标题猜测归属。
- 官方标题和摘要保留英文原文；界面与状态标签提供中英文。摘要为纯文本，事件链接指向官方站点。事件列表展开后默认显示 5 条，可查看更多，最多 20 条。
- 故障或维护时自动展开事件。页面隐藏时暂停刷新，恢复时立即读取最新缓存。
- 当前状态、事件和历史分别保存最近成功结果。失败保留旧数据并标记延迟，尚未成功获取时显示暂时不可用；正常情况下超过 5 分钟的数据也标记延迟。已有历史保留其实际颜色，不用最近事件列表推算正常天数。
- HTTP 请求复用连接池及更新服务代理配置，每项请求最多 10 秒、1 MiB；缓存最多保留 100 条事件。后台停止时取消正在执行的请求。

此功能只提供站内展示，不新增邮件或 Slack 订阅，不调用模型，也不自动改变账号调度、分组状态或计费。

### 请求协议与指纹

WebSocket 上下文比较忽略逐响应携带的 `request_country` 和 `access_programs`，避免仅这些字段变化就丢弃续链并重传历史。实际发往上游的当前帧保留原值，包括 `request_country` 的缺失、显式 `null` 和字符串。模型、工具和推理配置继续使用既有比较规则。

Codex turn metadata 在现有重建流程中使用官方 **8096 字节 ASCII JSON** 预算。超出预算时只筛选完整的 `workspaces.associated_remote_urls`，优先 `origin`，再按编码长度和稳定顺序选择；不截断 URL，不删除其它字段。去掉所有 remote 后仍超限时，保留原值。body 与兼容 header 分别计算，冻结的输入快照保持不变。

WebSocket 新握手可携带真实入站的合法 W3C `traceparent`／`tracestate`。无值或非法值不生成追踪上下文；trace 值不参与连接兼容键、Guardian pin、账号隔离键或指标标签。连接复用不因 trace 变化重连。指纹详情只显示存在／有效状态，以及 metadata 是否裁剪和最终长度，不新增原始 trace ID 或完整 metadata 存储。

内置 UA 和兼容版本兜底更新为 **0.171.0**，管理员配置与自动同步仍按既有优先级生效。Guardian、线程 originator、interrupt、类型化推理强度及音频桥接继续沿用现有行为。

### 遥测与模型目录

Statsig metrics 在发送边界使用 zstd 压缩，继续使用 JSON Content-Type 和 `OTel-OTLP-Exporter-Rust/0.31.0`。analytics 继续发送原 JSON。数据库封批内容、队列正文和重试原文不变；压缩数据只存活于一次发送，每个发送 worker 复用一个单线程编码器，不按账号创建编码器。现有代理和遥测开关继续生效。

本次不引入通用 OTLP exporter 配置或 `OTEL_EXPORTER_OTLP*` 环境变量处理。

新增以下客户端信号的覆盖说明，状态为 `awaiting_source`：

- MCP OAuth 锁的 `codex.auth_storage.lock_acquire`、`codex.auth_storage.lock_wait.duration`、`codex.auth_storage.lock_hold.duration`。
- 输入来源字段 `codex_turn_event.used_dictation`。
- Guardian V2 的 `codex_guardian_v2_classification.classifier_turn_id`。

这些信号依赖客户端自身事实，服务端不从音频或普通 turn ID 推造样本。遥测继续读写现有 v4 聚合快照。

目录新增可空布尔字段 `multi_agent_tools_in_code_mode` 的账号 metadata 和合成别名传递。缺失、`null`、`false`、`true` 分别保留；多个候选账号声明一致时保留该值，冲突或声明不完整时按现有交集规则返回 `null`。`model_messages.apps.usage_instructions` 保持上游原值。官方目录资产、提示词、Cyber 附件和 `unified_exec` 保持现状。

### 升级

本次无需数据库迁移。带历史条的官方状态使用独立的 v2 Redis 快照与刷新租约，损坏快照等待下次后台刷新恢复；首次运行由后台获取历史，无需等待本地累积 90 天。无需修改生产配置；部署后，已启用的 V2 监控页面将出现官方状态卡片。

实现与验证使用本地模拟服务和隔离数据库，官网公开状态样本仅用于格式核对；不部署、不修改 Engine、不调用真实模型。原兼容测试见 [验证报告](validation/codex-0.171/README.md)，历史条测试和读取分配见 [历史状态验证](validation/openai-official-status-history.md)。

## English

### Official service status

The V2 channel-monitor page now shares a compact OpenAI status card above its group and detailed views. It covers APIs, ChatGPT and Codex, using the existing monitor enablement and V2 mode. Logged-in monitor users read the same public snapshot; local group health remains based on real gateway traffic.

Component health comes from the official components JSON endpoint, while incident updates come from Atom. Historical impacts and product membership come from the public incident.io APIs used by the official page. A background poll refreshes current health, events and history independently every 120 seconds, coordinated across instances with Redis. The authenticated `GET /api/v1/channel-monitor-v2/official-status` endpoint reads memory only. Each source has a 10-second deadline and a 1 MiB response limit; at most 100 incidents are cached and 20 are exposed.

Each product has 91 daily blocks covering today and the preceding 90 days. Days use UTC and the worst observed component state; group uptime comes directly from the official aggregate. Tooltips support hover, focus and touch. Missing history stays gray, and failed refreshes retain the previous history with a stale notice. Half-open day intervals avoid counting an outage ending at midnight in the following day. The official site uses the viewer's timezone; this shared snapshot explicitly labels UTC dates. History is aggregated during refresh, and requests share immutable day slices instead of rebuilding them. The expanded snapshot and refresh lease use version 2, with no database migration.

Failures retain the last successful source value and mark it stale. Data older than five minutes is also stale; a cold cache is shown as unavailable. Unknown component identities are not guessed from incident titles. Incident content remains in the original language as plain text, with official detail links; interface labels are localized. The event list initially shows five items when opened. Refresh pauses while the page is hidden. There are no email subscriptions, model requests, routing changes or billing effects.

### Codex compatibility

WS continuation comparison ignores per-response `request_country` and `access_programs`, while preserving their actual current-frame values. Missing, null and string country values remain distinct. Model, tool and reasoning changes retain their established comparison behavior.

Rebuilt turn metadata follows the official 8096-byte ASCII JSON budget. Only whole workspace remote entries are optional, ordered by `origin`, encoded length and stable keys. Other metadata is retained, and metadata whose non-remote content already exceeds the budget is left intact. Body and compatibility header projections are budgeted separately, without modifying frozen inputs.

New WS handshakes can forward valid, actual inbound W3C tracing headers. Missing or invalid values are omitted. Trace values are not socket/pin/cache identities or telemetry labels, and do not force connection replacement. Fingerprint details retain only validity/presence and small metadata-budget observations. The fallback client and compatibility versions are 0.171.0, retaining configured and synchronized identity precedence.

Statsig metrics use zstd at the send boundary; analytics remain JSON. Persisted sealed payloads, queued input and retry records stay unchanged. The bounded sender workers reuse single-threaded encoders. The exporter version stays 0.31.0, and no generic OTLP environment/header configuration is introduced.

New client-only lock, dictation and Guardian V2 association signals are listed as awaiting their source rather than inferred. Existing v4 telemetry snapshots remain compatible. Model metadata and synthesized aliases now preserve the nullable `multi_agent_tools_in_code_mode` capability; conflicting candidate declarations retain the existing null intersection behavior. Apps usage instructions and existing descriptor/prompt assets are preserved.

No database migration or production configuration change is required. See the [compatibility validation report](validation/codex-0.171/README.md) and [history validation](validation/openai-official-status-history.md) for checks and benchmarks.
