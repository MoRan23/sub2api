# Upstream v0.2.14 merge

- Upstream: `3f1a2ea0a760730e3bc528105c00b4ee4f23e469` (v0.2.14).
- Local parent: `496737ddb49f523fa6680c61801e62d7d996eafb`.
- Merge branch: `codex/merge-upstream-v0.2.14`.
- Verification date: 2026-10-07. No deployment was performed.

## Compatibility decisions

All 25 upstream changed files are included. The two text conflicts were in
`UseKeyModal.vue` and `pnpm-lock.yaml`.

The OpenAI HTTP and WebSocket examples retain the local authentication modes,
image extension header, idle timeout, standalone search/image features and
unconditional model discovery flag. That existing flag already implements the
upstream OpenAI discovery fix. Grok, routed and Composite remote examples receive
the upstream discovery flag, with regression assertions for switching to and
from a file catalog. Removed legacy configuration fields remain absent.

The lockfile retains local Testing Library/MSW dependencies, upgrades Vue to
3.5.43 and adds the upstream source-map-js override. PNPM regenerated the affected
Vue peer snapshots; incidental machine-specific mirror tarball URLs were removed.
The resulting lockfile installs with `--frozen-lockfile`.

EasyPay rejects unexpected notification fields and payment return URLs discard
untrusted query parameters. Settlement still uses the fixed-ratio `amount`,
`gift_ratio` and `gift_amount` snapshots; it does not adopt tiered bonuses or
discounts, or write gifts into `bonus_amount`. A combined regression checks a
rejected callback, paid-query recovery, distinct paid/ordinary/gift amounts and
exactly-once wallet/rebate fulfillment. Custom EasyPay payment methods retain
their existing active-verify recovery path; periodic reconciliation retains its
existing Alipay/WeChat selection.

Fresh installations generate administrator credentials unless valid credentials
are explicitly supplied. Existing users/admins are checked before bootstrap
credential validation, so upgrading does not reset an existing administrator.
Compose retains the local Codex telemetry settings alongside the upstream empty
administrator defaults.

No schemas or published migrations changed. OAuth authorization, Daybreak,
Codex-Engine forwarding/preferred scheduling/stream usage draining, dual-wallet
and frozen-image settlement, CDK recharge, attribution/expected-model detection,
LM Detector packaging/updater and Pelican previews are preserved. Their source
files are unchanged from the local parent, apart from the Codex example updates
described above.

## Verification

- Full frontend Vitest: **377 files, 2947 tests passed**.
- `pnpm typecheck`, production build, frozen lock installation and changed-file
  ESLint passed. The final Codex example spec also passed all 31 tests.
- Backend setup and provider packages passed in the full unit run. Focused
  EasyPay, return URL, gift fulfillment, idempotency and refund precision tests,
  including the added callback recovery test, passed.
- Windows amd64 and Linux amd64 `CGO_ENABLED=0` server builds passed with
  `GOEXPERIMENT=jsonv2`; affected-package `go vet`, formatting checks and
  `golangci-lint run --new-from-rev=496737ddb` passed.
- WSL Docker PostgreSQL/Redis integration harness passed
  `TestMergeV0213MigrationsFreshInstallAndRestart` and
  `TestMergeV0213MigrationsUpgradePreservesDualWallet`, using the current complete
  migration bundle, isolated databases and repeated migration application.
- Four Compose files passed individual static configuration checks using only
  `.env.example`; `bash -n deploy/docker-deploy.sh` passed.
- Payment verification used mocks, not real payment transactions. No production
  databases were used or changed.

## Pre-existing unit failures

The full `go test -tags=unit ./...` run does **not** pass. The following 13 main
tests fail identically when run on the unmerged local parent `496737ddb`; no
assertions were weakened and none were skipped to make this merge pass.

| Package | Test | Baseline cause |
| --- | --- | --- |
| handler | `TestGatewayModels_UnmappedOpenAIAccountsSupplementMappedModels` | Old positive timestamp assertion for a catalog entry whose timestamp is unknown. |
| handler | `TestGatewayModels_CustomModelsListFiltersDefaultFallbackModels` | Expected removed default `gpt-5.4`. |
| handler | `TestGatewayModels_OpenAICustomModelsListKeepsOpenAIResponseShapeForDefaultFallback` | Expected removed default `gpt-5.4`. |
| handler | `TestResolveOpenAIMessagesDispatchMappedModel` | Expected legacy default model IDs. |
| handler | `TestOpenAIResponses_FunctionCallOutputHTTPGuidanceDoesNotSuggestPreviousResponseReuse` | Incomplete billing fixture panics before the request validation under test. |
| handler | `TestOpenAIResponsesWebSocketV2PassthroughNonCyberTurnAllowsFollowup` | Whole-body equality does not account for the existing default instructions injection; both turns complete. |
| server | `TestAPIContracts` | Settings snapshots omit ten existing local telemetry/request-policy fields. |
| server/middleware | `TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream` | Settings test double lacks `GetMultiple` and calls its nil embedded interface. |
| service | `TestAccountConfigurationExplicitIntentIsScopedAndCopied` | Old account-level user-agent edit expectation for OAuth OS identity. |
| service | `TestGetFallbackPricing_FamilyMatching` | Legacy model aliases now resolve to the newer local pricing family. |
| service | `TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity` | Legacy OS slot status is no longer an admission gate. |
| service | `TestUsageProbeSkipsUnavailableSharedGrant` | Same obsolete status expectation; the incomplete fixture reaches the network and times out. |
| service | `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback` | Missing `GetByID` implementation on a nil embedded test repository. |
