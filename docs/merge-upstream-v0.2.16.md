# Upstream v0.2.16 merge

- Upstream: `4d9ac3e482106d2da3a39fe3e546d8d6801f1cc1` (`upstream/main`).
- Local parent: `3d8c49f565cb1c8efd01f22ed8f04b920dbada7f` (`dev`).
- Verification date: 2026-10-13. No deployment or Engine changes.

## Compatibility decisions

All 79 incoming commits are merged with both histories retained. Seven text
conflicts are resolved by combining the upstream behavior and local contracts.

- Group translations coexist with Daybreak Blue/Red. Ent and Wire are fully
  regenerated: this also corrects automatically merged group field/default and
  `sort_order` index offsets, and includes Gemini cache lifecycle cleanup without
  removing local workers.
- Scheduler candidates continue to exclude API keys and OAuth secrets. Gemini
  explicit-cache candidate checks use public capability metadata; hydrated
  accounts still require valid credentials before sending. Bound caches retain
  their original account ownership.
- Model-scoped temporary cooldowns retain the local OAuth authorization-generation
  compare-and-swap protection. Engine business errors do not enter generic
  account-health handling; its narrowly scoped deployment cooldown is preserved.
- GPT-6.1 subscription long-context exemptions are propagated to observed-tier
  cost recalculation. The actual authorization used for billing stays frozen;
  Engine scheduling preference does not turn API-key pricing into OAuth pricing.
- The new Chat-to-Responses compatibility retry keeps the original request body
  and final outbound Daybreak policy. Engine remains on its early direct-forward
  branch, with original business errors and no protocol fallback or replay.
- Engine scheduled attribution, generic API-key manual-only attribution, initial
  OAuth tests, expected-model snapshots, two-pass promotion, and Outlook replies
  without a blank separator remain intact.
- The four new migrations keep their published filenames and SQL. The runner
  keys by full filename, so shared numeric prefixes do not overwrite or replay
  existing local migrations.
- Dependency versions and frontend lockfile are unchanged. `go.sum` includes
  missing checksums required by the existing Ent/Wire generation tools.

## Verification

- Windows and Linux `CGO_ENABLED=0` server builds passed with Go 1.27.2 and
  `GOEXPERIMENT=jsonv2`; server wiring tests and affected-package `go vet` passed.
- Frontend: 62 related Vitest files / 727 tests passed; type checking, locale
  checks, production build and changed-file ESLint passed.
- Go: changed-file `golangci-lint` 2.14.0 reported zero issues. Relevant Engine,
  Daybreak, scheduling, attribution, group approval, Gemini, cooldown, protocol
  retry and subscription-pricing regressions passed.
- Isolated WSL Docker PostgreSQL/Redis tests passed for the new migrations,
  repeated startup, preservation of Daybreak/OAuth/Engine data, Gemini cache
  lifecycle, group/API-key/settings repositories, moderation locking and Engine
  attribution. Cache round trips also verify that candidate metadata contains no
  secrets while Gemini accounts remain selectable.
- Repository integration CI is configured to run independently of unit tests.
  Non-repository integration coverage remains in the existing test job.
- After merge commit `351620d54` was pushed to `origin/dev`, GitHub's repository
  Actions permissions reported `enabled: false`; no new CI/check run was created.
  That repository setting was not changed. The database validation reported here
  was actually executed in local isolated WSL containers, not inferred from CI.

## Existing test limitations

The broader backend suite is not fully green. These failures were reproduced on
the unmerged local parent rather than hidden or weakened:

- Service: account-level OAuth identity edits, the old auth-cache v24 assertion,
  legacy GPT-5.1 fallback-price expectation, legacy shared-authorization admission
  expectations, and a context-observation fixture with an unimplemented `GetByID`.
- Handler: legacy default model IDs, an incomplete billing fixture, and whole-body
  WS equality that does not account for existing instructions injection.
- The inflight-memory assertion failed in the broad run; it passed on the parent
  and three isolated runs on the merged tree. Its threshold was not changed.
- Full repository integration reported 25 failing top-level/subtest names; the
  parent reported the same 25 plus the candidate-key assertion fixed in this
  merge. There were no newly failing names. Existing failures include leaked
  shared account fixtures affecting list/attribution tests, telemetry timing,
  an older migration fixture, gift-wallet fixtures and subscription concurrency.
  The focused database suite above passed independently of these failures.

The legacy usage-probe fixture was checked with a cancelled context in the
temporary baseline to prevent a real upstream request. No real model inference
was used for merge verification.
