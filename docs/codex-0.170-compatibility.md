# Codex 0.170.0 compatibility / 协议兼容说明

本轮以 Sub2API `db4705f5a0278fac34bd66b5cbdca74619d8cd7b` 和本地 `D:\Code\codex\codex` 中的 Codex **0.170.0** 源码为依据。实现范围是协议转换、推理强度、Guardian 路由隔离、指纹观测和遥测。官方目录、现有模型描述符、提示词模板及 Cyber 附件保持现有内容。

验证命令、原始日志和性能数据见 [验证记录](validation/codex-0.170/README.md) 与 [复现命令](validation/codex-0.170/commands.md)。本次交付不部署、不调用真实模型、不修改 Engine 或生产配置。

## 中文

### 推理强度与请求配置

- 请求中的推理强度支持字符串和无符号整数，处理时保留原有 JSON 类型。数字 `123` 和字符串 `"123"` 是不同的映射来源；数字支持精确的十进制 uint64 范围 `0..18446744073709551615`，不经过浮点数。小数和科学计数形式不作为有效强度，有上限时拒绝，未配置上限时由上游校验非法联合类型。
- 按当前请求携带的 `configuration_update` 顺序处理配置，优先使用最后生效的当前配置，其次使用隔离缓存中的续链继承值，最后才是基础配置。新的全量请求不继承续链状态。原生透传保留所需协议内容；Chat／Messages 桥接将有效配置用于相应目标协议。
- 已知档位继续执行原有上限、映射及计价规则。未知值不会被猜测成 `medium`、`xhigh` 等已知档位，也不把 `disabled` 改为 `none`。续链缺少有效状态且无上限时，实际／请求强度保持未知；有上限时明确拒绝并要求完整请求或配置更新。
- OpenAI／Composite 分组映射增加可选 `from_type`：`string` 或 `number`，省略时按字符串处理。配置中的自定义字符串来源去除外侧空白后最长 64 个 UTF-8 字节，按大小写精确匹配；数字配置规范化前导零。自定义／数字来源必须限定模型。映射目标仍使用现有已知档位或拒绝访问，请求中的原始自定义字符串保留大小写。
- 用量记录及前端保留长字符串、自定义值和数字值的显示，不因旧的固定枚举而丢失。没有新增独立的服务端 `ultra` 档位、倍率或分组选项。

续链缓存按分组、用户、响应 ID、账号授权／凭据版本、目标和代理隔离。L1 最多 65,536 条并按 TTL 增量清理；响应 ID、模型名和强度分别限制为 1,024、200、64 字节，类型化 Redis 记录限制 4 KiB。缺失、损坏或过期时不借用其他范围的状态。

### continuation 与音频桥接

Responses 请求经过 Chat／Messages 协议桥接时，非空 `previous_response_id` 会收到明确的 HTTP **400**，要求携带完整输入重新发送。桥接端没有原生 Responses 历史，不能静默去掉该字段后只转发增量；这个错误不触发换账号重试。

Chat 桥接支持请求中内联的 WAV／MP3 音频，使用目标协议的 `input_audio`，保留原 Base64 数据和工具调用／结果顺序。远程音频 URL、`file_id`、不支持的格式和无法表示音频的 Anthropic 转换返回明确错误。原生路径继续保留已有音频协议，不增加音频下载、转码或额外模型调用。

### Guardian 与线程身份

合法 reviewer 请求可根据 `client_metadata.parent_response_id`，复用本机从成功上游 WS 握手取得的父响应路由 Cookie。`response.created` 即可关联父响应，父请求仍在输出时也能安全建立关联。

- 仅处理允许的 ChatGPT HTTPS／WSS 地址和 `__oailb`、`__cflb` 路由 Cookie，保留有效期。重定向改变 origin 或 path 时移除路由 Cookie。
- 关联按用户、分组、业务账号、凭据归属、授权代次、OS、目标 origin/path 和代理隔离。连接池同时检查 pin 与所属范围；直通后续帧需要新 pin 时，在发出帧前安全重连。
- 本机路由 pin 与响应引用共用有界 FIFO，合计最多 4,096 条、内存预算 8 MiB。Cookie 不进入数据库、Redis、指纹详情、遥测或日志；重启后自然清空。过期或缺失时使用原正常路径，不复用其它范围的关联。
- 合法、显式的线程独立 `originator` 得到保留，并随当前 WS 回合使用的身份快照传播。规范化身份策略及授权隔离规则保持现有语义。
- Guardian 的服务档位与权限规则保持既有处理，本轮没有扩大用户权限或绕过请求审核。

### 真实 reasoning replay 遥测

从实际完成终态提取上游 `input_transformations` 摘要，只计 `type="reasoning"` 且 `action="removed"` 的条目。兼容真正的 `response.completed`，以及 `response.incomplete` 中 `incomplete_details.reason="interrupted"` 的完成语义。

只保存六类固定计数：`organization_mismatch`、`end_user_mismatch`、`model_mismatch`、`prefix_mismatch`、`turn_boundary`、`other`。未知或缺失的原因归入 `other`，不保留原始条目、任意原因字符串或正文。

| 上游事实 | 摘要 `report` |
|---|---|
| 字段缺失、null 或非数组 | `unavailable` |
| 数组存在，但没有 reasoning removed 条目 | `no_removals_reported` |
| 存在 prefix mismatch | `prefix_mismatch_reported` |
| 其余实际移除 | `removals_reported` |

每个物理尝试只记录一次 `codex.responses.reasoning_replay`；有合法数组时，六类原因各记录一个 `codex.responses.reasoning_items_removed` 直方图样本，实际为零也保留。直方图边界为 `0, 1, 2, 4, 8, 16, 32, 64, 128, 256`。客户端随后断开不抹除已观测的摘要；普通失败、超时或未看到完成终态时不生成报告。

这些指标仅来自真实观测，不进入初始化模拟或逻辑回合二次汇总。旧记录没有采集字段时显示“未记录”，不补造 `unavailable`。指纹详情只展示上述固定摘要。

`codex.websocket.continuation` 已登记协议指标结构，但客户端续链决策缺少可靠来源，覆盖状态为 `awaiting_source`，不生成推测样本。现有 critical path、工具声明增量、技能和认证存储信号继续按各自覆盖状态展示。

遥测聚合快照写入 **v4**，兼容读取 **v1–v4**。已封批的 payload 和重试内容保持原样；新数据只影响未封批聚合。

### 版本、迁移与发布

- 内置 Codex UA／兼容版本兜底更新为 **0.170.0**；管理员配置和自动同步值的优先级保持现状。
- 官方使用的 `OTel-OTLP-Exporter-Rust/0.31.0` 没有变更，本轮继续使用 **0.31.0**。
- [迁移 306](../backend/migrations/306_codex_reasoning_effort_text.sql) 将 `usage_logs.reasoning_effort` 和 `usage_logs.requested_reasoning_effort` 扩为 `TEXT`，保留原有值。没有新增表或新增独立 Ultra 计费配置。
- 请求／回合内使用只读解析结果和小型字段联合值；音频不转码，摘要不持有完整正文或 headers。新增状态、Cookie 注册表和指标维度均有界。

管理员安排升级时，应先备份应用版本及数据库，再统一运行迁移和新版应用实例，随后核对推理配置、使用记录与遥测恢复。v4 聚合和自定义长强度值需要新版程序处理；需要回退时，使用匹配的应用与数据库备份，避免旧实例继续写入同一套新状态。本任务只提交代码和验证材料，发布由管理员另行执行。

## English

### Scope and request behavior

This update is based on Sub2API `db4705f5a0278fac34bd66b5cbdca74619d8cd7b` and the local Codex **0.170.0** source tree. It adapts protocol conversion, reasoning effort, Guardian routing isolation, fingerprint evidence and telemetry. Existing catalogs, descriptors, prompt templates and Cyber attachments are preserved.

Reasoning effort accepts strings and unsigned integers without flattening their JSON types. Numeric `123` and string `"123"` are different mapping sources. Decimal uint64 values are preserved exactly through `18446744073709551615`; fractional and scientific notation are not valid effort values. A configured cap rejects them; without a cap, upstream performs invalid union-type validation. Effective configuration uses the last current `configuration_update`, then the scoped inherited continuation value, then the baseline. New full requests do not inherit continuation state. Known levels retain established limits, mappings and pricing; unknown values are not guessed into known levels, and `disabled` is not rewritten to `none`. A continuation lacking verifiable effective configuration remains unknown without a cap, or fails explicitly with a cap and asks for a full request or configuration update.

OpenAI and Composite mapping rules support optional `from_type="string"|"number"`, defaulting to string for old rules. Configured custom string sources are case-sensitive and limited to 64 UTF-8 bytes after trimming outer whitespace; configured numeric sources normalize leading zeroes. Both require a model scope. Targets remain known levels or denial. Usage records and UI retain custom, long and numeric values. No standalone server-side Ultra level, multiplier or group option is introduced.

Continuation state is scoped by group, user, response ID, account authorization/credential revision, target and proxy. L1 is capped at 65,536 entries with incremental TTL cleanup. Response IDs, models and effort are bounded at 1,024/200/64 bytes; typed Redis records at 4 KiB. Missing, corrupt and expired state never borrows another scope's configuration.

Responses-to-Chat/Messages bridges return HTTP **400** for a nonempty `previous_response_id`, asking the client to resend the complete input. They do not silently drop a server-side history reference or trigger account failover. Chat conversion accepts inline WAV/MP3 data as `input_audio`, preserving Base64 and tool ordering. Remote URLs, file IDs, unsupported formats and unsupported Anthropic audio conversion fail explicitly. Native audio behavior is retained.

### Guardian and identity

Valid reviewer requests can use `client_metadata.parent_response_id` to retrieve a local routing pin captured only from a successful upstream WS handshake. `response.created` associates the parent while it is still streaming. Only allowed ChatGPT endpoints and the `__oailb`/`__cflb` routing cookies are eligible, with expiry preserved. Redirects that change origin/path remove these cookies.

Pins are scoped to user, group, business account, credential ownership, authorization generation, OS, target and proxy. Pool compatibility includes the pin and scope. The process-local registry uses one bounded FIFO for routing pins and response references, capped at 4,096 total entries and 8 MiB. Cookies are not persisted or included in evidence, telemetry or logs. Missing/expired pins use normal routing; independent valid thread originators are preserved in the frozen per-turn identity.

### Observed replay metrics and persistence

Actual completed Responses terminals, including an incomplete terminal whose reason is `interrupted`, can provide `input_transformations`. Only exact reasoning/removed entries count. The retained evidence contains six fixed reasons: organization, end user, model and prefix mismatch, turn boundary, and other. No response content or arbitrary upstream reason text is retained.

The report is `unavailable`, `no_removals_reported`, `prefix_mismatch_reported` or `removals_reported`. Each physical attempt records one `codex.responses.reasoning_replay` sample. A present array also records six `codex.responses.reasoning_items_removed` samples, including genuine zero counts, using boundaries `[0,1,2,4,8,16,32,64,128,256]`. Subsequent delivery failure does not erase already observed facts. Failures without completed semantics do not produce replay reports.

Replay metrics are observed-only, excluded from startup simulation and logical-turn double counting. Legacy records without collection remain unrecorded. `codex.websocket.continuation` is registered as `awaiting_source`; no client continuation decision is fabricated. New unsealed aggregates use snapshot **v4**, reading **v1–v4**; sealed payloads and retry contents are unchanged.

### Upgrade and verification

The bundled UA/compatibility fallback is **0.170.0**, retaining configured and synchronized identity precedence. The official exporter remains **`OTel-OTLP-Exporter-Rust/0.31.0`**. Migration **306** widens both reasoning-effort usage columns to `TEXT` without rewriting existing values.

Before an administrator deploys, back up the matching application and database, apply migrations and switch application instances together, then verify reasoning policies, usage display and telemetry recovery. Rollback uses matching application/database backups because older readers do not understand v4 aggregates or the new custom-value handling. This work does not deploy, modify production configuration, invoke real models or modify Engine.

See the [validation report](validation/codex-0.170/README.md) for completed checks, existing failures, validation scope, raw logs and performance results.
