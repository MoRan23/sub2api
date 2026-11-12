# Engine capacity and model catalog synchronization

## Behavior / 行为

Codex-Engine accounts now fetch capacity and the public model catalog at startup
and every 60 seconds. No additional switch or Engine change is required.
Ordinary API-key and OAuth accounts retain their existing behavior.

- Each account uses the same immutable credential/proxy snapshot for parallel
  `GET /v1/engine/capacity` and `GET /v1/models` calls. Each call has its own
  15-second timeout. Eight account workers bound auxiliary traffic to 16 requests.
- Each observation succeeds or fails independently. HTTP errors, invalid JSON,
  missing/null catalogs, timeouts, and degraded catalogs preserve the last
  successful value. Configured proxies never fall back to a direct connection.
- Model IDs are trimmed, sorted and deduplicated, preserving case and internal
  spaces. The mapping becomes exactly `id: id`, including aliases published by
  Engine. Local extra models and aliases are replaced on the next successful sync.
- An explicit successful empty list means **no allowed models**, including public
  model listings, Codex Manifest and model routing. Capacity zero retains models
  and uses the existing capacity-owned pause/recovery rules.
- Model updates recheck source identity, independent observations and the model
  configuration under the account row lock. Concurrent capacity updates do not
  invalidate a model observation. Manual mapping changes invalidate work already
  in progress; a later cycle follows the Engine catalog again.
- A server-owned source revision fences URL/key/proxy/mode changes even when
  administrators change a value back. Proxy configuration revisions also reject
  stale observations. These internal revisions are hidden from account DTOs.
- Unchanged observations cause no account write, timestamp update or outbox event.
  Changed mappings and the scheduler outbox commit together. Background account
  snapshots preserve the latest synchronized mapping.
- Engine attribution in whitelist mode records its actual verdict only: it does
  not change mappings/groups or accumulate whitelist upgrades. Scheduling-control
  mode retains its existing pause/recovery behavior.

模型名单自动同步会整体替换本地名单和别名。管理员页面显示同步说明；
成功空目录显示「上游暂无可用模型」。目录同步不会解除管理员暂停。
普通 API Key/OAuth 及归因的开关、并行探针规则保持原样。

## Performance / 性能边界

Only IDs are decoded from model entries; no descriptor service, capability
enrichment or model probe is invoked. Both observations share the account
snapshot. No network request runs inside a database transaction. Unchanged
mapping comparison uses the decoded map; only changed mappings allocate the
replacement map. Empty-catalog checks read a few scalar fields, without JSON
marshalling or credential hashing on the routing path.

Catalog invalidation reuses scheduler metadata revisions and outbox maintenance.
It adds no per-request Redis lookup. Local cache generations reject late fills
that started before invalidation. Redis account snapshots retain the explicit
empty-catalog marker; existing JSON formats remain compatible.

## Upgrade / 升级

No database migration is needed. Deploy Sub2API normally; Engine needs no
deployment. The first scan starts immediately. The source catalog must implement
the existing Engine `object: "list", data: [{id: "..."}]` response.

Before reverting to a version without this feature, restore any desired local
aliases from an administrator backup and account for old versions treating empty
model mappings as unrestricted. Rolling back code does not restore replaced
model mappings.

## Verification / 验证

Local mock upstreams cover independent failures, parallel requests, the 16-request
bound, cancellation, valid empty data, degraded responses, ID normalization,
proxy failure and `30 → 20 → 0 → 30` capacity changes.

Isolated PostgreSQL/Redis coverage includes model replacement/restoration,
no-change timestamps and outbox counts, concurrent observation CAS, stale
snapshots, explicit administrator edits, rollback, and attribution record-only
versus scheduling-control behavior. Source and model CAS integration tests also
passed with the race detector. Cache tests cover normal lists, Manifest,
empty-catalog routing, cross-instance revision changes, Redis read failures and
stale cache-fill rejection.

Frontend component tests, type checking, directed ESLint, i18n checks and the
production build passed. The build retains existing Browserslist/chunk warnings.
Focused Go service/DTO/cache race checks and the backend build passed.

Reproduction commands (backend uses Go 1.27.3 and
`GOEXPERIMENT=greenteagc,jsonv2`):

```sh
go test -race ./internal/service ./internal/handler/dto -run 'Test(CodexEngine(Capacity|Models|Observations|Known)|DecodeCodexEngineModel|AccountCodexEngineModels|EngineSyncedEmpty|EngineCatalog|OpenAIGroupCatalog|InvalidateAvailableModelsCache|GetAvailableModels_|AccountModelMapping)' -count=1
CI=true go test -race -tags integration ./internal/repository -run 'TestCodexEngine(Source|Models|Capacity)Integration|TestApplyCodexEngineCapacity|TestPreserveCodexEngineCapacityLocked|TestAccountConfigurationExtraPatch' -count=1 -timeout=300s
CI=true go test -tags integration ./internal/repository -run TestAttribution -count=1 -timeout=300s
go test -race -tags unit ./internal/repository -run 'TestCodexEngine.*Scheduler' -count=1
go build ./cmd/server
```

Frontend: `pnpm exec vitest run` for `AccountStatusIndicator.spec.ts` and
`OpenAIAccessModeSelect.spec.ts`; `pnpm exec vue-tsc --noEmit`; directed ESLint
for changed components, tests, locales and types; `pnpm build`.

The broader configuration suite encountered the existing
`TestAccountConfigurationExplicitIntentIsScopedAndCopied` UA expectation
(`old environment` versus Ubuntu). This unchanged baseline failure is already
documented in [the v0.2.19 verification](UPSTREAM_MERGE_V0_2_19.md).

No production requests, configuration changes or deployments were performed.
