# Upstream v0.2.19 merge

## Baseline and scope

- Local parent: `beba5a3076394bd2af4f17152e570d9ffe3aacec`.
- Upstream parent: `a3298b46e`, including v0.2.19 and the subsequent OAuth non-streaming SSE fix.
- Common ancestor: `5c185c5c5d4f5e409e2139806e0964ae944e03e8`.
- Normal merge retaining both histories: 76 incoming commits, 261 incoming files, 24 textual conflicts.

The merge incorporates scoped administrator API keys, image-count billing, Grok catalog diagnostics, Gemini retry/model-list fixes, non-streaming response handling, DeepSeek pricing/cache accounting, pagination fixes and upstream dependency updates.

## Integration decisions

- Regenerate Ent from the combined schema, preserving Daybreak and multilingual fields alongside `image_count_billing`; regenerate Wire to include the scoped-key repository without dropping local background services.
- Bump the authentication snapshot to v26: both branches independently used v25 for different fields. Preserve Daybreak, image-count billing and the local model allowlist in cache round trips.
- Keep the pinned Codex 0.165 model descriptors and strict variants, including Terra's `low` default, six effort levels and full tool/context metadata. Retain upstream's Terra pricing and Responses tool-call guard; remove duplicate model entries and the synthesized extra effort level.
- Preserve Set/Omit/Unmanaged prompt-cache behavior, final wire metadata, Daybreak policies, OAuth authorization-generation guards and raw Engine forwarding. Mark Engine image endpoints for the new image-count billing policy without changing forwarded content.
- Combine upstream missing-Content-Type SSE detection and incomplete/cancelled terminal handling with local compact delivery tracking. Preserve intermediate model-conflict evidence and terminal-model priority for all providers.
- Keep scoped account/group writes on the existing account → ordered groups → bindings lock order. Route scoped Codex reauthorization through the existing authorization binder and honor explicit client identity OS during import/refresh.
- Keep the root repository for `SetSchedulable`'s existing post-commit cache callback. The new generic transaction clone intentionally disables immediate cache writes and must not swallow this deferred publication.
- Protect Grok catalog/metadata from stale administrator patch and bulk-update snapshots while retaining internal catalog synchronization.

No server deployment, Engine project edits or real-model requests are part of this merge.

## Verification

Validation uses local fixtures and isolated PostgreSQL/Redis containers. In addition to scoped unit tests, the upgrade test applies the two new migrations over the pre-merge schema, checks old migration ledger entries and group settings, and repeats startup twice. Ent/API-key group hydration includes Daybreak and image-count billing together.

The complete repository integration suite was compared with an archive of the exact local parent in the same environment. Existing failures include test data leaking into account/attribution list assertions, an older migration fixture missing `scheduling_revision`, and a subscription concurrency fixture reporting a bad connection. The new post-commit scheduling-cache regression was fixed and its commit/rollback test passed three consecutive runs.

Passing checks:

- Backend build; new-code lint across affected service, handler, repository, server, configuration and protocol packages.
- Frontend: 26 related suites / 636 tests after fixture corrections, type checking, targeted ESLint and production build.
- Model/protocol packages (`openai`, `gemini`, `geminicli`, `apicompat`), catalog/alias/pricing/Grok tests, OAuth non-streaming and multipart tests, Engine/Daybreak/metadata regressions.
- Native WS/control-frame, Codex 0.165 and bridge race tests across service, relay and handler packages.
- Isolated PostgreSQL/Redis upgrade, group hydration/membership and repeated-startup tests; scheduling commit/rollback cache test repeated three times.
- Scoped administrator tests with an explicit isolated PostgreSQL DSN: four packages, 199 successful test events; additional route, middleware, permission and DTO redaction checks.
- Managed Grok patch/bulk-update regressions preserve non-managed fields and internal sync paths.

The 427 test functions selected from incoming service test files exposed only the existing `TestGetFallbackPricing_FamilyMatching/openai_legacy_gpt5.1_falls_back_to_gpt5.4` expectation. It reproduces on the exact local parent: the local legacy-model default is newer than that old test's expected price. `TestAccountConfigurationExplicitIntentIsScopedAndCopied` and the missing-proxy expectation in `TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown` also reproduce unchanged on that parent. These baseline issues remain separate from the merge fixes; production protections and assertions were not disabled to hide them.
