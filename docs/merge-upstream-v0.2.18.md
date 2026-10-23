# Upstream v0.2.18 merge

- Upstream: `5c185c5c5` (`upstream/main`, v0.2.18).
- Local parent: `a60ed5b6a` (`dev`).
- Regular merge of 45 incoming commits, preserving both histories.
- Fourteen text conflicts resolved; no Engine repository changes or deployment.

## Compatibility decisions

- Scheduler snapshot memoization retains credential pruning. Full credentials
  and OAuth installation profiles are hydrated separately for each request;
  mutable data is not shared with memoized candidates. Model-mapping signature
  caching and model cooldown copy-on-write coexist with authorization-generation
  checks and local scheduling controls.
- Native Codex TUI defaults remain `codex-tui`, including the paired User-Agent
  suffix and `Ubuntu 24.4.0`. Upstream's explicit CLI normalization helper is
  available, but the merge does not enable it on the default path or overwrite
  frozen account identities. Published UA migration 273 is unchanged.
- Engine remains on its dedicated same-protocol forwarding path. OpenAI overload
  retries, queued/metadata event handling and Retry-After support do not cause
  Engine business errors to be replayed or protocols to be downgraded.
- Anthropic-compatible streams retain observed/final usage after downstream
  cancellation. Billing still carries local quota platform, pricing time,
  reasoning effort and session information. Daily/weekly account quota SQL and
  Vertex service-account quotas coexist with dual-wallet atomic settlement and
  frozen-balance handling.
- Gemini image-generation checks use the trusted request group, including the
  Composite parent, and recheck the mapped model. Token-counting stays exempt.
  Gemini stream cancellation and image delivery accounting are integrated.
- Image storage now validates public destinations, redirects and actual socket
  addresses. Private image URLs are rejected when image storage fetches them;
  this does not change private Engine API access or Base64 image handling.
- Cloudflare JSON/HTML 1010 edge rejections avoid account penalties while OAuth
  authorization-generation protection remains. Tool schema normalization and
  DeepSeek image-input deduplication are integrated.
- Account-filter persistence, quota display and Gemini group UI changes preserve
  Engine, Daybreak and attribution controls. Three-layer Daybreak policy,
  diagnostic overrides, durable 503 retries, initial allowlist application,
  later two-pass promotion, scheduling-control mode and mail approval behavior
  remain intact.
- Go advances to 1.27.3 across the module, images and all CI version checks.
  Frontend dependency changes follow upstream's lockfile. No extra dependency
  upgrades, Ent regeneration or Wire regeneration are needed.
- Migration `246_enable_gemini_image_generation_groups.sql` keeps its original
  full filename alongside local migrations. It enables existing Gemini and
  Antigravity image groups without changing other platforms or Daybreak/i18n
  fields. Repeat startup preserves subsequent administrator choices.

## Verification

All inference checks used mocks. Linux gateway/service tests ran in a separate
network namespace with loopback only; integration checks used isolated databases.

- Backend `go build ./...` passed with Go 1.27.3 and `GOEXPERIMENT=jsonv2`.
- OpenAI, Antigravity, handler DTO and migration unit packages passed.
- The broad service selection passed 1,603 top-level tests; 36 additional newly
  introduced service tests passed. Coverage includes streaming disconnects,
  model cooldown, image security, tool schemas, billing, Engine, Daybreak,
  authorization isolation, attribution and email approval. Known failures below
  were tracked separately; live billing-probe tests were excluded.
- The selected gateway/compatibility/Gemini handlers passed 159 top-level tests.
- Scheduler memoization, credential isolation, billing and quota repository unit
  tests passed. Model-mapping signatures, shared snapshot preparation and model
  cooldown copy-on-write tests passed.
- PostgreSQL 18.1 / Redis 8.4 integration checks passed 29 top-level tests
  (98 including subtests), with no skips. They cover quota settlement, dual-wallet
  transactions, scheduler cache isolation, migration upgrades and repeat startup.
  The integration user fixture now persists gift and frozen-wallet balances
  supplied by tests; existing settlement assertions were retained.
- Scheduler memo concurrent reads/writes, OAuth isolation and background revision
  preservation passed race checks.
- Frontend: frozen-lockfile installation, type checking, production build and
  changed-file ESLint passed. Related tests passed 530 unique cases, including
  six new account-filter persistence regressions.
- Affected-package incremental golangci-lint 2.14.0 passed with zero new issues.
  It was built with Go 1.27.3 in an isolated temporary directory; the older
  system lint tool could not read the new compiler's export-data format.
  Staged changes passed whitespace and conflict checks.

## Existing test limitations

Five backend failures were independently reproduced from an archive of the
unmerged local parent `a60ed5b6a`:

- `TestCaptureOpenAIOAuthIdentityIgnoresClientContextWindowID`: expects an empty
  context-window ID although current identity handling retains a UUID.
- `TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop`: existing changed-body assertion.
- `TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory`:
  ordinary, trigger and explicit cases omit existing injected instructions.
- `TestOpenAISetupTokenIdentityFlagOffAndAPIKeyStayNoOp`: full-body expectation
  omits existing injected instructions.
- `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback`:
  the 503 case panics in the incomplete `GetByID` test stub.

Three frontend assertions in `DiagnosticDaybreakEvidence.spec.ts` were also
reproduced on the parent: they still expect `0 / 2` where the existing first-result
behavior displays `0`. The implementation and assertions were not weakened to
make the merge appear green. Earlier documented v0.2.17 suite limitations remain
outside this merge's targeted verification.

The complete historical suites are therefore not reported as fully passing.
No real model requests, production data updates or automatic deployment were
performed.
