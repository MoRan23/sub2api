# v0.2.24 merge validation / 合并验证

## Scope and environment

- Baseline: `f987b4d0e3ae836b81184946577eac6057c6a887`.
- Upstream: `328db26f450edbdf9f8d4344a3ea903ac6396842` (`v0.2.24`).
- Verified code merge: `4bae9e380`; subsequent documentation does not change tested code.
- Linux toolchain: Go 1.27.4, `GOEXPERIMENT=greenteagc,jsonv2`, WSL Ubuntu 24.04. The baseline was exported from the exact Git commit and built with the same toolchain.
- Integration: disposable PostgreSQL 18.1 / Redis 8.4 containers; `CI=1` prevents silent Docker skips. No production database, real model endpoint or Engine checkout was used.
- Frontend: existing pnpm lockfile and installed dependencies; no dependency or schema changes.

## Completed checks

| Check | Result | Local run reference |
| --- | --- | --- |
| Account usage, bulk edits, pricing, refresh keys and account list | 5 files, 234 tests passed | `j-e4vwms` |
| Frontend type checking and production build, including i18n checks | Passed | `j-gaoykz` |
| ESLint for the 19 changed TypeScript / Vue files | Passed | `j-867lzi` |
| Complete handler and server unit suites after compatibility fixes | Passed, 66.500 s / 4.511 s | `j-gp4gl0` |
| Audit, routes, middleware, request model, page context, API compatibility and search packages | Passed; affected packages rechecked after fixes | `j-nvw6lz`, `j-v40yyl`, `j-g4i5lx` |
| Buffered hosted outputs, Kimi dynamic tools / uint64 effort, raw tool names, WS validation and HTTP bridge continuation | Passed | `j-fhfuoz` |
| Audio capability catalogs and official model-list contract | Passed | `j-c1vcnf` |
| AlphaSearch authorization containment and readable snapshot classification | Passed | `j-d7r8xv` |
| Repository test fixtures for existing CAS / credential protection | Passed | `j-oyav4b` |
| Migrations / restart, Grok atomic settlement / rollback, wallets, finite and permanent subscriptions, weighted metrics, scheduler and post-commit quotas | 12 top-level tests, 22 including subtests passed | `j-c6v55z` |
| Account CAS, shared authorization, revocation and reauthorization integration | 16 main cases plus field-level subcases passed | `j-d5gmwh` |
| Slim catalog and shadow projections, including protocol scalar and ungrouped accounts | Passed against isolated PostgreSQL | `j-3xiwf5` |
| Wire generation | Completed from merged injection sources | Account compatibility work |
| Race: OAuth refresh/CAS, WS validation and bridge interrupts, Grok counters, immutable request data and retries | Passed, service 1.417 s | `j-noxlx6` |
| Race: securityaudit, requestmodel, codexcontext, server middleware and apicompat | All passed | `j-noxlx6` |
| Final Linux production build after all compatibility fixes | Passed, `CGO_ENABLED=0` | [Final check log](raw/final-checks.log) |
| Performance: 76 paired cases plus 8 new ingress checks | Three rounds and five-round confirmation; no stable timing regression above 5% | [performance.md](performance.md) |

Frontend build warnings about the browser compatibility dataset, existing chunk sizes and dynamic imports remain unchanged. Lockfiles were not updated to suppress them. A read-only GitHub Actions permissions query on 2026-11-20 returned `enabled: false`; remote CI is disabled and was not run.

## Full service suite and pre-existing failures

The full service unit suite (`j-vm3cau`, 392.691 s) initially reported 39 failing top-level tests. The same test names were run against the exact pre-merge baseline (`j-ykfotx`), reproducing 27 failures. All 12 merge-specific failures were fixed and their relevant regressions passed. The existing snapshot denylist test was also corrected to classify the already-present non-sensitive scheduling revision.

The following 26 baseline failures remain outside this merge's scope. They are not reported as passing; no production behavior was changed merely to satisfy older assertions:

```text
TestAccountConfigurationExplicitIntentIsScopedAndCopied
TestPersistWhitelistAuditLog_CreateBeforeEmailUsesSourceIDAndPatchesStatus
TestGeminiForwardAsChatCompletions_NonStreamForwardsEffortWithoutThoughts
TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown
TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity
TestUsageProbeSkipsUnavailableSharedGrant
TestBuildCodexCLIUserAgent
TestCodexAliasFailoverMappingHonorsModelRouting
TestConvertOpenAIModelListToCodexManifestLeavesUnsupportedBodiesUnchanged
TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization
TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization
TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession
TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint
TestGPT61TerraBaseInstructionsUseOfficialTemplate
TestApplyOpenAIInstallationIDForOutboundShadowUsesAuthorizedParentProfile
TestOpenAIInstallationAPIKeyRemainsUnchanged
TestGPT55RetirementProjectionUsesPublicNames
TestCaptureOpenAIOAuthIdentityIgnoresClientContextWindowID
TestApplyOpenAIOAuthIdentityPlanSafePairPreservesRecognizedClient
TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite
TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop
TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory
TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels
TestForwardAsAnthropic_InjectsPromptCacheKeyForAPIKeyMessagesDispatch
TestForwardAsAnthropic_AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID
TestForwardAsAnthropic_APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled
```

The full service suite was not repeatedly rerun after each isolated fixture fix; the affected tests and packages were rerun. Local run references identify the execution logs retained by the development environment.

## 中文说明

本次验证把新引入的问题与合并前已有失败分开记录。新增协议、目录、授权和测试夹具问题修复后均已通过对应回归；完整 service 套件仍有上述 26 项可在原始基线复现的失败。数据库验证仅使用隔离容器，覆盖完整迁移、重复启动、原子结算、永久额度和授权竞争。

关键并发路径和相关包的 race、最终 Linux 生产构建均已通过。三轮性能对比覆盖 84 个子例，四项初始差异延长交替采样后均未出现稳定超过 5% 的回退；8 MiB 请求解析耗时降低约 56%，namespace 修改所需正文分配减半。完整结果、分配量及峰值 RSS 见[性能报告](performance.md)，执行方法见[命令记录](commands.md)。

未执行全项目无筛选 race 或真实上游端到端探测；未修改线上配置或部署应用。远端 Actions 当前关闭。
