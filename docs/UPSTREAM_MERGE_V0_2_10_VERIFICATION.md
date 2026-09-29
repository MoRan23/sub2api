# v0.2.10 合并验证记录

验证日期：2026-09-29。验收口径为无新增回归，不能把本记录理解为整个仓库测试全绿。

## 固定对照与环境

- 基线：`feb79171f2113c4bf865d0ed0f245fdbc8f25ee3`。
- 上游：`a60a29549f488a854966aaec9541abbe006cac22`。
- 在现有 WSL Ubuntu-24.04 / Docker 环境分别从固定基线和合并索引导出源码，使用独立测试树。本次重新执行基线验证，不复用之前版本的失败豁免。
- 后端：Go 1.27.0，`GOTOOLCHAIN=go1.27.0`、`GOEXPERIMENT=jsonv2`、`TYPESAFE_LIVE_TEST=0`、`GOMAXPROCS=2`，所有实际运行的 Go 测试使用 `-p 1 -count=1`，补跑测试二进制不使用结果缓存。
- lint：golangci-lint v2.13.0，使用同一 Go 1.27.0 工具链构建，基线与合并树使用同一二进制、配置和不限量诊断参数。
- 集成测试通过仓库 testcontainers 夹具创建临时 PostgreSQL 18 / Redis 8.4，不连接生产服务。
- 前端：Windows Node 24.11.1、pnpm 10.27，基线与合并树各自冻结锁文件安装。
- HTTP、SMTP 和浏览器交互使用模拟服务与合成身份；未发送真实收费请求。

## 执行命令

后端分别在两棵树的 `backend` 目录执行，`RESULT_DIR` 指向各自的本地结果目录：

```bash
export GOTOOLCHAIN=go1.27.0 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 GOMAXPROCS=2
go test -p 1 -run '^$' ./...
go test -json -p 1 -count=1 -timeout=15m ./...
go test -json -p 1 -count=1 -tags=unit -timeout=15m ./...
go build -p 1 -trimpath -o $RESULT_DIR/server ./cmd/server
go test -json -p 1 -count=1 -tags=integration -timeout=20m ./...
golangci-lint run --timeout=30m --max-issues-per-linter=0 --max-same-issues=0 --output.json.path=$RESULT_DIR/lint.json
```

合并树另外执行 `go generate ./cmd/server` 重新生成 Wire 代码。已有 panic 会使所在包剩余测试无法执行，因此对两棵树分别编译相同标签的测试二进制，列出顶层测试，分批补跑尚未完成的测试；遇到既有 panic 后继续剩余测试，保留原始失败及 panic。补跑不是跳过失败，也不把失败的全量命令改记为通过。

前端分别执行：

```bash
pnpm install --frozen-lockfile
pnpm lint:check
pnpm typecheck
pnpm test:run
pnpm build
```

## 首次全量命令结果

Go 测试数按 JSON 中的测试与子测试结果计数；此表保留首次全量命令结果，补跑汇总另列，避免将包内 panic 后未运行的测试误算为通过。

| 检查 | 固定基线 | 合并结果 | 对照 |
| --- | --- | --- | --- |
| 全包编译 | 通过 | 通过 | 无新增编译错误 |
| server 构建 | 通过 | 通过 | 无新增构建错误 |
| 默认测试 | 8,815 通过 / 4 失败 / 11 跳过 | 8,943 通过 / 4 失败 / 11 跳过 | 首轮失败与 panic 集合相同 |
| unit | 15,099 通过 / 7 失败 / 15 跳过 | 15,398 通过 / 7 失败 / 15 跳过 | 首轮失败与 panic 集合相同 |
| integration | 9,720 通过 / 23 失败 / 13 跳过 | 9,850 通过 / 23 失败 / 13 跳过 | 首轮失败与 panic 集合相同 |
| 完整 lint | 391 条诊断 | 390 条诊断 | 按文件、linter、消息及数量比较，无新增诊断 |
| 前端安装 / lint / 类型 / 生产构建 | 通过 | 通过 | 锁文件未变 |
| Vitest | 368 文件 / 2,799 测试通过 | 369 文件 / 2,818 测试通过 | 无失败 |

lint 数量减少一条不作为修复既有问题的声明；本次没有修改 lint 配置、增加忽略项或放宽检查。

## 补跑后的完整对照

两棵树合计完成 206 批补跑，原先因包内 panic 未执行的顶层测试全部取得结果或明确的 panic 证据。下表按包名与完整测试名去重，并保留任一次失败；包含子测试，三个标签之间不累加为独立测试总数。

| 标签 | 基线：通过 / 失败 / 跳过 | 合并：通过 / 失败 / 跳过 | 新增失败 / 新增 panic |
| --- | --- | --- | --- |
| 默认 | 16,325 / 81 / 13 | 16,462 / 81 / 13 | 0 / 0 |
| unit | 25,625 / 85 / 17 | 25,943 / 85 / 17 | 0 / 0 |
| integration | 17,230 / 100 / 15 | 17,369 / 100 / 15 | 0 / 0 |

每种标签均有 19 个既有 panic 测试，两边集合和 panic 消息相同。所有失败测试的归一化断言与项目堆栈证据一致，没有新增失败、无法归因的失败或新增 lint 诊断。上述既有问题不记为通过，也没有通过删除测试、降低断言或恢复退役功能掩盖它们。

本次重现的既有失败覆盖 53 个顶层测试（不同标签重复项合并，部分含失败子测试）：

```text
internal/handler
  TestOpenAIResponsesWebSocketV2PassthroughNonCyberTurnAllowsFollowup
internal/repository
  TestAccountRepoSuite
  TestCodexTelemetryPostgresRuntimeThreeSystemsAcrossRestart
  TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount
  TestUsageBillingRepositoryApply_UsesBothWalletsAndReportsCombinedBalance
  TestUsageBillingRepositoryBatchImage_CaptureReturnsRemainderToOriginalWallets
  TestUsageBillingRepositoryBatchImage_ReleaseRestoresOriginalWallets
  TestUserSubscriptionRepoSuite
internal/server
  TestAPIContracts
internal/server/middleware
  TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream
internal/service
  TestAccountConfigurationExplicitIntentIsScopedAndCopied
  TestApplyOpenAIInstallationIDForOutboundShadowUsesAuthorizedParentProfile
  TestCodexAliasFailoverMappingHonorsModelRouting
  TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback
  TestCodexContextWindowSyncRoundTrip
  TestCodexDirectImagesAccountTestAndWhitelist
  TestCodexDirectImagesRouting
  TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization
  TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization
  TestFinalizeOpenAIOAuthResponsesRequestAppliesDefaultsAndWireSnapshotForAPIKey
  TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop
  TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory
  TestForwardAlphaSearchUnauthorizedDoesNotMarkAccountError
  TestForwardAsAnthropic_APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled
  TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession
  TestForwardAsAnthropic_AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID
  TestForwardAsAnthropic_InjectsPromptCacheKeyForAPIKeyMessagesDispatch
  TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint
  TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels
  TestForwardCodexHistoryNotesBindingIsolatesGroupKeyAndLogicalSession
  TestForwardCodexHistoryNotesBindingSurvivesResponsesMigrationAndExpiry
  TestForwardCodexHistoryNotesBindsBeforeRequestAndBuffersCompleteJSON
  TestForwardCodexHistoryNotesDoesNotMarkOtherOperationsAsThreadHint
  TestForwardCodexHistoryNotesErrorsNeverSwitchAccounts
  TestForwardCodexHistoryNotesIgnoresFormerUnprefixedBinding
  TestForwardCodexHistoryNotesIncompleteResponsePreservesBinding
  TestForwardCodexHistoryNotesPermissionFailurePreservesResponsesSticky
  TestForwardCodexHistoryNotesReadSemanticFailuresDoNotFallback
  TestForwardCodexHistoryNotesReadsLegacyResponsesSeedWithoutWritingIt
  TestForwardCodexHistoryNotesRebindsOnlyWhenAccountLosesEligibility
  TestForwardCodexHistoryNotesReusesResponsesStickyBinding
  TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution
  TestForwardCodexHistoryNotesSessionMappingIsolatesAPIKeys
  TestForwardCodexHistoryNotesThreadHintFreshness
  TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity
  TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries
  TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling
  TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover
  TestOpenAIInstallationAPIKeyRemainsUnchanged
  TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite
  TestOpenAISetupTokenIdentityFlagOffAndAPIKeyStayNoOp
  TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB
  TestUsageProbeSkipsUnavailableSharedGrant
```

其中账号列表集成测试的额外记录、旧授权/身份断言、缺少 mock 方法导致的 nil-pointer panic、旧 API 契约、礼金余额与原钱包断言均在本次固定基线复现。原始逐测试证据和归一化对照保存在 `comparison.json`；随机身份、时间与地址差异不作为功能差异。

## 冲突与功能专项

精确筛选的后端冲突专项共 92 个测试及子测试通过，覆盖全局白名单、旧白名单、Cyber 仅审计、Claude 额度读取、复合分组 WS 和历史违规统计。`service` 与 `handler` 的相关专项再执行 `-race`，均通过。服务装配通过 Wire 重新生成、全包编译和 server 构建验证。

新增交叉场景：

- `TestMergeV0210CompositeWSUnchangedMappingHonorsRevokedAccountModel`：passthrough / dedicated 两路径验证同模型映射不能绕过最新账号权限撤销，拒绝轮次不写入上游。
- `TestMergeV0210GlobalAllowlistOverridesLegacyAuditWhitelist`：同时命中新旧名单；异步任务排队期间移除名单，仍保持请求判定时的仅审计语义。
- `TestMergeV0210CyberLogOnlyOverridesLegacyNotificationRecipients`：保留本地通知收件人配置，同时验证新仅审计事件不发信、不计罚。
- `TestMergeV0210HistoricalAuditEventsNeverBecomePenalties`：真实临时 PostgreSQL 中插入不同事件类型，名单移除后只统计真实阻断事件。
- 设置页新增组件回归验证共享用户选择器绑定、用户 ID 变更及保存。

上游新增及修改的流式用量、工具名重写、计费、Antigravity、Sonnet 5.5、模型白名单映射测试纳入对应全量与补跑；不以只跑冲突测试代替完整对照。

## 模拟 API 浏览器验证

使用 Playwright Chromium 加载真实 Vue 组件并拦截 API，七项流程全部通过，页面异常、控制台错误、组件解析警告均为零：

1. 糖果测试显示每账号实时模型目录失败信息，创建任务发送真实原始模型 ID。
2. Claude 重置额度只读 GET 查询、次数与过期时间展示，不调用兑换接口。
3. OpenAI 分组生成配置不包含 `model_catalog_json`。
4. Claude-Code-only 分组仅显示支持的客户端。
5. 其他适用分组仍显示自定义 Codex 模型目录配置。
6. 用户趋势从 tokens 切换到 actual_cost，API 参数同步变化。
7. 风控白名单加载已选用户、搜索添加、移除及保存，提交正确的用户 ID 字符串。

自动合并审查发现并修复旧选择器组件名；修复后重新执行完整前端 lint、类型检查、Vitest、构建和浏览器流程。

## 证据与范围

本机原始命令日志、退出码、基线源码、补跑逐批记录、浏览器报告及对比 JSON 位于 `.git/task-artifacts/merge-upstream-v0-2-10/`，不纳入提交。基线和合并结果的断言证据同时比较；仅归一化测试时间、随机 UUID 和内存地址等非确定值，保留失败测试名与业务断言。

最终提交不包含测试产物、模拟配置或凭据；既有数据库迁移及依赖锁文件保持不变。此次交付不执行生产数据操作、手动部署或标签发布。
