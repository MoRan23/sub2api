# OpenAI OAuth 三系统独立授权验证记录

基线：`41501a3de`。验证仅使用本地模拟上游、隔离数据库和 Redis；没有发送真实业务、
OAuth 登录、收费采集或遥测请求，没有部署或发布标签。

## 已验证的行为

- 旧凭据只迁入默认系统；迁移重放、撤销后的再次补全不会复活授权。Windows、macOS、
  Linux 的授权、刷新 CAS、失败状态和 token 缓存隔离，缺少授权时不会借用其他槽。
- 已知入站系统按原始请求冻结；未知系统逐候选使用其默认系统。覆盖普通调度、TopK、
  粘性会话、previous response、重试、Spark，以及 HTTP、Chat、Messages、透传、
  HTTP/WS bridge、原生 WS、Live、Images、count tokens、模型目录和插件目录。
- WS 握手 token 与冻结槽的版本一致；授权撤销或更换后，已有连接不能静默换凭据。
  Spark 刷新写入母账号对应槽，同时保留业务账号 ID。已交付请求的用量不因随后发生的
  授权撤销、冷却或重新授权而漏记。
- 账号授权 state 固定账号、系统、用途和授权代次，一次性消费；不接受跨用户／工作区
  授权或把相同 refresh token 复制到另一槽。测试、auth.json 导出和导入使用指定系统。
- turn-state 的缓存、需求、轮换与观测按系统隔离；429 维持账号共享冷却。只修改采集
  代理列表时，各系统已有正常有效票据保留原到期时间，旧采集结果不能覆盖新配置。
- 遥测批次冻结授权槽及代次，旧授权批次跳过；新旧授权统计不混合，安装池保持稳定。
- compact 正文清理后仍从入口快照读取原生 UUIDv7 根及子线程，避免丢失连续性。
- 普通单账号／批量更新和旧快照不能覆盖受管 provider 凭据或授权模式；显式模式转换
  要求独立动作标记。多系统备份使用版本 2，并覆盖已撤销默认系统的恢复。

## Go 与数据库

运行了相关 service、handler、admin、repository 回归及定向 `-race`。竞态覆盖授权与
刷新、token 快照、Spark、并发 WS、同步根及 compact、turn-state、遥测、管理导入导出
与用量记录。`go build ./cmd/server` 通过。单账号及批量编辑的综合竞态回归、
辅助请求组的最后一轮竞态回归均已取得通过结果。

合并回归命令（backend 目录）：

```bash
go test -tags=unit ./internal/service ./internal/handler ./internal/handler/admin ./internal/repository \
  -run 'Test(OpenAI|CodexTelemetry|CRS|Account.*OAuth|Account.*Codex)' -count=1
```

通过 WSL Ubuntu 的 Docker 运行隔离 PostgreSQL 18.1／Redis 8.4 集成测试，使用
`CI=true` 避免没有 Docker 时静默跳过。覆盖全套 AccountRepoSuite、迁移 251／252、
迁移 SQL 重放、授权并发 CAS、导入回滚、旧写入保护、账号共享冷却、跨系统缓存、
单次／批量代理修改及遥测重启恢复。最后的存储回归全部通过。

## 前端

相关 16 个文件的 259 项测试通过；类型检查、lint 和生产构建通过。新增模式转换请求
类型后再次通过类型检查。构建保留原有 chunk 和 Browserslist 提示。

在本地真实 Vue 组件配模拟接口完成浏览器验收，2026-09-22 补验的十项检查均通过：
三系统授权状态展示、未授权和需重新授权的系统不可测试、各系统重新授权绑定账号／
系统／用途且锁定选择、各系统测试匹配模型目录，以及 turn-state 系统切换取消迟到
请求并保持两部分结果与每五秒刷新使用当前系统。页面错误及外部请求均为零；唯一
取消的请求为切换系统后主动中止的旧 macOS 状态请求。

浏览器报告保存在本地 `.git/task-artifacts/os-oauth-auth/browser-report.json`；测试脚本
使用已安装的 Chrome、Vite 真实组件和全部拦截的本地 API。测试入口单独使用完整 i18n
编译器，未修改产品配置。按系统导出的 API 参数和响应由前端组件／API 测试及后端
导出测试覆盖。未使用真实授权或业务端点。

## 基线失败与限制

以下四类旧测试在隔离的 `41501a3de` 工作树中也失败；保持原断言，未为本功能修改
API Key 的生产身份规则：

- `TestOpenAIInstallationAPIKeyRemainsUnchanged`：旧测试期待保留传入 installation 字段，
  既有实现会清理该字段。
- `TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries` 和
  `TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop`：旧测试期待 OpenAI API Key 不参与 Codex
  身份投影，既有实现已经将其纳入。
- `TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite/api_key_passthrough`：
  旧测试比较原始正文，既有默认身份投影会增加 client metadata。
- `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta/api_key_explicit_beta_remains_caller_controlled`：
  旧测试期待 API Key 原样保留 `responses=experimental`，既有 Codex 身份投影已清理
  此过时 beta；本次在原基线独立工作树再次复现。

最后一轮 service 合并回归完整运行结束，无 panic，仅剩上述四项匹配命令范围的基线
失败。`TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop` 不匹配该命令的前缀，仍按此前
独立验证保留为基线失败。handler、admin、repository 同范围均通过。此次补齐的
OAuth beta／路由诊断、并发 WS 执行作用域、图片限流及能力冷却夹具均通过定向竞态测试。

另有旧的 API Key Chat／透传测试依赖未显式配置的身份开关；已给精确正文测试明确关闭
该开关，并保留其原有正文、稳定性和租户隔离断言。OAuth 旧测试已显式建立授权槽，
生产代码没有增加无授权回退。未运行与此次变更无关的全仓测试。
