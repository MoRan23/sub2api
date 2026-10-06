# Codex-Engine 错误诊断修复（2026-10-06）

## 现场证据与边界

只读查询了目标账号「自研 普通」的最近 24 小时错误、现有服务日志和部署信息。关联记录的入站、出站均为 `/v1/responses`，没有协议降级证据。

- 502 包含 Engine `native_error`，内层原因是 `invalid_encrypted_content`：密文绑定的 item ID 与请求中的 ID 不一致。
- 409 包含 `continuation_required`，以及较早的 `context_owner_unknown`。Engine 会话负责核查原生注入历史和待工具续传门禁。
- 其中部分 502 的准确原因已存在于 `upstream_errors`，但请求级摘要被覆盖成 `upstream stream failed`。
- Sub2API 未保存这些请求的原始正文，不能从生产日志确认客户端提交时 ID 是否缺省。本地代码和模拟测试表明专用转发分支不改写 reasoning ID、密文、工具结果或 `previous_response_id`；只执行已有模型映射和速度策略。

本轮没有调用真实模型，没有修改生产数据、账号调度开关或部署。

按最后一次上游错误事件重新聚合：81 条 502 均为密文 item ID 不匹配（UTC 10 月 6 日 00:00–00:25）；33 条 409 为工具续接门禁（00:01–00:53）；另 12 条上下文归属 409 来自前一天 08:15–08:16。上述 126 条均有不同的 Sub2API 请求 ID。

## Sub2API 修复

原中间件从有限长度的 SSE 捕获内容中提取错误；`response.failed` 在错误之前携带较多输出时，捕获可能截断错误字段。专用转发器虽已读取完整事件，其准确诊断仍会被中间件的兜底消息覆盖。

现在专用转发器额外提供脱敏、仅含错误的诊断事件，中间件继续使用原有错误解析与分类逻辑。此摘要不发送给客户端、不包含生成内容、不用于记账。补齐顶层错误、`status_code` 及无 `event/type` 的嵌套错误；保持独立的 `response.incomplete` 处理行为。

## 本地验证

- 修复前，两种超过正文捕获上限／终止事件探测上限的模拟响应均复现摘要丢失。
- 回归覆盖具体错误与上游请求 ID 保留、只记录一次、输出内容不入错误摘要、HTTP/SSE 原样返回。
- 覆盖 429 推断、显式 403/502、HTTP 409、顶层及嵌套错误，并验证显式状态优先。
- 模拟 Engine 请求验证 ID 有值、缺省、`null`、空字符串，以及工具结果、响应 ID 和未知字段原样转发；既有混合账号池验证继续保留。
- 受影响 Go 测试、全后端编译和改动范围 lint 均通过；本轮无前端或数据库结构修改。

```sh
go test -tags=unit ./internal/handler ./internal/service -run 'CodexEngine|OpsErrorLoggerMiddleware|OpsCaptureWriter|ParseOpsSSEFailure' -count=1
go build ./...
golangci-lint run --new-from-rev=HEAD ./internal/service/... ./internal/handler/...
```

Engine 的原生修复与发布验证在对应仓库交付；本项目的日志修复本身不能恢复已损坏的历史上下文，也不代表线上故障已经消除。
