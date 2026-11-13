# Codex 0.169.0 compatibility / 兼容说明

## Scope / 范围

This adaptation follows the supplied local Codex 0.169.0 source. The built-in
client UA fallback and compatibility marker become `0.169.0`; saved administrator
settings and synchronized client identities retain precedence. Model manifests,
descriptors, prompts and Cyber attachments are unchanged. No new Ultra tier,
database columns or telemetry snapshot version is introduced.

本次以提供的 Codex 0.169.0 源码为依据，更新内置 UA 兜底版本及兼容标记。
已有管理员配置与自动同步身份保持原优先级；模型目录、描述符、提示词和 Cyber
附件保持原样。沿用已有 Daybreak、授权隔离、调度、重试及计费规则。

## Guardian requests / Guardian 请求

Only an explicit, single `x-codex-guardian: reviewer` or `classifier` value enables
the dedicated behavior on OpenAI OAuth Codex requests. Absent values are not
generated. Unknown or conflicting values are observed as invalid and do not
activate that behavior. An account header override cannot manufacture a
Guardian request. Ordinary API Key and Engine paths keep their existing protocol.

Valid Guardian requests omit `service_tier` on the final physical send, including
when Fast is forced locally. Reviewer requests also omit the routing hint.
Accounting and observations use the final request. This marker grants no user
permissions and does not bypass prompt auditing.

仅客户端明确提供的单一合法标记触发 OAuth Guardian 处理。最终发送时省略
`service_tier`，优先于强制 Fast；reviewer 同时省略 routing hint。
缺失标记不补充，异常标记不启用专用处理，普通 API Key 和 Engine 保持现有行为。

`client_metadata.guardian_credits_requested` and `client_metadata.parent_response_id`
retain their explicit values in the flat body metadata. They are not generated
or moved into `x-codex-turn-metadata`. The parent identifier is not a continuation
`previous_response_id`. Fingerprints store only credits type/status and parent ID
presence/format, not the complete parent identifier.

两项元数据保留正文平铺位置，不自动申请 credits 或构造父响应 ID。
指纹页展示 Guardian 类型、字段状态、最终档位及处理原因；旧记录显示“未记录”。
WS 观测以实际物理连接握手为准。连接池、预热和重连要求 Guardian 标记匹配；
强绑定续链不匹配时明确失败，不删除响应 ID 或另开连接重放。

## Standalone context / 独立上下文

A `function_call_output` is standalone when `call_id` is absent or null, `name`
is a nonblank string, `namespace` is absent/null/string, and `output` has a valid
string or structured array form. Its item `id` remains independent and is not
copied to `call_id`. This includes `agent.proposed_plan` and other named context.
Real tool outputs still require a corresponding call; standalone items cannot
make an unrelated orphan tool result valid.

HTTP、OAuth 转换、WS 续链和完整性检查共用该判定，保留结构化 output 与独立条目
ID。已有 page、automation、delegation 兼容行为继续保留。

## Interrupts on HTTP bridges / HTTP 桥接中断

The downstream connection has one reader, serialized writes and at most one
queued create. While an HTTP response is active, `response.interrupt` promptly
returns `response.interrupt.failed`: HTTP has no native interrupt acknowledgement.
The response continues to its real terminal event unless the client disconnects.
No accepted acknowledgement or synthetic interrupted terminal event is emitted.

HTTP 流执行期间可及时处理中断控制帧。控制帧不创建 turn，不触发新模型请求、
首输出计时、账号失败或重复结算。客户端实际断开后取消 HTTP，并按现有 1200ms
有界清理约定退出读取、关闭 Body 和释放资源。保留完整交付计费条件，不新增
后台追用量或断连补扣。原生 WS 的 ACK 顺序和连接退役规则保持原行为。

## Telemetry / 遥测

Existing simulated main/title turns in the `critical_path_status=disabled` branch
now contain three additional nulls: `critical_path_sampling_ms`,
`critical_path_compaction_ms`, and `critical_path_auto_review_overhead_ms`.
Tool, command, file-change and hook events propagate an observed `turn_trigger`
when available; unknown values are omitted. Source annotations distinguish
observed and simulated facts. Missing client measurements remain `awaiting_source`.

新增三项关键路径字段输出 null，不使用网关延迟推算客户端时间。已有模拟 Guardian
指标补齐标签，仍标为 simulated，不增加审核次数或虚构 token／耗时。
WS 事件分类补齐 interrupt accepted/failed 和 `codex.rate_limits`；保留聚合上限。

Each real pooled or passthrough WebSocket Dial records `codex.websocket.connect`
and its duration once, including failed handshakes, timeouts and prewarm. Pool
reuse records no connection. This independent observer does not create a turn,
sampling or model attempt. `reauth` is available only from an unambiguous previous
successful connection in the same logical session/route, comparing authorization
generation and credential revision. Unknown evidence is omitted; failure does
not advance the successful baseline.

建连回调仅冻结小型标量，不持有 Gin、完整 headers、正文或可变账号。
现有开关、策略代际、授权边界、有界队列和批次存储继续生效；握手不等待数据库。
沿用 v3 聚合快照。旧未封批数据使用新生成器；已封批 payload 保持原字节发送与
重试，无需数据迁移。

## Rollout / 更新与回退

Deploy the validated application normally; no data migration is required for
this adaptation. Historical observations without new fields remain readable.
A previous application can be restored without rewriting sealed telemetry.
Validation uses local fake HTTP/WS servers and isolated PostgreSQL/Redis only.
Delivery pushes source changes; production deployment and Engine changes are
outside this update.

验证记录及固定场景性能对比见 [Codex 0.169.0 validation](validation/codex-0169.md)。
