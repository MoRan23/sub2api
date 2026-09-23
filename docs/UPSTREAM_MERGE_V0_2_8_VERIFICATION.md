# 上游 v0.2.8 合并：基线与合并树验证对照

> 状态：最终复验完成。相同精确排除名单下，合并树 default/unit service 诊断的失败集合与基线完全一致；无新增失败或进程 panic。无截断 lint 也与基线保持相同的 514 项。全套仍有既有失败，不宣称所有测试通过；下文保留初次失败及修复后的结果。

## 工具链与边界

- 基线：`dev@f181646f5`，主工作树 `D:/Code/sub2api`；基线验证期间源码保持未修改，验证完成后再快进 `dev`。
- Go：WSL Ubuntu-24.04 / Go 1.27.0；Windows lint：golangci-lint 2.13.0（Go 1.27.0）。
- 全部检查使用 `GOEXPERIMENT=jsonv2`、`TYPESAFE_LIVE_TEST=0`。诊断阶段限制 `GOMAXPROCS=4`，构建 `-p 2`，直接测试 binary 使用 `-test.timeout=10m`。
- 未发送真实业务、采集、授权或遥测请求；未部署。所有原始输出位于主仓库 `.git/task-artifacts/merge-upstream-v0-2-8/`，下文日志路径均相对该目录。

## 最终复验结果

| 检查 | 基线 | 最终合并树 | 新增 / 消失 |
|---|---|---|---|
| service default 诊断 | 10792 pass / 98 fail 事件 | 11374 pass / 98 fail 事件 | 0 / 0 |
| service unit 诊断 | 17979 pass / 99 fail 事件 | 18957 pass / 99 fail 事件 | 0 / 0 |
| 无截断 Windows lint | 514 项 | 514 项 | 0 / 0 |

- 两套最终诊断均在同一 22 项精确名单下走到正常测试结束，没有进程 panic；所有剩余失败事件均已在固定基线复现。
- 初次诊断的 9 个新增顶层失败族、42 个新增失败事件已全部修复并在完整重跑中消除。没有通过删除测试、跳过新增失败或放宽旧断言收口。
- 最终冻结源码重新构建 default/unit 测试二进制后执行；根代理另完成最后全包 build，日志为 `merged/final/build-after-compatibility-fixes.log`。
- 本文件只汇总所负责的基线、service 对照、Windows lint 及指定竞态复现；数据库、前端、跨模块专项竞态及其他检查由对应验证记录补充。

## 正式基线检查

| 检查 | 结果 | 说明 |
|---|---|---|
| 全包编译（`go test -run ^$ ./...`） | 通过 | exit 0 |
| server 构建 | 通过 | exit 0 |
| 全仓 default 测试 | 失败 | 4 个失败包，5 个 fail 测试事件；另有 service 后台 panic 截断包执行 |
| 全仓 unit 测试 | 失败 | 5 个失败包，8 个 fail 测试事件；service 与 middleware 同类 fixture panic |
| Windows lint 默认输出 | 失败 | 显示152项，有默认同类截断，不能作为完整诊断集合 |
| Windows lint 无截断 | 失败 | 514项既有诊断，见 `baseline/lint-unlimited.json` |

正式默认/unit suite 因既有 panic 未能覆盖包内全部后续测试。为了检查合并是否引入新失败，仅在诊断模式按实际发生过的精确测试名称逐项排除崩溃触发者；保留全部正式失败日志，诊断不等于全套通过。

## 完整 service 诊断对照（初次）

| 模式 | 基线 pass / fail 事件 | 合并 pass / fail 事件 | 新增 fail 事件 | 剩余进程 panic |
|---|---|---|---|---|
| default | 10792 / 98 | 11321 / 140 | 42 | 0 |
| unit | 17979 / 99 | 18904 / 141 | 42 | 0 |

- 默认与 unit 使用同一22项精确名单（其中 middleware 项在 service 包没有匹配对象）。两者都走到正常测试结束，失败由断言汇总产生。
- unit 相对 default 确有额外测试：基线3948项、合并4032项，因此没有省略 unit 诊断。
- unit 的 `TestSubscriptionMaintenanceQueue_TryEnqueue_PanicDoesNotKillWorker` 产生预期的已恢复 panic 日志且通过；没有将它排除。解析器只将以 `panic:` 或 `fatal error:` 开头的运行时输出视为进程崩溃。
- 失败事件含顶层测试和子测试，不等同于独立问题数。基线 default 为31个顶层失败，unit为32个；unit多出的旧失败是 `TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint`。

### 既有失败类别

- 账号配置与授权：旧快照 Extra/UA 预期、OAuth账号仓储fixture缺失、共享授权测试、Spark安装身份。
- turn-state 与观测：采集模型证据、状态来源、HTTP各转换路径与bridge、仓储异常fixture。
- 协议及会话：Messages prompt-cache/continuation、WS identity、API Key透传、响应体上限切换及图片路由。
- 基线仓储SQL mock顺序、设置接口JSON契约和WS默认提示词预期也在原始全仓suite失败。

### 初次新增失败族（已在最终完整复验中消除）

- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity`
- `TestGetOpenAICodexCanonicalUserAgentRebuildsPanelUAVersion`
- `TestNormalizeKnownOpenAICodexModelGPT6Families`
- `TestOpenAICodexVersionSyncRefreshesCompleteRuntimeIdentity`
- `TestOpenAIReferralSend`
- `TestOpenAIReferralSendGuards`
- `TestOpenAIReferralSendPreservesUnknownOutcomeWithoutRetry`
- `TestOpenAIReferralSendStopsOnQueryError`
- `TestQueryUsageCodexCredits`

已修正旧模型前缀兜底误接纳无效日期、canonical UA 提前返回绕过本地强制 TUI 规则，以及上游新增用量/邀请测试缺少唯一账号凭据仓储 fixture。UA 问题确认为合并后的行为冲突，不归因于全局状态污染。修复 picker fixture 后暴露的 HTTP 缓存命中断言，通过在构造缓存键前统一凭据快照修复；保留原筛选及缓存命中断言，并通过相关定向竞态验证。

## Lint 与竞态

- 无截断比较：基线514项，初次合并517项；新增3，消失0。按文件、诊断文本与linter匹配，忽略行列号漂移。
- 最终无截断比较：基线514项，合并514项；新增0，消失0。以下三项已使用显式类型检查及断言修复，未增加 lint 豁免。
- 默认截断输出会在相同消息的旧文件之间轮换，曾显示151项与5项位置差异；这些不是新增问题，以无截断结果为准。
- 初次新增、最终已消除的未检查type assertion：
  - `internal/repository/http_upstream_native_lifecycle_test.go:53`：Error return value is not checked (errcheck)
  - `internal/repository/http_upstream_native_lifecycle_test.go:131`：Error return value is not checked (errcheck)
  - `internal/service/account_test_cli_version_test.go:24`：Error return value is not checked (errcheck)
- `TestUsageCleanupServiceExecuteTaskDashboardRecomputeError` 的 dashboardRepoStub 计数读写竞态在原基线 `-race` 定向复现，见 `baseline/usage-cleanup-dashboard-race.json` 与原始日志。

## 关键日志索引

- 正式基线命令、退出码、完整lint项：`baseline/results.json`。
- 基线无panic最终诊断：`baseline/diagnostic-continue-21.log`、`baseline/service-unit-diagnostic-1.log`。
- 合并对应对照：`merged/service-default-final-comparison.json`、`merged/service-unit-final-comparison.json`。
- 不截断lint集合：`baseline/lint-unlimited.json`、`merged/lint-unlimited.json`。
- 最终完整对照：`merged/service-default-verified-comparison.json`、`merged/service-unit-verified-comparison.json`；最终构建记录为 `merged/service-verified-builds.json`。
- 最终不截断 lint：`merged/lint-verified-unlimited.json` 与同名 `.log`。

## 附录 A：精确已证实崩溃排除名单

- `TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream`
- `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback`
- `TestCodexContextWindowSyncRoundTrip`
- `TestCodexDirectImagesAccountTestAndWhitelist`
- `TestFetchOpenAIAccountModelsOAuthLabelsLocalImageModelsLikeUpstream`
- `TestFetchOpenAIAccountModelsOAuthPopulatesPickerFields`
- `TestFetchOpenAIAccountModelsOAuthRespectsImageAllowlist`
- `TestForwardAlphaSearchUnauthorizedDoesNotMarkAccountError`
- `TestForwardCodexHistoryNotesBindingIsolatesGroupKeyAndLogicalSession`
- `TestForwardCodexHistoryNotesBindingSurvivesResponsesMigrationAndExpiry`
- `TestForwardCodexHistoryNotesBindsBeforeRequestAndBuffersCompleteJSON`
- `TestForwardCodexHistoryNotesDoesNotMarkOtherOperationsAsThreadHint`
- `TestForwardCodexHistoryNotesErrorsNeverSwitchAccounts`
- `TestForwardCodexHistoryNotesIgnoresFormerUnprefixedBinding`
- `TestForwardCodexHistoryNotesIncompleteResponsePreservesBinding`
- `TestForwardCodexHistoryNotesPermissionFailurePreservesResponsesSticky`
- `TestForwardCodexHistoryNotesReadSemanticFailuresDoNotFallback`
- `TestForwardCodexHistoryNotesReadsLegacyResponsesSeedWithoutWritingIt`
- `TestForwardCodexHistoryNotesRebindsOnlyWhenAccountLosesEligibility`
- `TestForwardCodexHistoryNotesReusesResponsesStickyBinding`
- `TestForwardCodexHistoryNotesSessionMappingIsolatesAPIKeys`
- `TestForwardCodexHistoryNotesThreadHintFreshness`

## 附录 B：基线完整 service 失败事件（default/unit并集）

- `TestAccountConfigurationExplicitIntentIsScopedAndCopied`
- `TestAccountConfigurationPreservesLatestAfterStaleSnapshot`
- `TestApplyOpenAIInstallationIDForOutboundShadowUsesAuthorizedParentProfile`
- `TestCodexAliasFailoverMappingHonorsModelRouting`
- `TestCodexAliasFailoverMappingHonorsModelRouting/routing_rule_resolves_the_alias`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence/conflicting`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence/event_fallback`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence/har_astra_luna_312`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence/not_reported_allowed`
- `TestCodexCollectorModelEvidenceAndFailurePrecedence/target_mismatch`
- `TestCodexDirectImagesRouting`
- `TestCodexDirectImagesRouting/gpt-image-1.5`
- `TestCodexDirectImagesRouting/gpt-image-2`
- `TestCodexDirectImagesRouting/gpt-image-2.5-flare`
- `TestCodexDirectImagesRouting/gpt-image-2.5-flare-2026-09-08`
- `TestCodexDirectImagesRouting/gpt-image-2.5-sunburst`
- `TestCodexDirectImagesRouting/gpt-image-2.5-sunburst-2026-09-08`
- `TestCodexStateEnabledObservationCollectorOriginFromActualResponse`
- `TestCodexStateEnabledObservationCollectorOriginFromActualResponse/failed_metadata_extended`
- `TestCodexStateEnabledObservationCollectorOriginFromActualResponse/header_target`
- `TestCodexStateEnabledObservationCollectorOriginFromActualResponse/metadata_extended`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/chat/stream=false/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/chat/stream=false/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/chat/stream=true/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/chat/stream=true/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/messages/stream=false/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/messages/stream=false/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/messages/stream=true/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/messages/stream=true/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/passthrough/stream=false/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/passthrough/stream=false/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/passthrough/stream=true/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/passthrough/stream=true/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/responses/stream=false/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/responses/stream=false/metadata`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/responses/stream=true/header`
- `TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff/responses/stream=true/metadata`
- `TestCodexStateEnabledObservationRealBeginFallback`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_error/chat`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_error/messages`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_error/passthrough`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_error/responses`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_nil/chat`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_nil/messages`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_nil/passthrough`
- `TestCodexStateEnabledObservationRealBeginFallback/begin_nil/responses`
- `TestCodexStateEnabledObservationRealBeginFallback/missing_generation/chat`
- `TestCodexStateEnabledObservationRealBeginFallback/missing_generation/messages`
- `TestCodexStateEnabledObservationRealBeginFallback/missing_generation/passthrough`
- `TestCodexStateEnabledObservationRealBeginFallback/missing_generation/responses`
- `TestCodexStateEnabledObservationWSToHTTPBridgeFingerprintOff`
- `TestCodexTurnStateCollectorStopsWhenProfileStorageFails`
- `TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization`
- `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization`
- `TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop`
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory`
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory/compaction_trigger`
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory/explicit_compaction`
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory/ordinary_turn`
- `TestForwardAsAnthropic_APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled`
- `TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession`
- `TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession/previous_response_id_is_not_available_for_this_user`
- `TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession/previous_response_id_requires_an_OpenAI_API-key_account_for_HTTP_requests`
- `TestForwardAsAnthropic_AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID`
- `TestForwardAsAnthropic_InjectsPromptCacheKeyForAPIKeyMessagesDispatch`
- `TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint`
- `TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/account_disabled`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/account_no_longer_schedulable`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/still_paid_and_active`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/subscription_became_free`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/subscription_expired`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/subscription_expiry_disappeared`
- `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution/without_refresh_provider_uses_captured_account`
- `TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity`
- `TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries`
- `TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling`
- `TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling/allowed_group_keeps_image_billing`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_500`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_502`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_503`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_504`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_520`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_521`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_522`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_523`
- `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover/status_524`
- `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta`
- `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta/api_key_explicit_beta_remains_caller_controlled`
- `TestOpenAIInstallationAPIKeyRemainsUnchanged`
- `TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite`
- `TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite/api_key_passthrough`
- `TestOpenAISetupTokenIdentityFlagOffAndAPIKeyStayNoOp`
- `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB`
- `TestUsageProbeSkipsUnavailableSharedGrant`

## 附录 C：初次合并新增失败事件（default/unit一致）

- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/default/recognized_trailer`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/default/valid_suffix`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/default/valid_tab`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_manual/recognized_trailer`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_manual/valid_suffix`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_manual/valid_tab`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_versions/recognized_trailer`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_versions/valid_suffix`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/invalid_versions/valid_tab`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/manual/recognized_trailer`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/manual/valid_suffix`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/manual/valid_tab`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/synced/recognized_trailer`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/synced/valid_suffix`
- `TestGetOpenAICodexCanonicalUserAgentOutboundIdentity/synced/valid_tab`
- `TestGetOpenAICodexCanonicalUserAgentRebuildsPanelUAVersion`
- `TestGetOpenAICodexCanonicalUserAgentRebuildsPanelUAVersion/鑷�瀹氫箟鎸囩汗鍘熸牱淇濈暀`
- `TestGetOpenAICodexCanonicalUserAgentRebuildsPanelUAVersion/闄堟棫闈㈡澘_UA_璺熼殢鐢熸晥鐗堟湰`
- `TestGetOpenAICodexCanonicalUserAgentRebuildsPanelUAVersion/闈㈡澘鐗堟湰鍙疯�嗗啓浼樺厛`
- `TestNormalizeKnownOpenAICodexModelGPT6Families`
- `TestOpenAICodexVersionSyncRefreshesCompleteRuntimeIdentity`
- `TestOpenAIReferralSend`
- `TestOpenAIReferralSend/plus`
- `TestOpenAIReferralSend/self_serve_business_usage_based`
- `TestOpenAIReferralSend/team`
- `TestOpenAIReferralSendGuards`
- `TestOpenAIReferralSendGuards/exhausted`
- `TestOpenAIReferralSendGuards/ineligible`
- `TestOpenAIReferralSendGuards/no_consent`
- `TestOpenAIReferralSendGuards/reward_exhausted`
- `TestOpenAIReferralSendGuards/unknown_capacity`
- `TestOpenAIReferralSendGuards/unknown_reward_capacity`
- `TestOpenAIReferralSendPreservesUnknownOutcomeWithoutRetry`
- `TestOpenAIReferralSendStopsOnQueryError`
- `TestQueryUsageCodexCredits`
- `TestQueryUsageCodexCredits/absent`
- `TestQueryUsageCodexCredits/decimal_balance`
- `TestQueryUsageCodexCredits/hidden_balance`
- `TestQueryUsageCodexCredits/null`
- `TestQueryUsageCodexCredits/unlimited`
- `TestQueryUsageCodexCredits/zero`
