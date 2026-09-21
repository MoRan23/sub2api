# OAuth 三系统身份与 auth.json 验证记录

验证日期：2026-09-21。实现基于 `4ac9cfabf`，工具名及缺省提示词调整单独提交于 `c84e823a9`，相关旧测试断言单独提交于 `b9e2816da`。所有出站测试使用 mock／本地服务，未发送真实收费请求。

## 已通过

- 三系统迁移、只修复缺项、新建 Windows 默认、Spark 继承、账号类型边界、无可靠存储时拒绝临时身份、单系统重生成及旧请求兼容。
- 原始 UA 优先、当前独立环境兜底、历史／引用／技能排除、终端名称不改变系统，以及 Android／iPhone／iPad 等移动系统视为未知。
- HTTP 最终 UA、安装 ID 和同步根一致；日轮换关闭时系统隔离；同请求跨日／账号切换；WS 连接身份及根冻结；后台默认系统与存储失败停止发送。
- 指纹观测仅在实际 UA、根一致时标记系统；启动补全可取消并等待退出；Wire 重新生成，`cmd/server` 测试通过。
- auth.json 格式、字段错误、缺 refresh token／access token 过期警告、旧 Agent Identity 拒绝、导出不修改凭据、step-up、no-store、审计及导出再导入账号选择一致。

Go 使用 `GOEXPERIMENT=jsonv2`。在 WSL Ubuntu-24.04 上对新增服务、管理 handler、路由、中间件、UA 解析及服务关闭测试执行 `go test -race -p 2 -tags=unit`，通过。移动 UA 修正后针对解析用例单独重跑 `-race`，通过。

隔离集成使用 Testcontainers、PostgreSQL `18.1-alpine3.23` 和 Redis `8.4-alpine`，设置 `CI=true`，没有以跳过代替通过。`go test -race -p 2 -tags=integration ./internal/repository` 选择以下测试组，全部通过：

- `TestOAuthOSProfilesConcurrentInitialization`
- `TestAccountRepoSuite/TestOAuthOSProfiles*`
- `TestAccountRepoSuite` 下配置陈旧写入保护、并发重生成 pin 检查、类型切换拒绝和显式 TLS 配置用例
- `TestOAuthDailyOSRoots*`
- `TestOAuthOSIdentityFacadeRealRedisIsolation`

上述测试验证了真实数据库行锁和唯一约束、旧安装镜像损坏恢复、资格变化、跨实例初始化收敛、六根旧值绑定、午夜换日、只读查询、级联删除，以及生产 facade 对真实 Redis 的三系统线程隔离与同系统重试复用。Redis 用例覆盖每日根开启及关闭。

前端相关 194 项测试通过，包含单系统重生、防重复点击、切换账号后忽略迟到结果、Spark 继承、PAT／Agent Identity 保留旧编辑方式、根列表、auth 导出与 step-up。`typecheck`、`lint:check`、生产构建均通过；构建仍提示既有的大 chunk 警告。

使用本地 Vite 和合成 API 完成浏览器验收：三套只读资料、仅 macOS ID 重生成、创建时三套待生成行、六条具名根、Spark 跳转母账号、首次 step-up 后重试导出。浏览器实际下载的 `auth.json` 只包含官方 auth 对象，没有外层封装或 warnings。未使用生产凭据；模拟服务与测试下载已清理。

## 已有失败

扩大回归范围后，下列失败均在未包含本次改动的 `4ac9cfabf` 独立目录中复现；未修改这些断言掩盖失败：

| 范围 | 失败测试 |
| --- | --- |
| service | `TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling` |
| service | `TestOpenAIGatewayService_Forward_APIKeyMissingInstructionsKeepsLargeInputRaw` |
| service | `TestOpenAIInstallationAPIKeyRemainsUnchanged` |
| service | `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover` |
| service | `TestOpenAIGatewayService_APIKeyPassthrough_PreservesBodyAndUsesResponsesEndpoint` |
| service | `TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries` |
| service | `TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite` |
| service | `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta` |
| service | `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB` |
| repository | `TestProxyUpdateInvalidatesBoundProbeSnapshotsAndEnqueuesOutboxAtomically` |
| repository | `TestProxyUpdateRollsBackWhenProbeInvalidationOutboxFails` |
| repository | `TestProxyUpdateSkipsProbeInvalidationForNonIdentityChange` |

service 失败主要涉及原有 API Key 转发附加 metadata 后的原文／长度断言；repository 三项是既有代理 O2O SQL 与 mock 预期不一致。因此不宣称全仓测试全部通过。

## 边界

未部署、未发布标签、未调用真实上游；升级时仍需按 [迁移说明](OPENAI_OAUTH_OS_IDENTITIES.md) 排空旧进程。没有运行全仓所有平台的测试，也没有使用生产账号验证真实上游会话行为。
