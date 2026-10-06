# Codex-Engine 接入

在账号管理中创建或编辑 **OpenAI → API Key** 账号，将“接入模式”设为 **Codex-Engine**，填写 Engine Key 和 **Codex-Engine 平台 API** 地址。不要填写 Worker 直连地址：Worker 不提供完整的 Chat、Messages 和 count_tokens 入口。

地址支持部署路径前缀、尾斜杠和已有 `/v1`，例如 `https://engine.example/prefix/v1`。模式保存在 `extra.openai_api_key_mode`，值为 `generic` 或 `codex_engine`；旧账号未设置时仍使用通用模式。账号导入导出会保留模式，无需数据库迁移。

专用模式复用 Sub2API 的鉴权、分组权限、模型白名单与映射、速度策略、并发、限额和记账。普通透传及协议兼容设置在此模式下不参与转发。Responses（包括 compaction_trigger）、Responses compact、Chat Completions、Messages、独立搜索、图片生成/编辑和 Messages count_tokens 均转发到平台同名接口，由 Engine 处理协议转换。count_tokens 继续不计费。

JSON 正文保留工具、namespace、历史、密文及未知字段，仅按现有模型映射和速度策略修改必要字段。公开会话、线程、回合与工具关联头会保留；下游认证与 Engine 内部控制头不会转发。Engine 的 JSON/SSE 和业务错误原样返回，业务错误不会触发账号停用、兼容降级或跨账号重放。已有用量及图片结算流程继续使用上游实际返回的信息，不估造缺失 token。

排查失败时，在运维错误记录查看上游请求 ID、错误代码和消息。HTTP 200 的 SSE 也可能以 `response.failed` 结束；这类失败会被记录为错误，客户端仍收到上游原始事件。没有实际 token 或图片用量的业务拒绝不生成普通使用记录；失败前已有用量的请求继续按现有规则结算。错误诊断只记录错误字段，不额外存储生成内容或请求历史。

即使 `response.failed` 在错误字段前包含大量输出，诊断仍使用专用转发器从完整事件提取并脱敏的错误摘要，避免日志捕获上限把具体原因覆盖为 `upstream stream failed`。摘要沿用现有状态码和错误类型分类，不扩大正文留存范围；发给客户端的 SSE 保持原样。

专用模式不会修正历史项 ID、删除密文或去掉 `previous_response_id`。`context_owner_unknown` 表示 Engine 无法确认密文历史归属；`invalid_id_prefix` 或 `unsupported_persisted_item_context` 表示上游拒绝了历史项或无法续接已存储上下文。应结合 Engine 日志处理具体原因；既有不兼容历史需要客户端开始新会话或提供可重放的有效历史，重复发送同一请求通常不能恢复。

图片 JSON 参数原样保留。multipart 图片按上传顺序转为 `images[].image_url` data URL，保留内容及 MIME 类型；单个上传项超过现有 20 MiB 限制会明确报错，请求整体大小限制仍生效。`n`、尺寸、质量等显式参数不会自动改写为 Engine 支持的值；不支持的参数由 Engine 返回错误。转换后不上传或执行任何用户文件。

`/models`、`/model-catalog` 仍由 Sub2API 管理。本模式不提供响应读取/删除、`/capabilities` 或 WebSocket 专用适配。
