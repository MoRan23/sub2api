# Codex auth.json 导出

账号管理页的常规 OpenAI OAuth 账号可通过操作菜单直接导出 `auth.json`，无需选择系统。每份文件包含账号共享的一套 ChatGPT 登录凭据；它不包含本项目的账号配置、UA、installation ID、每日会话根或 turn-state 运行态。

## 适用账号与读取行为

- 仅支持 OpenAI 平台的常规 OAuth 账号。
- API Key、setup-token、Personal Access Token、agent identity 和影子账号不可导出。影子账号应前往凭据母账号操作。
- 每次导出直接读取数据库中账号共享授权的最新完整快照，保持该次读取中的凭据一致。导出不刷新 token、不修改账号、不调用上游接口，也不进行真实模型请求。
- 导出要求非空 `access_token`，以及可被 Codex ID-token 解析结构读取的 `id_token`。这里只验证 JWT 包装与相关字段类型，不验证签名或授权资格；ID token 的旧签发时间、过期时间不会单独阻止导出。

## 管理接口与文件格式

接口为 `GET /api/v1/admin/accounts/:id/codex-auth`。旧 `os=windows|macos|linux` 参数保留兼容，但不改变导出的授权。沿用管理员认证和现有敏感操作 step-up 策略。需要 step-up 时，前端完成验证后重试；取消验证不会生成下载。

成功响应使用标准 API envelope，`data` 下包含 `auth` 与 `warnings`。前端仅将 `auth` 序列化为 `auth.json`，不会把 envelope 或警告写入文件。

| 文件字段 | 类型与含义 |
|---|---|
| `auth_mode` | 固定字符串 `chatgpt` |
| `OPENAI_API_KEY` | 固定为 `null` |
| `tokens.id_token` | 当前快照保存的 ID token 字符串 |
| `tokens.access_token` | 当前快照保存的 access token 字符串 |
| `tokens.refresh_token` | 当前快照保存的 refresh token；缺失时为空字符串 |
| `tokens.account_id` | 当前快照保存的 `chatgpt_account_id`；缺失时为 `null` |

不生成 `last_refresh`：导出时间不代表凭据刷新时间，也不能据此延长凭据有效期。字段结构对应 Codex 的 `AuthDotJson` 与 `TokenData`；文档不提供可导入的凭据样例。

## 警告与失败

`warnings` 始终为数组，可以同时包含多个警告：

| 警告 | 含义 |
|---|---|
| `missing_refresh_token` | 文件已导出，但快照没有 refresh token；此文件不能依靠该字段自动续期 |
| `access_token_expired` | 文件已导出，但保存的有效期或可解析的 access token 到期信息表明它已过期 |

有效期未知时不推测已过期。ID token 自身过期不属于上述 access token 警告。

错误遵循现有 API envelope：HTTP 状态在 `code`，稳定错误标识在 `reason`。导出资格或凭据结构错误的 `message` 仅包含相关字段名，不包含 token、JWT 内容或解码错误正文。

| `reason` | 含义 |
|---|---|
| `OPENAI_CODEX_AUTH_EXPORT_UNSUPPORTED` | 账号类型或归属不支持导出 |
| `OPENAI_CODEX_AUTH_EXPORT_INCOMPLETE` | `access_token` 缺失，或 `id_token` 缺失/结构不能解析 |

非法账号 ID、账号不存在，以及管理员认证或 step-up 未通过时，使用既有接口错误语义。

## 缓存、审计与下载

导出处理器返回 `Cache-Control: private, no-store` 和 `X-Content-Type-Options: nosniff`。敏感 GET 读取记录审计动作 `admin.accounts.codex_auth.export`，记录操作者、目标账号及结果等现有审计元数据，不记录返回正文。

前端不把导出内容写入账号缓存、全局状态或本地存储；下载使用短期 Blob URL，并在触发下载后释放。缺少刷新令牌或已知过期不会伪装成无警告的可用凭据。

## 再次导入

Codex session 导入支持该文件结构。选择工作区时，先读取显式 `tokens.account_id`，再读取既有顶层兼容字段；仅在显式账号 ID 缺失时从 token claims 推导，避免把导出时选择的工作区替换成 JWT 中另一工作区。

导出允许携带已过期 access token 并给出警告。导入按现有规则验证并更新账号共享授权；不再创建另一个系统授权槽。文件不会复制服务端私有凭据代次或运行态。详见 [共享授权恢复与升级](codex-turn-state.md#共享授权恢复与升级)。
