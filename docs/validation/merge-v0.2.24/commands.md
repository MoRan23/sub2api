# Reproduction commands

Use Go 1.27.4 on Linux with the existing dependencies. Select that toolchain explicitly when the host default differs.

## Backend

From `backend/`:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local GOMAXPROCS=2
go version

go test -p 1 -tags=unit -count=1 -timeout=15m ./internal/service
go test -p 1 -tags=unit -count=1 ./internal/handler ./internal/server
go test -p 1 -tags=unit -count=1 ./internal/securityaudit ./internal/server/middleware ./internal/server/routes ./internal/pkg/apicompat ./internal/pkg/requestmodel ./internal/pkg/codexcontext ./internal/pkg/websearch

go test -race -p 1 -tags=unit -count=1 -timeout=5m ./internal/service -run '^(TestOpenAIWSClientValidation_.*|TestOpenAIWSHTTPBridgeInterruptFailsWithoutEndingActiveTurn|TestOpenAIRefreshCredentials.*|TestOpenAIRotation.*|TestRefreshIfNeeded_.*|TestTokenRefreshService_OpenAIRotationSurvivesAttemptDeadline|TestGrokSearchCounter.*|TestRawChatGrokSearchUsage|TestOpenAIResponseEffortConcurrentReads|TestOpenAINativeHTTPConcurrentAccountScopesRemainSeparate|TestOpenAIRequestPatches_ConcurrentImmutableInput|TestOpenAIResponsesRetryLazyHashConcurrentActivation)$'
go test -race -p 1 -tags=unit -count=1 -timeout=5m ./internal/securityaudit ./internal/pkg/requestmodel ./internal/pkg/codexcontext ./internal/server/middleware ./internal/pkg/apicompat

CGO_ENABLED=0 go build -p 1 -trimpath -o /tmp/sub2-merge024-server-linux ./cmd/server
```

The full service suite has the pre-existing failures listed in [README.md](README.md). Reproduce them against a separate export of `f987b4d0e`, rather than overwriting the current checkout. The isolated storage commands and actual container versions are in [integration.md](integration.md).

## Frontend

From `frontend/`:

```bash
pnpm exec vitest run src/components/account/__tests__/AccountUsageCell.spec.ts src/components/account/__tests__/BulkEditAccountModal.spec.ts src/components/modelPlaza/__tests__/PlazaModelPricingTable.spec.ts src/utils/__tests__/accountUsageRefresh.spec.ts src/views/admin/__tests__/AccountsView.lite.spec.ts
pnpm run build
```

`build` runs locale completeness checks, `vue-tsc -b`, and Vite. ESLint was run without fixes against the 19 changed TypeScript/Vue files. No dependency upgrade was performed.

## Performance

Build baseline and merged test binaries with `go test -c -tags=unit`. Run the selected existing benchmarks serially with `GOMAXPROCS=2`, `-test.run='^$'`, `-test.benchmem`, and `-test.benchtime=300ms`; perform three baseline/merged rounds. Four initial timing differences were repeated with `1s` samples over five alternating rounds. The complete benchmark names and per-round values are preserved with [performance.md](performance.md).

These checks use local test fixtures and isolated databases. They do not invoke live model services or deploy an application.
