# 代理路由与 Codex 轮次兼容修复（2026-10-25）

## 变更

- 账号绑定代理读取失败、关联缺失或配置无效时，OAuth、配额、目录、诊断和相关
  辅助请求停止执行，返回脱敏的本地路由错误，不回退到空代理地址。
- URL 安全检查通过所选代理查询固定 HTTPS DNS 端点；保持双栈私网检查、
  重定向检查及取消传播，失败不回退到本机 DNS。复用客户端连接，淘汰时清理。
- 原生 Codex 的 mailbox 唤醒会使用 UUIDv4 创建普通轮次。允许规范 RFC UUIDv4
  作为轮次 ID，保留 UUIDv7 生成逻辑、非法 ID 拒绝及父／根轮次校验。
  HTTP 重试和 WS 工具／压缩后续轮沿用原 ID，UUIDv4 不生成虚构时间戳。

## 生产只读定位

2026-10-25 12:23:28 至 12:25:51（UTC+8）出现的 90 条集中 502 均对应：

```text
resolve openai OAuth identity plan: invalid OpenAI Codex wire profile:
turn_id is invalid for request_kind turn
```

这些请求在身份解析阶段终止，尚未调用代理传输。日志未保存原始轮次 ID；
本次修复的是按原生源码和本地请求复现的 UUIDv4 兼容缺口。

## 已验证

- 回环 sing-box 1.14.3：四种代表性加密方法（含三种 SS2022）、标准和原生 TLS、
  16 并发、2.34 MB 正文及连接复用，共 384 次本地请求成功。
- 回环 SS2022 到 TLS DNS 模拟器：双栈、CNAME、私网结果、畸形响应、取消、
  超时、错误脱敏及无直连回退。
- HTTP client／repository：普通、TLS 和原生发送路径的代理选择、重定向、
  缓存隔离、失败释放并发资源及校验器生命周期；通过相关竞态测试。
- Service：代理故障零出站、刷新和隐私路径、账号诊断，以及 UUIDv4 的 HTTP
  解析／最终出站／重试和 WS 后续轮；新增用例通过竞态测试。
- OAuth 模型目录的 token 获取前与实际发送前保护通过回归及竞态测试。
  五个平台的令牌提供者均在代理解析错误时停止，不回退旧 token；Antigravity
  的数据库／Redis 冷却零写入及连续项目补全失败用例通过竞态测试。
- 后端 `go build ./...` 通过；golangci-lint v2.14 对本次增量检查为 0 issues。

## 原基线失败

扩大相关回归时，下列既有断言失败。使用 Go overlay 恢复对应 HEAD 生产文件，
再次运行同一用例仍失败；本次保留断言及相关保护逻辑，不将这些结果计为通过：

- `TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity`
- `TestCaptureOpenAIOAuthIdentityIgnoresClientContextWindowID`
- `TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop`
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory`
- `TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization`
- `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization`

本次未调用真实模型，未修改服务器配置、数据或服务运行状态。
