# Supplemental service verification

Baseline: `e47c186b6`. Extracted new `Test` function declarations from the diff of all added/modified `backend/internal/service/*_test.go` files.

Already covered selectors are excluded: GPT62, UserCatalog, OpenAIGroupCatalog, stream/idle/JSON-edit tests and the billing worker's targeted selector. The whole service suite is deliberately not selected. `TestUsageProbeSkipsUnavailableSharedGrant` is excluded.

Remaining tests:

- TestTryCustomRules_CacheTTLAllocation
- TestAnthropicCacheReadZeroPreservesIndependentInput
- TestAnthropicConverterOutputCacheFields
- TestResponsesTerminalUsageMatchesAnthropicOutput
- TestAnthropicZeroCacheDeltaPromptReconciliation
- TestAntigravityRawCacheUsageStreamingLifecycle
- TestApplyCacheTTLOverrideAuthoritativeTotal
- TestCacheTTLOverrideForwardBillingBoundary
- TestNativeCacheTTLOverridePreservesRawBillingBoundary
- TestCacheTTLOverrideForwardPreservesAbsentDetails

Run from `backend`, after the serialized benchmark finishes:

```bash
export GOMAXPROCS=4 GOEXPERIMENT=greenteagc,jsonv2
selector='^(TestTryCustomRules_CacheTTLAllocation|TestAnthropicCacheReadZeroPreservesIndependentInput|TestAnthropicConverterOutputCacheFields|TestResponsesTerminalUsageMatchesAnthropicOutput|TestAnthropicZeroCacheDeltaPromptReconciliation|TestAntigravityRawCacheUsageStreamingLifecycle|TestApplyCacheTTLOverrideAuthoritativeTotal|TestCacheTTLOverrideForwardBillingBoundary|TestNativeCacheTTLOverridePreservesRawBillingBoundary|TestCacheTTLOverrideForwardPreservesAbsentDetails)$'
go test -tags unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1 -v > ../docs/validation/v0.2.23/raw/service-supplemental-unit.log 2>&1
go test -race -tags unit -p=2 -parallel=4 ./internal/service -run "$selector" -count=1 -v > ../docs/validation/v0.2.23/raw/service-supplemental-race.log 2>&1
```

Both commands use only local fixtures; neither invokes a real model or changes production configuration.
