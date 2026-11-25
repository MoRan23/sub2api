# 网络故障与 Cyber 账号状态策略 / Network and Cyber account-state policy

## 中文

DNS、代理连接或认证、TCP/TLS、连接中断和网络超时不再自动冷却账号，也不累计账号调度失败或 API Key 健康熔断。该规则覆盖转发、流读取、WebSocket 和 Token 刷新；错误日志、请求失败响应、连接清理及原有有界重试继续执行。

精确识别的 `cyber_policy` 拒绝同样不修改账号冷却、限流、模型屏蔽或禁用状态。原有拒绝响应、审核、邮件、会话阻断与计费规则继续执行。

实际收到的 HTTP 401/403、429、额度错误和 5xx 保留原有处理。错误正文提到网络故障不会将真实 HTTP 响应改判为网络错误。缺少流终态但没有网络错误证据的协议错误，也保留原规则。

错误跨协议转换时使用小型来源标记，避免把网络故障合成的 502/504 当作账号故障。正常请求不新增查询、正文复制或目录解析。已交付插件请求仍遵守禁止自动重试的条件。

兼容说明：

- `gateway.disable_openai_transport_error_cooldown` 及对应环境变量保留解析，值不再影响行为。
- 原流超时处罚设置不再产生账号冷却、禁用或处罚计数；流超时计时器和当前请求清理仍生效。
- 已有冷却按原到期时间结束，管理员暂停和其他原因的禁用保留。
- 代理级故障隔离保留。本次调整针对账号状态。
- 无数据库迁移；回退版本会恢复该版本的原有处罚行为。

## English

DNS, proxy connection/authentication, TCP/TLS, disconnected reads and network timeouts no longer create account cooldowns, scheduler failure penalties or API-key health-breaker failures. This covers forwarding, stream reads, WebSocket and token refresh. Diagnostics, failure responses, connection cleanup and existing bounded retries remain active.

An exact `cyber_policy` refusal also leaves account cooldown, rate-limit, model-block and disabled state unchanged. Existing refusal responses, auditing, email, session restrictions and billing continue.

Actual HTTP 401/403, 429, quota errors and 5xx retain their existing handling. Network-like prose in an HTTP response is not transport evidence. A missing stream terminal event without a network cause remains a protocol failure.

A small error-origin marker survives protocol conversion so synthetic 502/504 errors are not attributed to account health. Successful requests gain no database queries, body copies or catalog parsing. Already-sent plugin requests retain their no-retry conditions.

Compatibility: the legacy transport-cooldown flag remains parseable but is ignored; stream-timeout punishment settings no longer count or penalize accounts, while timeout timers still run. Existing cooldowns expire normally. Administrative pauses and proxy-level isolation remain. No migration is required; reverting restores the previous version's policy.
