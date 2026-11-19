# Reproduction commands / 复现命令

在仓库根目录开始。Go 命令进入 `backend/` 后执行，使用当前模块选定的 Go **1.27.4** 和 `GOEXPERIMENT=greenteagc,jsonv2`。下列命令是本轮已执行命令或其等价复现形式；测试仅连接本地模拟服务和隔离容器。

Start at the repository root. Backend commands use the module-selected Go **1.27.4** and the existing experiments. Tests target local fixtures and isolated containers.

## 类型化推理强度 / Typed reasoning effort

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run 'TestOpenAIEffort|TestOpenAIResponseEffort|TestApplyOpenAIReasoningEffortPolicy|TestNormalizeReasoningEffortMappings' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^BenchmarkOpenAIEffort(ConfigurationLargeInput|PreparedLargeInput)$' -benchmem -benchtime=200ms -count=3
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^BenchmarkOpenAIEffortConfigurationPrefilter/.*/alternating_pair$' -benchmem -benchtime=1s -count=3
```

首条命令是专题首轮检查；后续交叉修复由最终联合复测覆盖。后两条分别验证当前实现的大正文固定分配，以及同迭代交替执行的预过滤／结构扫描耗时。早期分开批次的性能日志只保留作排查记录，结论以最终配对数据为准。

## 协议桥接 / Protocol bridge

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/pkg/apicompat
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/pkg/apicompat
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$'
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run 'Test(ResponsesBridgeContinuationIsClientErrorBeforeUpstream|DeriveCompatPromptCacheKey|DeriveAnthropicCompatPromptCacheKey|ShouldAutoInjectPromptCacheKeyForCompat)'
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/pkg/apicompat -run '^$' -bench 'Benchmark(Codex0170ExistingChatBridge|ResponsesInlineAudioPayload|BridgeEffectiveReasoningLargeAudio)$' -benchmem -count=3
```

## Guardian、身份及连接池 / Guardian, identity and pool

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run 'Test(OpenAIGuardian|OpenAIThreadOriginator|OpenAIWSParentPinCompatibility|CodexSafePair|EnforceCodexIdentity|OpenAIWSConnPool)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/service -run 'Test(OpenAIGuardianRouting|OpenAIGuardianPinPhysical|OpenAIThreadOriginator|OpenAIWSParentPinCompatibility|CodexSafePair)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service ./internal/handler -run 'Test(OpenAIForwardResultFreeze|StampOpenAIRequestedReasoningEffortKeepsResolvedUnknown|OpenAIGuardian|OpenAIThreadOriginator|Codex0170HTTP)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^BenchmarkOpenAIGuardianRoutingDeltaObservation$' -benchmem -count=3 -benchtime=300ms
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/service -run 'Test(OpenAIGuardianRouting|OpenAIGuardianPinPhysical|OpenAIWSCtxPoolLaterTurn429)' -count=1
```

## 遥测 / Telemetry

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run 'TestCodex(ReasoningReplay|Telemetry(Stream|HTTP|Coverage|ClientOnly|Metrics|Skills|Connect)|ModelEvidence)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/service -run 'TestCodex(ReasoningReplay|Telemetry(Stream|HTTP|Coverage|ClientOnly|Metrics|Connect)|ModelEvidence)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^BenchmarkCodexReasoningReplaySummary$' -benchmem -count=3 -benchtime=300ms
```

## 隔离 PostgreSQL / Redis

本轮在 WSL Ubuntu 24.04 运行，使用 Linux Go 1.27.4。Testcontainers 自动创建并清理临时 PostgreSQL 18.1 和 Redis 8.4；不提供生产 DSN。

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local go test -tags=integration ./internal/repository -run '^Test(CodexTelemetry(0169|0170)|Migration306|GatewayCacheOpenAIResponseEffort)' -count=1 -timeout=8m -v
GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local go build -o /tmp/sub2api-codex0170 ./cmd/server
```

本轮实际 Linux Go 二进制位于 `/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin/go`。WSL 初次下载遇到 TLS 失败后，通过 Windows `go mod download -json golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64` 取得已校验工具链；未关闭 TLS 或模块校验。其它环境可以直接使用安装好的相同 Go 版本。

## 前端 / Frontend

以下合并列出同一轮分批执行的八个测试文件；原始日志记录每次子集与后续复测。合并命令用于复现当前完整范围。

```bash
cd frontend
pnpm exec vitest run \
  src/views/admin/__tests__/groupsReasoningEffort.spec.ts \
  src/components/admin/group/__tests__/ReasoningEffortPolicyFields.spec.ts \
  src/utils/__tests__/formatReasoningEffort.spec.ts \
  src/api/__tests__/admin.codexTelemetry.spec.ts \
  src/components/admin/account/__tests__/OpenAIResponseEvidenceDetails.spec.ts \
  src/views/admin/__tests__/CodexTelemetryObservations.spec.ts \
  src/components/admin/usage/__tests__/UsageTable.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm run typecheck
pnpm run build
git diff --name-only -z --relative -- src | xargs -0 pnpm exec eslint
```

最后一个命令对应本轮未提交改动的定向 lint；提交后复现时请显式列出本次变更的前端源文件，不将空列表解释为本轮已复测。

## 基线与最终对照 / Baseline and final comparison

在独立的 `db4705f5a` 基线 checkout 和当前版本运行相同合成基准、工具链及并发设置。`BenchmarkCodex0170ExistingChatBridge` 的基准 fixture 同样加入临时基线目录，业务源码仍为基线。

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^BenchmarkCodex0169Compatibility$' -benchmem -count=3
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^$' -bench '^(BenchmarkCodex0169Compatibility|BenchmarkOpenAIGuardianRoutingDeltaObservation)$' -benchmem -count=3 -benchtime=300ms
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/pkg/apicompat -run '^$' -bench '^BenchmarkCodex0170ExistingChatBridge$' -benchmem -count=3
```

Windows 既有失败的独立复现：

```bash
# 在 db4705f5a 的独立基线 checkout/backend 中执行
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -run '^TestUsageExportDownloadLeaseCloseAndCancellation$' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service -count=1 -run 'TestCodexAliasFailoverMappingHonorsModelRouting|TestConvertOpenAIModelListToCodexManifestLeavesUnsupportedBodiesUnchanged|TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization|TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization|TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels' -v
```

## 最终联合复测与构建 / Final combined tests and build

下列 `-skip` 仅列出上面已在未修改基线复现的五项失败；Windows 文件下载租约测试不在此定向 `-run` 范围内。The skip list contains only the five failures reproduced on the unchanged baseline; the Windows download-lease test is outside this selected run.

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service ./internal/handler ./internal/handler/dto ./internal/pkg/apicompat \
  -run 'Codex|ReasoningEffort|ReasoningReplay|OpenAIWS|OpenAIGuardian|OpenAIThreadOriginator|OpenAIResponseEffort|OpenAIEffort|ResponsesBridgeContinuation|UpstreamModelCatalog|PassthroughUsageMeta|PassthroughUsageLogInfers|UsageExportPrecision|UsageExportRow|AdminOpaque' \
  -skip '^(TestCodexAliasFailoverMappingHonorsModelRouting|TestConvertOpenAIModelListToCodexManifestLeavesUnsupportedBodiesUnchanged|TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization|TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization|TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels)$' \
  -count=1 -timeout=8m
GOEXPERIMENT=greenteagc,jsonv2 go build -o /c/Users/Admin/AppData/Local/Temp/sub2api-codex0170.exe ./cmd/server
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/service ./internal/handler \
  -run 'Test(OpenAIEffort|OpenAIResponseEffort|Codex0170HTTP|ProxyOpenAIWSHTTPBridgeTurn|PassthroughUsageMeta|OpenAIResponsesWebSocket_PassthroughUsageLogInfers)' \
  -count=1 -timeout=5m
```
