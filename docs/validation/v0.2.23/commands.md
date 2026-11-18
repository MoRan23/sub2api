# v0.2.23 merge verification commands

Repository: `C:/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api`.

Go commands run from `backend`, with the cached Go 1.27.4 toolchain selected by `go.mod`. Unless stated otherwise: `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`. No real model or production configuration was used. Targeted service selectors intentionally avoid `TestUsageProbeSkipsUnavailableSharedGrant` and the unrelated whole-service fixtures.

## Catalog and model compatibility

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go test -tags unit ./internal/pkg/openai ./internal/service -run 'TestGPT62|TestUserCatalog|TestOpenAIGroupCatalog' -count=1
go test -race -tags unit ./internal/pkg/openai ./internal/service -run 'TestGPT62|TestUserCatalog|TestOpenAIGroupCatalog|TestForcedServiceTier' -count=1
go test ./internal/pkg/openai -count=1
```

All passed. The first two outputs are retained in `.tmp-merge-023-catalog-tests.log` and `.tmp-merge-023-catalog-race.log` in the repository root. The last command verifies the full package after preserving `gpt-5.6-sol` as the default account test model.

Allocation-only observation for the new immutable template path (not a serialized pre/post performance comparison):

```bash
go test ./internal/pkg/openai -run 'TestGPT62' -bench '^BenchmarkGPT62Instructions$' -benchmem -count=3
```

Final output: `.tmp-merge-023-catalog-bench-final.log`; canonical lookup uses zero allocations. Root owns the serialized baseline/merged benchmarks.

## Stream, JSON editing, and first-output behavior

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
selector='Test(StreamRawChatCompletions_|RelayStalls|SetStringFieldInJSONObjects|EnsureDeepSeekChatReasoningPlaceholders|NormalizeOllamaCloudChatCompletionsRequest|GrokSafety|OpenAINativeFirstOutput|OpenAIVisible)'
go test -tags=unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1
go test -race -tags=unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1
```

Both passed (19.355 s unit, 17.751 s race). Original outputs: `C:/Users/Admin/.fastctx/jobs/j-mjav78/output.log` and `C:/Users/Admin/.fastctx/jobs/j-guhtg4/output.log`. Includes all five added JSON-object field editing tests.

## Supplemental new service tests

After comparing new `Test` declarations against the already-executed selectors, ten additional tests are selected. The exact commands and names are in [raw/service-supplemental-commands.md](raw/service-supplemental-commands.md). Both runs passed: unit 5.784 s, race 1.861 s. The verbose outputs verify all ten top-level tests actually ran: `raw/service-supplemental-unit.log` and `raw/service-supplemental-race.log`. This includes the EOF-before-output and EOF-after-output cases from upstream commit `82395b0b5` in `TestAnthropicConverterOutputCacheFields`.

The diff's modifications inside existing test functions were also reviewed, including newly added subcases. Six more existing tests were outside the prior selectors:

```bash
export GOMAXPROCS=4 GOEXPERIMENT=greenteagc,jsonv2
selector='^(TestExtractSSEUsage|TestCalculateCost_OpenAIGPT54LongContextMarkerRequiresActualCostIncrease|TestGatewayService_ParseSSEUsagePassthrough_MessageStartFallbacks|TestParseClaudeUsageFromResponseBody|TestHandleResponsesBufferedStreamingResponse_PreservesMessageStartCacheUsage|TestHandleResponsesStreamingResponse_NormalizesTerminalUsage)$'
go test -tags unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1 -v > ../docs/validation/v0.2.23/raw/service-modified-cases-unit.log 2>&1
go test -race -tags unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1 -v > ../docs/validation/v0.2.23/raw/service-modified-cases-race.log 2>&1
```

Both runs passed: unit 0.115 s, race 1.250 s. The verbose logs verify all six top-level tests and their added subcases ran (`raw/service-modified-cases-unit.log`, `raw/service-modified-cases-race.log`). These runs precede the billing worker's final local scalar-escape optimization; its affected tests are revalidated separately.

The modified heap-isolation fixture in `TestInflightEstimate_AccountMappingNoDBAndBoundedMemory` is covered by root's dedicated verification rather than repeated here.

## Billing, usage, and immutable snapshots

Actual billing worker commands (default parallelism follows `GOMAXPROCS=4`):

```bash
export GOEXPERIMENT=greenteagc,jsonv2
GOMAXPROCS=4 go test -tags unit -p 2 -run '^$' ./internal/pkg/apicompat ./internal/service
GOMAXPROCS=4 go test -tags unit -p 2 -count=1 ./internal/pkg/apicompat ./internal/service -run 'Test(.*(CacheCreation|CacheWriteTTL|AnthropicPrompt|Anthropic.*Usage|Channel.*[Pp]ricing|ChannelUltrafast|GPT62Pricing|GPT62RecordUsage|UsageBillingGroupSnapshot|UsageRecordSnapshot|CodexEngine.*(Billing|Pricing|Cache)|GrokSafety|OpenAI.*(RecordUsage|ServiceTier|FreeFast|FreeUltrafast)|ComputeCacheCreationCost|AccountStats).*)'
GOMAXPROCS=4 go test -race -tags unit -p 2 -count=1 ./internal/pkg/apicompat ./internal/service -run 'Test(.*(CacheCreation|CacheWriteTTL|AnthropicPrompt|Channel.*Multiplier|ChannelUltrafast|GPT62Pricing|GPT62RecordUsage|UsageBillingGroupSnapshot|UsageRecordSnapshot|CodexEngine.*(Billing|Pricing|Cache)|GrokSafety|OpenAI.*(RecordUsage|ServiceTier|FreeFast|FreeUltrafast)|AccountStats).*)'
GOMAXPROCS=4 go test -race -tags unit -p 2 -count=1 ./internal/service -run 'Test(CodexEngineCurrentPricingGPT62UltrafastUsesRequestInstant|ChannelMultipliersMustBePositive)$'
```

All passed. Original retained outputs: `C:/Users/Admin/.fastctx/jobs/j-1lc290/output.log` (unit), `C:/Users/Admin/.fastctx/jobs/j-d5zfn9/output.log` (race), and `C:/Users/Admin/.fastctx/jobs/j-q28cg1/output.log` (final focused race rerun). The minimal compilation output was returned directly by FastCtx.

## Frontend

Run from `frontend`; Node 24.11.1, pnpm 10.27.0:

```bash
pnpm exec vitest run src/components/keys/__tests__/UseKeyModal.spec.ts src/composables/__tests__/useModelWhitelist.spec.ts src/views/admin/__tests__/ChannelsView.ultrafastPricing.spec.ts src/components/admin/channel/__tests__/PricingEntryCard.modelDefaultPrice.spec.ts src/components/admin/channel/__tests__/PricingEntryCard.timePricing.spec.ts > ../docs/validation/v0.2.23/raw/frontend-unit.log 2>&1
pnpm run typecheck > ../docs/validation/v0.2.23/raw/frontend-typecheck.log 2>&1
pnpm exec eslint src/components/keys/UseKeyModal.vue src/components/keys/__tests__/UseKeyModal.spec.ts src/composables/useModelWhitelist.ts src/composables/__tests__/useModelWhitelist.spec.ts src/components/admin/channel/PricingEntryCard.vue src/components/admin/channel/types.ts src/views/admin/ChannelsView.vue src/views/admin/__tests__/ChannelsView.ultrafastPricing.spec.ts src/api/admin/channels.ts > ../docs/validation/v0.2.23/raw/frontend-lint-targeted.log 2>&1
pnpm run build > ../docs/validation/v0.2.23/raw/frontend-build.log 2>&1
```

All passed; Vitest: 82 tests. An earlier `pnpm run lint:check -- <the same nine paths>` also passed, but its configured script additionally scans `.`; `raw/frontend-lint.log` is retained separately from the targeted run.

## Build, repository integration, and serialized benchmarks

From `backend` (all passed):

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go test -tags unit -p 2 ./internal/pkg/openai ./internal/pkg/apicompat -count=1
go test -tags unit -p 2 ./internal/handler/admin ./internal/repository ./migrations -run 'Test(Channel|.*Ultrafast|.*Pricing|Migration252)' -count=1 -timeout=5m
go build -tags embed -p 2 -o ../.tmp-v023-server-windows.exe ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags embed -p 2 -o ../.tmp-v023-server-linux ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -tags integration -p 2 -c -o ../.tmp-v023-repository-integration ./internal/repository
```

Run that integration binary in WSL, from the same `backend` directory. Repository TestMain provisions isolated PostgreSQL 18.1 and Redis 8.4 using Testcontainers. `CI=1` makes unavailable Docker fail instead of silently skipping:

```bash
CI=1 GOMAXPROCS=4 ../.tmp-v023-repository-integration -test.run='Test(ChannelUltrafastPricing|ChannelPricing|ChannelCache|GrokSafety|BillingApply|CodexEngine).*' -test.count=1 -test.timeout=10m
```

Output: `raw/repository-integration.log`. This covers fresh migration setup, repeatable new migration against pre-existing cards, nullable multiplier CRUD/replacement and affected billing transactions.

For performance, create a separate detached worktree at `e47c186b6`; copy only `backend/internal/service/openai_raw_stream_flush_benchmark_test.go` into it. Compile each service test executable from its respective `backend` directory with identical Go 1.27.4, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`:

```bash
go test -tags unit -p 2 -c -o /absolute/output/service.test.exe ./internal/service
```

Then from the merged repository root:

```bash
python docs/validation/v0.2.23/run-comparison.py --baseline .tmp-v023-baseline-service.test.exe --merged .tmp-v023-merged-service.test.exe --output docs/validation/v0.2.23/raw/performance --working-directory backend
python docs/validation/v0.2.23/summarize-comparison.py docs/validation/v0.2.23/raw/performance > docs/validation/v0.2.23/performance.md
```

The runner refuses to overwrite logs. Use an empty output directory for reproduction. Three alternating rounds use CPU affinity `0x55`, four logical processors, identical fixtures, 2 s measurements (retained heap: one controlled GC sample per round). Other builds/tests are paused during measurements.

After the final scalar-only parser change, recompile the merged service test executable and repeat only the affected usage cases (passed, three rounds):

```bash
python docs/validation/v0.2.23/run-comparison.py --baseline .tmp-v023-baseline-service.test.exe --merged backend/.tmp-v023-final-service.test.exe --output docs/validation/v0.2.23/raw/performance-final --working-directory backend --case usage
python docs/validation/v0.2.23/summarize-comparison.py docs/validation/v0.2.23/raw/performance docs/validation/v0.2.23/raw/performance-final > docs/validation/v0.2.23/performance-final.md
```

Both embedded backend builds were rerun after that change and passed. The initial and final parser logs are retained separately.

## Final usage-parser allocation follow-up

After the initial comparison, raw Anthropic cache usage was adapted through a shared scalar merge function to avoid temporary pointer-valued DTOs. Missing prompt-cache-miss fields also avoid allocating a temporary integer. Presence, explicit-zero, cumulative-update and TTL semantics remain covered by the existing boundary tests, plus a zero-allocation raw-merge regression.

From `backend`:

```bash
export GOEXPERIMENT=greenteagc,jsonv2
selector='^Test(Anthropic(Cache|Prompt)|RawAnthropicCacheCreation|HandleNonStreamingResponse_CacheCreation|Antigravity(CacheCreation|RawCacheUsage|ForwardUpstream_CacheCreation)|OpenAI.*CacheCreation|.*CacheTTL|RewriteCacheCreation|HandleResponsesBuffered|HandleResponsesStreamingResponse_NormalizesTerminalUsage|ExtractSSEUsage|GatewayService_ParseSSEUsage|ParseClaudeUsageFromResponseBody)'
GOMAXPROCS=4 go test -tags unit -p 2 -count=1 ./internal/service -run "$selector"
GOMAXPROCS=4 go test -race -tags unit -p 2 -count=1 ./internal/service -run "$selector"
GOMAXPROCS=4 go test -c -tags unit -p 2 -o .tmp-v023-final-service.test.exe ./internal/service
```

All passed: unit 0.378 s, race 8.823 s, followed by successful executable generation. Combined original output: `C:/Users/Admin/.fastctx/jobs/j-7cqcpo/output.log`. Final benchmark executable: `backend/.tmp-v023-final-service.test.exe`.

An earlier broader selector also selected three pre-existing prompt-cache identity tests whose expected raw IDs conflict with the local mapped-ID behavior. All three failures were reproduced using the `e47c186b6` baseline executable; identity behavior was preserved. Evidence: `raw/usage-broad-selector-failures.log` and `raw/identity-baseline-failures.log`.
