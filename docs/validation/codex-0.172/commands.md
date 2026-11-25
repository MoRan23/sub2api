# Verification commands / 验证命令

Backend checks ran in WSL from `backend/`, using:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local GOMAXPROCS=2
go_binary=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin/go

"$go_binary" test -p 1 ./internal/service \
  -run '^(TestCodex0172.*|TestCodex0171.*|TestCodexTelemetry.*|TestBuildCodexCLIUserAgent|TestCodexCanonicalUserAgentFollowsResolver|TestFinalizeOpenAIOAuthResponsesRequest.*|TestCloneFingerprintObservationHeaders.*|TestOpenAIWSConnPool_AcquireReusesSameStableIdentity|TestCodexCatalogWebSearchProvider.*|TestOpenAIGroupCatalogOfficialDescriptorsRemainComplete|TestResponsesBridgeContinuationIsClientErrorBeforeUpstream)$' \
  -count=1 -timeout=6m -v

"$go_binary" test -p 1 -race ./internal/service \
  -run '^(TestCodex0172.*|TestCodex0171.*Trace.*|TestCodexTelemetry(Coverage.*|ClientOnly.*|DisableCancels.*|NativeHTTPPreserves.*)|TestFinalizeOpenAIOAuthResponsesRequest.*|TestOpenAIWSNative.*|TestOpenAIWSHTTPBridge.*|TestCodexCatalogWebSearchProvider.*)$' \
  -count=1 -timeout=6m -v

"$go_binary" test -p 1 -race ./internal/pkg/apicompat -count=1 -timeout=5m
CI=true "$go_binary" test -p 1 -tags=integration ./internal/repository \
  -run '^TestCodexWebSearchProviderMetadataPostgresRedisRoundTrip$' -count=1 -v -timeout=8m
"$go_binary" test -p 1 -tags=unit ./internal/server \
  -run 'TestAPIContracts/GET_/api/v1/admin/settings' -count=1 -v -timeout=5m
CGO_ENABLED=0 "$go_binary" build -p 1 -trimpath -o /tmp/sub2api-codex0172 ./cmd/server
/tmp/sub2api-codex0172 -version
```

The repository integration harness creates temporary PostgreSQL and Redis containers and applies the existing migrations. No production DSN is supplied. Full apicompat unit tests and narrower implementation checks ran before the final race pass.

## Frontend

From `frontend/`:

```bash
pnpm exec vitest run src/views/admin/__tests__/CodexOutboundMetadataDetails.spec.ts
pnpm exec vitest run src/api/__tests__/admin.codexTelemetry.spec.ts src/views/admin/__tests__/CodexTelemetryObservations.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm run typecheck
NODE_OPTIONS=--max-old-space-size=4096 pnpm run build
git diff --name-only --relative -- src | xargs -r pnpm exec eslint
```

The last command ran before committing, with a nonempty changed-file list. After commit, supply the changed source paths explicitly.

## Benchmarks

Export `561ed4714` into a temporary directory and compile its service and apicompat test binaries with `go test -p 1 -c`. Compile the same packages from the final tree. With other checks finished, run each baseline/current pair alternately, three rounds, using the same CPU:

```bash
GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-baseline-service.test -test.run='^$' -test.bench='^BenchmarkCodex0169Compatibility$' -test.benchmem -test.benchtime=500ms
GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-current-service.test -test.run='^$' -test.bench='^BenchmarkCodex0169Compatibility$' -test.benchmem -test.benchtime=500ms
GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-baseline-apicompat.test -test.run='^$' -test.bench='^BenchmarkCodex0170ExistingChatBridge$' -test.benchmem -test.benchtime=500ms
GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-current-apicompat.test -test.run='^$' -test.bench='^BenchmarkCodex0170ExistingChatBridge$' -test.benchmem -test.benchtime=500ms

GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-current-service.test -test.run='^$' -test.bench='^BenchmarkCodex0172TraceProjection$' -test.benchmem -test.benchtime=500ms -test.count=3
GOMAXPROCS=1 taskset -c 2 /tmp/sub2-codex172-current-apicompat.test -test.run='^$' -test.bench='^BenchmarkCodex0172StandaloneBridge$' -test.benchmem -test.benchtime=500ms -test.count=3
```

The actual baseline export was `D:/WINTEMP/UTEMP/sub2-codex172-baseline-fQfDo7`. It contains only tracked baseline backend files; current edits and preexisting investigation artifacts were not copied into it.
