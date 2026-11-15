# Fast / Ultrafast 验证记录 / Verification record

基线 / Baseline: `6ffb77fad`. 验证日期 / Date: 2026-11-13.

## 环境 / Environment

- Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`.
- Windows amd64：定向 unit、race、后端编译与前端检查。
- WSL Ubuntu 24.04：Ent 完整生成、账户测试、隔离 PostgreSQL 18.1 / Redis 8.4 集成测试。数据库由 Testcontainers 创建和清理。
- Node 24.11.1、pnpm 10.27；未升级依赖。
- 仅模拟上游；未调用真实模型、修改 Engine 或生产配置、部署服务。

## 结果 / Results

| 范围 / Area | 结果 / Result |
| --- | --- |
| 分组 service、管理 handler、DTO | 定向测试通过；默认值、互斥、部分更新及复制 |
| 账户 service、repository | 新增定向 unit 全部通过；布尔校验、管理员设置保护、批量设置、共享凭据业务配置 |
| 请求策略、目录与指纹 | 定向测试和 race 通过；HTTP/WS、能力隔离、304、降级、超时、Guardian、全局策略、正文复用 |
| 计费 | 40 个顶级测试 / 217 个含子例 PASS；race 18 个顶级测试 / 56 个含子例 PASS |
| 隔离数据库 | 6 项通过；账户及分组并发互斥、旧快照保护、鉴权投影、迁移幂等 |
| 账户创建／编辑／批量前端 | 3 个文件、275 项测试通过 |
| 分组及设置辅助函数 | 分组目录 10 项、复制 14 项、Composite 1 项、辅助函数 3 项通过 |
| 指纹前端 | 64 项测试通过 |
| 静态检查和构建 | 前端类型检查、定向 ESLint、生产构建和最终后端编译通过；`git diff --check` 通过 |

数据库用例：

- `TestAccountRepoSuite/TestAccountFastSettingsAdminIntentAndStaleWriters`
- `TestAccountRepoSuite/TestAccountFastSettingsPatchRechecksConcurrentForceChange`
- `TestAPIKeyRepoSuite/TestUltrafastAuthSelectPreservesTrustedGroupPolicy`
- `TestGroupRepoSuite/TestUltrafastDefaultsPartialAndStaleUpdate`
- `TestGroupUltrafastConcurrentForceUpdates`
- `TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate`

## 可复现命令 / Reproduction

在 `backend/`，设置上述 GOEXPERIMENT 后执行：

```sh
go test ./internal/service -run 'TestForcedServiceTier|TestApplyOpenAIFastPolicyToBody|TestWSResponseCreate|TestFetchCodexModelsManifestAPIKey|TestOpenAIGuardian|TestFingerprintServiceTierPolicy|TestFingerprintObservation' -bench '^BenchmarkOpenAIServiceTier' -benchmem -benchtime=200ms -count=1

go test -race ./internal/service -run 'TestForcedServiceTier|TestApplyOpenAIFastPolicyToBody|TestWSResponseCreate|TestFingerprintServiceTierPolicy|TestModelsBackgroundRefreshRejectsReboundAuthorization|TestModelsCapabilitiesAreIsolatedByOSAndAuthorization|TestFetchCodexModelsManifestAPIKeyConcurrentRequestsShareRefresh|TestFetchCodexModelsManifestAPIKeySharedRefreshSurvivesCallerCancellation|TestFetchCodexModelsManifestAPIKeyCacheKeyIsolatesRequestIdentity|TestFetchOpenAIAccountModelsOAuthCacheUsesOSAndAuthorization' -count=1

go test -tags unit ./internal/service -run 'Test(OpenAIFreeServiceTierPolicy|OpenAIRecordUsageFreeAcceleration|CodexEngineFreeUltrafast|EffectiveOpenAIFreeServiceTiers|OpenAIGatewayServiceRecordUsage_FreeOpenAIFast|UsageRecordSnapshot|UsageBilling|CodexEngineFrozenPricingLongContext|OpenAI.*LongContext)' -count=1 -v

go test -tags integration ./internal/repository -run 'TestAccountRepoSuite/TestAccountFastSettings|TestAPIKeyRepoSuite/TestUltrafast|TestGroupRepoSuite/TestUltrafast|TestGroupUltrafastConcurrentForceUpdates|TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate' -count=1 -v

go build ./cmd/server
```

在 `frontend/` 执行相关 Vitest 文件、`pnpm run typecheck`、`pnpm run build`，并对变更的 Vue、TS 和测试文件运行 ESLint。

## 分配观测 / Allocation observations

最终实现本地单轮 microbenchmark，200ms/项：

| 场景 / Scenario | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| 不可变能力索引读取 / capability index lookup | 8.016 | 0 | 0 |
| 400 KiB 原始目录缓存命中 / cached catalog lookup | 521.6 | 392 | 6 |
| 2 MiB 请求已经使用目标档位 / unchanged body | 161.1 | 48 | 3 |

后两项同时断言不返回目录正文副本、未变化请求保持同一正文切片。以上是本机合成测试数据，未执行生产压测。

## 既有失败与检查边界 / Existing failures and scope

- `TestAccountConfigurationExplicitIntentIsScopedAndCopied` 的旧 UA 断言失败：期望 Ubuntu，实际保持 `old environment`。用 Go overlay 替换为 HEAD 原版 `account_configuration_intent.go` 后复现同一失败；本次未修改其测试、UA 逻辑或断言。新增账户测试单独重跑通过。
- Windows Ent 生成遇到文件映射占用后，恢复受影响生成文件，改用 WSL 完整生成成功；最终后端编译通过。
- 前端生产构建成功，仍输出 Browserslist 数据过旧、Node shell 调用弃用及既有大 chunk / 混合导入告警。
- 运行的是受影响范围测试，未执行全仓库测试。远端 Actions 未运行；本次无部署验证。

Existing UA assertion failure was reproduced against the original HEAD implementation and retained unchanged. Targeted new tests, race checks, isolated database tests, frontend checks and builds passed. Microbenchmarks are one local synthetic run; no production load test or deployment was performed.

## 强制 Ultrafast 回退 / Forced Ultrafast fallback (2026-11-15)

基线 / Baseline: `0a723eec5`.

- 账号或分组开启强制 Ultrafast 时，有效上游目录未声明最终映射模型支持 Ultrafast，则发送 `service_tier="priority"`。无需同时开启强制 Fast；两个强制开关继续互斥。
- 目录不可用、无效、过期或降级时仍保留客户端档位；不把查询失败作为不支持的证据。账号优先级、Guardian 和全局 Fast 策略继续生效。
- 指纹保留强制意图 `ultrafast`、能力结果 `unsupported` 及实际发送值 `priority`，不修改旧观测记录。回退后的费用按实际档位处理，对应免费开关为免费 Fast。
- 沿用现有能力缓存与定点修改；跨 HTTP 重试及 WS 回合按当前模型决策，原始正文不变。原本已经是 `priority` 的正文复用原切片，不增加模型请求或目录重复查询。

When account or group policy forces Ultrafast but a valid upstream catalog does not declare it for the final mapped model, the request now uses `priority`. Force Fast need not be enabled, and the force switches remain mutually exclusive. Unavailable, invalid, expired or degraded catalogs retain the client tier. Account precedence, Guardian and global Fast policy remain authoritative. Fingerprint records retain the Ultrafast intent, unsupported capability and actual priority tier; historical records are unchanged. Billing follows the effective tier, including the Free Fast option. Existing capability caching and immutable request ownership are reused.

验证范围 / Verification scope:

- OAuth 原生 Responses 的真实出站正文及结果档位。
- 普通 API Key、Engine、账号／分组优先级、历史冲突配置。
- HTTP／WS 不同初始档位、映射模型与后续回合、原文不变及缓存只查询一次。
- 目录不可用保留原值、全局过滤／拦截、Guardian、有效免费档位。
- 共用开关的中英文提示、互斥行为及 i18n 消息编译。

全部请求使用本地模拟上游，无数据库字段或持久化逻辑变更；本次不运行数据库迁移测试，不修改生产配置或部署。前端只修改两条翻译，验证相关消息编译、完整性、交互辅助测试及定向 ESLint。

All upstream requests are simulated locally. No schema or persistence behavior changed, so migration tests are outside this patch. Only the two translated hints change in the frontend; related message-compilation, integrity, toggle-helper tests and targeted ESLint cover them. No production configuration or deployment is involved.

结果 / Results：请求策略定向单测通过（5.355s），请求／WS／指纹／Guardian 竞态测试通过（9.410s），免费档位计费回归通过（5.802s），后端 `go build ./...` 通过。前端 3 个文件共 9 项测试及两份 locale 的定向 ESLint 通过。未运行远端 CI。

Targeted policy tests, request/WS/fingerprint/Guardian race tests, free-tier billing regression tests and the backend build passed. All nine tests across the three frontend files and targeted locale ESLint passed. Remote CI was not run.

```sh
# backend; Go 1.27.3
export GOEXPERIMENT=greenteagc,jsonv2
go test ./internal/service -run 'Test(ForcedServiceTier|FingerprintServiceTierPolicy|OpenAIGatewayService_.*Fast|OpenAIFast)' -count=1
go test -race ./internal/service -run 'Test(ForcedServiceTier|FingerprintServiceTierPolicy|ApplyOpenAIFastPolicyToBody|WSResponseCreate|OpenAIGuardian)' -count=1
go test -tags=unit ./internal/service -run 'Test(OpenAIFreeServiceTierPolicy|OpenAIRecordUsageFreeAcceleration|CodexEngineFreeUltrafast|EffectiveOpenAIFreeServiceTiers)' -count=1
go build ./...
```

```sh
# frontend
pnpm exec vitest run src/components/account/__tests__/openaiServiceTierSettings.spec.ts src/i18n/__tests__/openaiFastPolicyLocales.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm exec eslint src/i18n/locales/zh/admin/overview.ts src/i18n/locales/en/admin/overview.ts
```
