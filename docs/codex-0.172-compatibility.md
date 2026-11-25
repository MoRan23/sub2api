# Codex 0.172.0 compatibility / 协议兼容说明

Baseline: Sub2API `561ed4714`. Reference: the local `D:\Code\codex\codex` source archive, whose Rust workspace version is **0.172.0**. This records gaps found against that source; it does not assign an introduction release to changes without upstream history.

## 中文

### 身份和追踪

内置 Codex UA 和协议兼容版本兜底更新为 **0.172.0**，管理员配置和自动同步值继续按原优先级生效。

OAuth／SetupToken 的 Responses HTTP 最终发送阶段复用 WS 的 W3C 校验，转发实际入站的合法 `traceparent`／`tracestate`。普通请求、透传、compact、Messages 转换及 WS→HTTP 桥接共用此入口。重复或非法值不发送，`tracestate` 依赖合法的 `traceparent`；账号请求头覆盖不能生成追踪身份。普通 API Key 和 Engine 的处理不变。

解析结果按请求缓存，只包含小型标量。指纹详情区分 `http_request` 与 `ws_handshake`，只报告存在／有效状态，HTTP 的有效状态核对最终出站头。完整 trace ID 不进入观察记录、连接兼容键或遥测维度；连接复用不因 trace 变化重连。

### 模型目录

账号 metadata、模型列表转换及合成别名现在保留上游的 `web_search_provider`。`standalone`、`search_mcp`、缺失和显式 `null` 沿用已有能力投影及多账号一致性规则；不补默认 provider。缓存往返和原始目录投影保留嵌套提示及未知字段。

现有官方目录资产、6.2 描述符、Cyber 附件、音频声明和 `unified_exec` 保持原样。新增字段不改变授权、路由或服务端搜索实现。

### 协议桥接

`function_call_output` 的 `call_id` 缺失或为 `null`、`name` 非空、`namespace` 缺失/null/字符串，且 `output` 为字符串或内容数组时，属于具名独立上下文。原生 Responses 保留原内容；转换到 Chat／Messages 时输出普通 user context，先添加 JSON 转义的来源标签，再按序保留输出内容，不生成工具调用 ID。合法空文本可正常转换，非法文本字段和目标协议无法表示的独立上下文内容明确报错。

真正工具调用和结果继续执行原有关联及排序；审核和时区转换的来源映射保留原始输出路径。

Chat／Messages 无法表示仅使用 `file_id` 的图片引用时，转换在请求上游前返回 HTTP 400，要求图片 URL 或内联数据；覆盖消息和工具结果中的图片。原生 Responses 的文件引用不变。本次不新增附件下载或远程存储。

### 遥测与性能

官方 exporter 仍为 **`OTel-OTLP-Exporter-Rust/0.31.0`**。metrics 继续发送 JSON＋zstd，analytics 继续 JSON，现有代理和冻结身份保持生效。

覆盖页面新增执行命令耗时／计数、多智能体创建、additional context、注册工具、MCP 目录大小、模型搜索发现及 artifact 操作的待接入状态。它们依赖客户端执行器、注册表或插件归因事实，因此仅登记 `awaiting_source`，不新增模拟样本或启动指标。

新增能力在目录提取阶段处理；追踪不解析正文；新增上下文来源映射不重复转换或编码图片数据。遥测沿用 v4 快照，已封批内容保持原样。无需数据库迁移、依赖升级或生产配置变更。

## English

The built-in Codex identity and compatibility fallback is **0.172.0**, retaining configured and synchronized version precedence. OAuth/SetupToken HTTP Responses requests now reuse the validated inbound W3C tracing projection already used by WebSockets. This includes compact and protocol bridges. Duplicate/invalid values are omitted, tracestate requires a valid traceparent, and account overrides cannot invent trace context. API-key and Engine behavior remains unchanged. Observations retain only validity and transport source, checking the final HTTP headers; raw trace IDs are not retained as observation data, connection identities or metric labels.

Account metadata and synthetic catalogs preserve `web_search_provider` with existing null and cross-account agreement semantics. There is no provider default, new search service or wholesale descriptor replacement. Official assets, Cyber attachments and current audio capabilities remain intact.

Named standalone function outputs become user context in Chat/Messages bridges, with an escaped provenance label and ordered original content. Missing/null call IDs remain distinct from malformed IDs. Actual tool pairing and source mappings are retained. File-only image references unsupported by the target protocol produce a client error before upstream access instead of being silently discarded; native Responses remains unchanged.

New client-only telemetry families are listed as awaiting their source, without adding startup or simulated measurements. OTLP exporter **0.31.0**, JSON/zstd transport, existing proxy/identity policy and v4 aggregate snapshots are unchanged. The update requires no database migration or deployment configuration changes.

Verification and performance results: [validation report](validation/codex-0.172/README.md). Implementation and validation use local fixtures and isolated databases, without real model calls, Engine changes or production deployment.
