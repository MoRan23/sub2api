# Executed checks

Repository: `C:/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api`. Commands below ran from `backend` or `frontend` as indicated. All Go checks use Go 1.27.4 and `GOEXPERIMENT=greenteagc,jsonv2`.

## Backend (Git Bash / Windows)

```bash
export GOEXPERIMENT=greenteagc,jsonv2
go test ./internal/service -run '^TestOfficialStatus' -count=1
go test -race ./internal/service -run '^TestOfficialStatus' -count=1
go test ./internal/server/routes -run 'TestChannelMonitor.*Guard' -count=1
go test ./cmd/server -run 'Test.*Cleanup' -count=1
go build ./cmd/server

GOMAXPROCS=4 go test -p 2 ./cmd/server ./internal/server/routes -run 'Test(ProvideServiceBuildInfo|ProvideCleanup_WithMinimalDependencies_NoPanic|ChannelMonitorModeV2Guard)' -count=1 -timeout=3m

go test ./internal/service -run 'Test(CodexCatalogMultiAgentCodeModeCapabilities|CodexCatalogPreservesAppsUsageInstructions|AstraUltraCatalogPreservesWorkflowMetadata|AstraUltraCatalogPreservesExplicitWorkflowOverrides|AstraCodexToolCapabilities|CodexToolCapabilities)' -count=1 -timeout=3m

go test -tags=unit ./internal/service -run 'Test(Codex0171|.*(Fingerprint|CodexWire|TurnMetadata|OutboundIdentity|OpenAIWS|TimingMetrics|Guardian).*)' -count=1
# The broad run above exposed the observation-header regression described in README.
# After the fix, this targeted race includes that regression and the new fixtures:
go test -race -tags=unit ./internal/service -run 'Test(Codex0171|CloneFingerprintObservationHeaders|OpenAIWSConnPool_AcquireReusesSameStableIdentity|.*Fingerprint.*Metadata.*|.*CodexWire.*)' -count=1

go test ./internal/service -run 'TestCodexTelemetry' -count=1 -timeout=5m
go test -race ./internal/service -run 'TestCodexTelemetry(0171|Coverage|ClientOnly|AuthStorage|DisableCancels|NativeHTTPPreserves)' -count=1 -timeout=5m
go test ./internal/service -run '^$' -bench '^BenchmarkCodexTelemetry0171Compression$' -benchmem -benchtime=400ms -count=3
go test ./internal/service -run '^TestOfficialStatusComponents$' -bench '^BenchmarkOfficialStatusGet$' -benchmem -count=3
```

## Isolated database checks and Linux build (WSL Ubuntu-24.04)

The repository's integration TestMain owns temporary PostgreSQL/Redis containers and applies the migrations. `CI=true` makes missing Docker an error. The two tests ran together initially; after correcting the official-status test's lock key, only that test was rerun. The telemetry persistence check passed in the first run.

```bash
cd /mnt/c/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api/backend
export GOTOOLCHAIN=local GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go_binary=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin/go
CI=true "$go_binary" test -p 2 -tags=integration ./internal/repository -run '^(TestOfficialStatusStoreRecoveryAndLease|TestCodexTelemetry0170PostgresReplayV3UpgradeV4Restart)$' -count=1 -v -timeout=300s
CI=true "$go_binary" test -p 2 -tags=integration ./internal/repository -run '^TestOfficialStatusStoreRecoveryAndLease$' -count=1 -v -timeout=300s
CGO_ENABLED=0 "$go_binary" build -p 2 -trimpath -o /tmp/sub2api-codex0171 ./cmd/server
/tmp/sub2api-codex0171 -version
docker ps --filter label=org.testcontainers --format '{{.ID}} {{.Names}} {{.Image}}'
```

## Frontend

The executable was `/c/Users/Admin/AppData/Roaming/npm/pnpm`.

```bash
pnpm exec vitest run src/features/channel-monitor-v2/__tests__/OfficialStatusCard.spec.ts src/features/channel-monitor-v2/__tests__/GroupOverview.spec.ts src/views/admin/__tests__/CodexOutboundMetadataDetails.spec.ts src/views/admin/__tests__/CodexTelemetryObservations.spec.ts src/api/__tests__/admin.codexTelemetry.spec.ts
# After the final postmortem/stale incident regression case:
pnpm exec vitest run src/features/channel-monitor-v2/__tests__/OfficialStatusCard.spec.ts
pnpm run typecheck
pnpm run build
pnpm exec eslint src/api/officialStatus.ts src/api/admin/codexTelemetry.ts src/api/admin/fingerprintObservations.ts src/api/__tests__/admin.codexTelemetry.spec.ts src/features/channel-monitor-v2/OfficialStatusCard.vue src/features/channel-monitor-v2/__tests__/OfficialStatusCard.spec.ts src/views/user/ChannelStatusV2View.vue src/views/admin/components/CodexOutboundMetadataDetails.vue src/views/admin/__tests__/CodexOutboundMetadataDetails.spec.ts src/views/admin/__tests__/CodexTelemetryObservations.spec.ts src/i18n/locales/en/channelMonitorV2.ts src/i18n/locales/zh/channelMonitorV2.ts src/i18n/locales/en/admin/fingerprintObservation.ts src/i18n/locales/zh/admin/fingerprintObservation.ts
```

Three-round baseline/current commands and process-affinity details are recorded in [performance.md](performance.md). Local-only logs include earlier failed fixtures so the correction history remains explicit.
