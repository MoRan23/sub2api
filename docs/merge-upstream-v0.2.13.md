# Upstream v0.2.13 merge

- Upstream commit: `b8dece9000c68815a5b867ca5a1e6f236e173905` (v0.2.13)
- Local base: `b4ba8061cf057e4a7986247fce82b461692c0397`
- Merge branch: `codex/merge-upstream-v0.2.13`

The merge keeps the local fixed-ratio recharge gift flow. The upstream tiered
recharge bonus, tiered discount, configuration, API, and UI were intentionally
excluded. The upstream `bonus_amount` column and migration remain with a zero
default for schema compatibility; local settlement continues to use
`amount`, `gift_ratio`, and `gift_amount` without recalculating historical
orders.

The rest of v0.2.13 is included, including TypeSafe/System One, account
priority editing, atomic email verification attempts and reset-token consume,
public order rate limiting, deleted-key billing tolerance, and Axios 1.20.
The existing Pelican test preview, status overlays, timestamps, rounded
thumbnails, model list customizations, OAuth authorization, CDK recharge, and
dual-wallet settlement remain in the tree.

## Verification

- Frontend: `pnpm exec vitest run` — 372 files, 2896 tests passed.
- Frontend: `pnpm run typecheck`, `pnpm run build`, and changed-file ESLint passed.
- Backend: Windows `CGO_ENABLED=0 go build` and WSL Linux build passed.
- WSL Docker targeted integration tests passed for migration fresh install,
  upgrade/restart/idempotence, dual-wallet preservation, TypeSafe constraints,
  and Redis atomic verification/reset-token operations.
- Full backend unit and integration runs still report the same pre-merge
  failures in legacy model mapping, old auth fixtures, stale pricing/API
  contract assertions, and nil test doubles; they were reproduced on the
  local base and are not suppressed by this merge.
