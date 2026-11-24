# Reproduction commands

Run from `backend/` with the existing Go 1.27.4 toolchain on Linux:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local GOMAXPROCS=2

go test -p 1 -tags=unit -count=1 -timeout=15m \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler \
  ./internal/server ./internal/server/middleware ./internal/server/routes \
  -run 'Test.*(HeaderOverride|ReportedHeader|UpstreamBilling|BillingGroup|GroupSync|APIKeyPassthrough|OpenAIPassthrough|CompactStructured|CompactResponseDelivery|ManagedExtra|OpenCodeGo|KeyBilling)|Test(AdminUpdateAccountRejectsVariablesOnNonAPIKeyAccounts|OpenAIWSHandshakeCompatibilitySeparatesReportedIdentity|AppendOpsUpstreamErrorCapturesReportedHeaders|AnthropicUpstreamBuildersReportIdentity)'

go test -p 1 -tags=unit -count=1 -timeout=10m \
  ./internal/server/middleware ./internal/server/routes ./internal/server ./internal/pkg/adminaccess ./internal/handler/admin \
  -run 'Test(APIKeyAuth|SimpleModeBypassesQuotaCheck|RequireGroupAssignment|APIContract|Route|Admin|AccountHandlerBatchCreateForwardsBillingSyncSettings|GatewayKeyBilling|KeyBilling|UpstreamBilling)'

go test -race -p 1 -tags=unit -count=1 -timeout=10m ./internal/service \
  -run 'Test.*(HeaderOverride|InvalidEncryptedContent|Guardian|Residency|OpenAIPassthrough|CompactStructured)|TestOpenAIWSConnPool|TestOpenAIWSHandshakeCompatibilitySeparatesReportedIdentity|TestCodexEngineWS|TestUpstreamBillingProbe(ManualBatchesShareConcurrencyLimit|ManualAndScheduledRequestsShareOneNetworkProbe|FiveInstancesRunOneConcurrentBatch)'

go test -race -p 1 -tags=unit -count=3 -timeout=5m ./internal/service \
  -run '^TestCodexEngineWSUsesDedicatedHTTPAndDurableIDPerTurn$'

CGO_ENABLED=0 go build -p 1 -trimpath -o /tmp/sub2-merge025-server-linux ./cmd/server
```

## Isolated storage

The repository harness starts disposable PostgreSQL 18.1 and Redis 8.4 containers and applies migrations. `CI=1` makes unavailable Docker fail instead of silently skipping integration checks.

```bash
CI=1 go test -p 1 -tags=integration -count=1 -timeout=15m ./internal/repository \
  -run 'Test(AccountUpdatePreservesConcurrentProbe|AdminAccountEditPreserves.*SynchronizedAfterLoad|BillingGroupSyncPreservesEngine|ProbeSnapshot|AccountUpdateClearsProbe|BulkUpdateAndCredentialUpdateDeleteProbeKey|ProxyIdentityUpdateInvalidatesProbe|ProbeGroupWrite|ListDueUpstreamBillingProbe|CodexEngineModelsIntegration|CodexEngineCapacityIntegration|SchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount)' -v

CI=1 go test -p 1 -tags=integration -count=1 -timeout=15m ./internal/repository \
  -run '^TestAccountRepoSuite$/(TestAdminConfigurationExplicitEditsAndStaleForm|TestPatchOpenAIOAuthCredentialsIfUnchanged|TestOAuthSharedCredentialsAllOSReadOneGrantAndRefreshFencesEveryIdentity|TestOAuthMetadataRevokeAndCASAreAtomic|TestOpenAIOAuthAccountState_)' -v
```

The proxy-outbox test was also run alone and after the Engine capacity/models tests on the exact pre-merge export to distinguish an existing test-isolation problem from a production regression. See [validation results](README.md).

## Frontend

From `frontend/`:

```bash
pnpm exec vitest run --maxWorkers=1 --minWorkers=1 \
  src/components/account/__tests__/EditAccountModal.spec.ts \
  src/components/account/__tests__/CreateAccountModal.spec.ts \
  src/components/account/__tests__/BulkEditAccountModal.spec.ts \
  src/components/account/__tests__/credentialsBuilder.spec.ts \
  src/views/admin/__tests__/AccountsView.lite.spec.ts \
  src/views/admin/ops/components/__tests__/OpsErrorDetailModal.spec.ts \
  src/views/admin/ops/utils/__tests__/errorDetailResponse.spec.ts \
  src/components/account/__tests__/UpstreamGroupSyncButton.spec.ts \
  src/components/account/__tests__/HeaderOverrideEditor.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts \
  src/api/__tests__/admin.accounts.upstreamBillingProbe.spec.ts
pnpm run build
```

The checked-in regression tests include cancellation and source changes. `build` performs locale checks, TypeScript checking and Vite production compilation. ESLint was run without fixes on all 25 changed TypeScript/Vue files.

## Performance

Export the exact baseline and build its service/handler test binaries with `go test -c -tags=unit`, using the same Go version and experiment settings. Run the selected cases serially with `-test.run='^$' -test.benchmem -test.benchtime=300ms -test.count=3`. Repeat timing outliers with alternating baseline/merged binaries and longer samples. New-feature benchmark results are measurements of new paths rather than invented pre-merge equivalents. See [performance results](performance.md).
