# 上游 v0.2.7 合并记录

## 合并范围

- 本地基线：`4897e8b5395f093d5a868cdf9ab2fef2fbb8c738`。
- 固定上游：`Wei-Shaw/sub2api main@1a9d49e16f7a22c432b428fce4af8d731f1fa364`。
- 先撤销 `f56ed9393` 的回退，恢复此前合并的 72 个文件；再合入上游新增的 19 个提交、60 个文件。
- 保留真实合并历史，没有合并上游 dev，没有重写或强推已有提交。

此前回退只撤销文件内容，未从 Git 祖先链移除旧上游提交。直接合并最新 main 会漏掉那批修复，因此恢复与增量合并是两个独立步骤。

## 必要适配

- 将插件 KV 和账号目录装配放入正式 `ProvidePluginManager`，再从源码生成 Wire。保留本地 turn-state、遥测、每日会话根、代理地域与关闭清理接线。
- 插件账号列表及单账号解析统一 active／OpenAI OAuth／非影子资格，基础身份头沿用账号 UA 和 FedRAMP 设置。目录不分配会话根，也不触发 turn-state 学习或采集。
- 插件 status bridge 将响应绑定到原请求对象和 bridge token，防止 iframe 导航后复用 request ID 时收到旧文档的迟到结果。
- 插件明确报告请求可能已发送时，该错误优先于首响应头超时，避免被转成可以换号重放的错误。gRPC 超时取消同样保留发送边界，并通过 `Unwrap` 保持 `errors.Is` 的取消识别。普通内置上游的超时行为保持原有规则。
- Seedance 能力保持显式启用；普通账号不会自动获得该能力。插件 Redis KV 与本地加密 turn-state 表、锁及取消通知相互独立。

## 本地行为保留

暂停调度的 active OAuth 账号仍可刷新凭据，但 turn-state 采集仍要求账号可调度；凭据更新继续通过 CAS 和服务端配置代次使旧状态失效。成功交付后的响应归属写入使用独立超时，metadata-only、失败、弃用和写失败响应仍不能发布 turn-state。

保留账号配置显式意图及数据库行锁、Spark 母账号继承、独立采集代理与删除保护、导入导出的代理键映射、HTTP／WS 来源守卫与冻结快照、native transport、IPv4／IPv6 出口地域和重试隔离。本地迁移 `243_openai_codex_state.sql` 与 req fork 保持完整。

插件 Host Services、协议生成代码和 SDK 一起合入。新增 Redis KV 不需要 PostgreSQL 迁移，持久性依赖部署的 Redis 持久化配置；TTL=0 沿用上游的不过期语义。

## 验证方式

基线和合并树采用相同工具链、`GOEXPERIMENT=jsonv2`，比较默认测试、unit 测试和 lint 的具体失败项。验证只使用本地模拟上游、合成凭据及隔离 PostgreSQL／Redis，没有发送真实收费请求。

额外测试覆盖 Wire 重生成后的装配、旧 SDK 编译出的真实插件二进制、新旧 RPC 兼容及 broker 清理、插件转发与 turn-state 发布边界、采集绕过插件、账号目录资格／UA、插件 KV 隔离／TTL，以及浏览器中的账号配置、代理选择、Seedance 和插件状态。

原始日志和基线对照位于本次任务的 `.git/task-artifacts/merge-upstream-v0-2-7/`。既有失败与新增问题分别记录，不通过删除测试或放宽断言掩盖失败。

| 最终检查 | 固定基线 | 合并树 |
| --- | --- | --- |
| `go test -run '^$' ./...` 全包编译 | 通过 | 通过 |
| `go build ./...` | — | 通过 |
| `go test ./...` | 23 项顶层失败 | 相同 23 项，另有一次等待超时，见下文 |
| `go test -tags=unit ./...` | 26 项顶层失败 | 相同 26 项，完整失败集合一致 |
| `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0 ./...` | 326 条诊断 | 325 条诊断，未通过；差异说明见文末 |

最终默认全套还出现一次 `TestRecordCyberPolicyEvent_RuntimeSnapshotRefreshFailureKeepsStaleScope` 的 1 秒 `Eventually` 等待超时。该测试及对应实现相对基线未改动；同环境基线与合并树分别精确重复 10 次均通过。其余全套任务结束后，单独重跑完整 `internal/service` 包，该测试通过，19 项顶层失败与基线 service 名单一致。这次波动不能归为已在基线复现的失败，也不抹去全套原始失败记录。

## 已完成的专项验证

- Windows 与 Linux 使用 Go 1.27.0；Windows lint 使用 golangci-lint 2.13.0。工具链遵守仓库 go.mod，使用 `GOEXPERIMENT=jsonv2`。
- 前端基线 312 文件／2383 测试通过；合并树 327 文件／2467 测试通过。完整 lint、vue-tsc、生产构建均通过；构建仍有既有 Browserslist 数据和 chunk 大小提示。
- 浏览器使用本地 Vite 与模拟 API，9 条流程通过：账号开关／个人与 Team 分类／代理保存／无修改不发送配置意图／状态信息／未知套餐／Spark 继承／Seedance 显式能力／插件只读状态。无未知 API 或外部请求；只允许 sandbox 插件 iframe 中 serviceWorker、localStorage 的两条明确隔离错误，其余页面与控制台错误为零。
- 使用基线 SDK 独立编译真实旧插件二进制，验证两次启动、旧 Health 字段、新 InitHostServices 的 Unimplemented 兼容、模拟 HTTP 转发、响应体所有权、排空、broker 清理与重启；没有跳过或使用当前 SDK 冒充旧插件。
- Linux native transport、TLS、HTTP client、proxy、WS relay 竞态测试通过。HTTP／WS／状态及 repository 专项竞态运行未发现数据竞争；广泛 service 范围中大首帧长度与 API-key no-op 两项既有断言失败保留并在基线复现。
- 插件正常 Responses／透传／Chat／Messages 的流式及非流式 header／metadata 自然学习、插件失败不发布、独立采集绕过插件，以及首响应头超时交叉测试通过。
- 隔离 PostgreSQL 18.1／Redis 8.4 中，11 个账号项、2 个代理项及 10 个 OAuth CAS 子项通过；另有 9 项状态／KV 测试通过，覆盖自然请求租约持久化、并发 CAS、配置禁用与发布串行化、跨实例采集锁与所有者校验、取消通知、到期扫描、存储不可用和插件 KV 命名空间／TTL／重建客户端。上述集成测试均实际执行，无跳过。
- 真实 AccountHandler／AdminService／PostgreSQL 导出→删除源记录→导入闭环通过：业务代理和采集代理均重新分配 ID，便携键正确映射；Team 配置保留，代次重新生成，token 和运行态不迁移；导入后采集代理仍受引用删除保护。

## 验证过程中修正的测试适配

- 恢复的 Chat 角色边界测试原本要求 DeepSeek 历史 assistant 完全没有 `reasoning_content`。根据本次上游明确新增的规则，只在严格 DeepSeek 路径期待单空格占位，同时继续完整比较消息、工具和原始请求未修改的断言。
- 四项 PostgreSQL 集成测试的夹具问题已在固定基线复现：事务内再次开启事务，以及影子账号没有设置 `quota_dimension=spark`。改用独立数据库连接执行需要真实提交的测试，并补正确影子维度，保留原业务断言；精确复跑通过。
- 三项 PgDumper 测试的首轮失败来自 PowerShell 子进程缺少 `sh`；补齐 Git Bash PATH 后原测试通过，没有改变生产代码。

## 既有后端失败清单

固定基线默认测试有以下 23 项顶层失败（含子项的原始报告单独保存）：

```text
internal/repository:
  TestProxyUpdateInvalidatesBoundProbeSnapshotsAndEnqueuesOutboxAtomically
  TestProxyUpdateRollsBackWhenProbeInvalidationOutboxFails
  TestProxyUpdateSkipsProbeInvalidationForNonIdentityChange
internal/server/middleware:
  TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream
internal/service:
  TestFinalizeOpenAIOAuthResponsesRequestIsNoOpForAPIKey
  TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop
  TestForwardAsAnthropic_APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled
  TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession
  TestForwardAsAnthropic_AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID
  TestForwardAsAnthropic_InjectsPromptCacheKeyForAPIKeyMessagesDispatch
  TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels
  TestForwardAsChatCompletions_APIKeyAutoDerivesStableIsolatedPromptCacheKey
  TestForwardAsChatCompletions_APIKeyPropagatesPromptCacheKeyInResponsesBody
  TestForwardAsChatCompletions_ResponsesShapeDoesNotAutoDerivePromptCacheKey
  TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries
  TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling
  TestOpenAIGatewayService_APIKeyPassthrough_PreservesBodyAndUsesResponsesEndpoint
  TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover
  TestOpenAIGatewayService_Forward_APIKeyMissingInstructionsKeepsLargeInputRaw
  TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta
  TestOpenAIInstallationAPIKeyRemainsUnchanged
  TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite
  TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB
```

unit 标签另外有 `TestAPIContracts`、`TestApplyOAuthAccountTestRootSessionUsesDailySyncRoot`、`TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint` 三项基线失败，共 26 项。

取消 lint 输出数量限制后，基线 326 条、合并树 325 条诊断，lint 本身未通过。两条按源码文本比对显示变化的诊断仍来自同一既有 finalBody 路径；少的一条 gosec 诊断位于未修改文件，是扫描差异，不作为已修复计数。
