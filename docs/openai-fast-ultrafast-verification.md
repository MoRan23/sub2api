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
