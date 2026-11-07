# Upstream v0.2.20 merge / 上游 v0.2.20 合并

## Parents and scope / 基线与范围

- Local parent: `b41bb98fb7313b33d7c18bc0e72d6e3d7db86094`.
- Upstream parent: `6b4b37db43443b5e88e424fe3c69602cc3810dfb` (`v0.2.20`).
- Ordinary merge: 107 incoming commits, 458 upstream files, 50 text conflicts.
- Engine repository and deployed services are outside this change. Verification uses synthetic requests, mock upstreams and isolated databases.
- 保留两侧历史和原有六份未跟踪排查文件；本轮不操作生产模型、不修改 Engine 项目、不部署。

## Images behavior / 图片行为变化

Image parameter validation follows upstream for **all** image models. `gpt-image-2.6-sunburst` and `gpt-image-2.6-flare` use native Images generation/edit endpoints after model mapping, including when those endpoints return 404 or 405. Older eligible models retain their existing Responses fallback.

所有图片型号统一采用上游参数规则。`quality` 与 `complexity` 分别保留，不互相转换、不补默认值；接入上游 SVG 选项、编辑标注和 JSON/multipart 处理。移除本地额外的 GIF/SVG、流式、多图和帧参数组合限制。新增标注图进入既有内容审核流程。

The merged path includes image-size tier scheduling and the upstream JSON-to-completed-SSE adapter. A complete JSON response to a streaming request emits each returned image and settles usage once. Ordinary API Key requests retain original vendor extension fields; Engine keeps its dedicated forwarding path. Proxy routing, Daybreak policy, final-wire fingerprinting, integrity checks, original prompt whitespace, GIF bytes, safe SVG download and actual image-count billing remain in place.

## Compatibility decisions / 兼容处理

- Request-body budgets coexist with body-retention observation. Each physical send retains its patch scope and integrity check. Budget exhaustion returns immediately; other recoverable patch failures may fall back to map processing.
- Engine SSE keeps its two primary frame leases, original bytes and cancellation/late-usage rules, while enforcing upstream event-size and line-count limits. Large buffers are not retained in the reuse pool.
- Engine's already-trimmed lines reuse the shared limit check without trimming again. Overflow inspection is delayed until the byte count can exceed the budget; CRLF, empty events, comments and fragmented-line boundary tests preserve the limit semantics.
- Shared immutable scheduler candidates and detached billing snapshots remain. Queued usage tasks do not regain Gin contexts or request bodies. Upstream bounded post-billing and notification workers coexist with gift balances, reservations, idempotency and Engine confirmed-event settlement.
- The local unified `previous_response_id` preselection now carries the upstream image-size sticky-account context. Default/unknown size preserves the bound account; explicit unsupported size is rejected without discarding the response binding. Group, privacy, health, authorization and transport checks remain.
- WS bridge identity and prompt-cache decisions remain in the local unified finalizer. Instructions echo is synchronized **after** request building so it sees local finalizer changes and restores the original client value correctly.
- Auth cache version is **28**, combining Daybreak/gift/image settings with privacy requirements and group-scoped RPM override provenance.
- Terra is loaded from the local official catalog once, then projected through account capabilities; a second raw upstream descriptor load does not re-enable Lite, search or code mode.
- Management MCP/OAuth includes authorization, revocation, auditing and resource permissions. Without a public URL it remains disabled. Full-access administrator keys follow upstream scope; restricted keys retain resource checks.
- Upstream email blacklist checks coexist with affiliate invitation admission and transactional inviter binding. Codex configuration export preserves local auth, WS, timeout and model-directory settings.
- Vue i18n/compiler move together to 11.4.16 with upstream Vite integration. The lockfile keeps both required dependency sets. Wire is regenerated; local services and shutdown ordering are retained. No Ent schema generation is needed.

保留本地账号授权代次、Daybreak 三层开关、归因任务恢复及模式、遥测增量事务、双钱包、邀请注册、Engine 容量同步和容量暂停提示。上游 Kimi 并发冷却改用限流存储，相关失败注入测试随新存储入口调整，继续验证数据库失败时运行态封禁有效。

## Migration and rollout / 迁移与上线

`249_mcp_oauth.sql` is kept under its full upstream filename alongside the existing local `249_openai_oauth_daily_os_roots.sql`. Migration identity uses full filenames. Applied migrations are not rewritten or renumbered. Fresh installation, upgrade from the complete local migration ledger through 278, and repeat startup are verified on isolated PostgreSQL.

按既有流程备份数据库后更新应用即可；本轮不新增遥测事务协议版本，不重复执行上一轮内存优化的迁移。回退应使用部署前的应用与数据库备份配对方案，不能删改已应用的迁移记录。管理 MCP 的公共地址需要管理员明确配置。

## Verification / 验证

- Linux amd64 backend build passed, including the regenerated Wire graph.
- Images tests cover 2.6 native-only routing, old-model fallback, mapped models, complexity/quality, SVG/GIF, JSON/multipart markup, transparent backgrounds, JSON-to-SSE, errors, proxy/Daybreak/Engine boundaries and safe downloads.
- Changed service-test files exercise **882 top-level tests** after the listed baseline exclusions. The size-routing/previous-response case exposed a merge interaction, was repaired, and passed its final targeted rerun along with all size-routing, previous-response and WS-state-store tests.
- Repository, handler and middleware broad unit runs passed **850 / 1,007 / 194 top-level cases** after excluding the recorded baseline failures. Their fixtures contain 16 / 1 / 0 explicit skips, primarily database or live checks; these are not reported as passes.
- Shared candidates, auth v28, frozen billing/reservations, Engine SSE, WS interrupt/turns, Daybreak, integrity and incremental telemetry passed targeted race checks. Body budgets and retention observation are tested together.
- MCP authorization/refresh replay, code-exchange competition, revocation, multi-instance keys, fresh migration, upgrade from the local ledger, repeat startup, mailbox aliases and batch administrator restrictions passed isolated PostgreSQL tests.
- Additional PostgreSQL/Redis attribution, Daybreak, invitation, capacity, telemetry, scheduler and dual-wallet checks passed **120 assertions/cases including subtests, zero skips** (49 top-level tests/suites). These are separate from the unit fixture skips above.
- Frontend frozen-lock install, typecheck, production build, changed-file ESLint and **437 files / 3,750 tests** passed.
- GitHub Actions are disabled for this repository; remote CI was not run.

后端完整历史测试集仍有下列既有失败；本轮没有删除保护逻辑或降低断言以隐藏它们。对应测试均在 `b41bb98fb` 归档源码或其测试二进制中复现。扩大回归的 Linux 进程只开放本机回环网络；依赖外网 DNS 的情况单独列出。

### Baseline failures / 基线失败

| Test | Existing limitation |
|---|---|
| `TestCodexModelsFailsOverFromInvalidManifestEnvelope` | Old empty API-key catalog envelope expectation. |
| `TestResolveOpenAIMessagesDispatchMappedModel` | Old GPT-5.4 family default assertion; local default is GPT-5.6 Sol. |
| `TestOpenAIResponses_FunctionCallOutputHTTPGuidanceDoesNotSuggestPreviousResponseReuse` | Fixture omits dependencies required before account-specific validation. |
| `TestAPIKeyAuthForwardsUserScopedOpenAIFastPolicyToUpstream` | Incomplete repository stub lacks `GetMultiple`. |
| `TestGrokCatalogCASRefreshesSingleAccountOutsideTransaction` | SQL mock omits the existing OAuth OS-profile relation query. |
| `TestAccountConfigurationExplicitIntentIsScopedAndCopied` | Old legacy environment-edit expectation for an OAuth OS-profile owner. |
| `TestGetFallbackPricing_FamilyMatching` | Old GPT-5.4 price assertion after local GPT-5.1 normalization to Sol. |
| `TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown` | Configured proxy failure now reports `ACCOUNT_PROXY_UNAVAILABLE`; old assertion expects token-unavailable. |
| `TestLiveCreateUsesSharedAuthorizationAndRequestedOSIdentity` / `TestUsageProbeSkipsUnavailableSharedGrant` | Tests use legacy authorization Status; current contract validates credentials and generation instead. Generation/rebind boundaries pass separately. |
| `TestCodexContextObservationHistoryRejectionWaitsForDeliveryWithoutFallback` | Incomplete OAuth owner `GetByID` stub in the 503 case. |
| `TestCodexContextWindowSyncRoundTrip` | Account lookup fixture returns `ACCOUNT_NOT_FOUND`. |
| `TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution` | Stale lookup-count/reload expectations after owner revalidation. |
| `TestForwardCodexHistoryNotesReusesResponsesStickyBinding` | Incomplete OAuth owner `GetByID` stub. |
| `TestValidateCreateParams_CheckModeMatrix` | Two cases require public DNS; loopback-only verification returns endpoint-unreachable first. |

## Performance record / 性能记录

The [three-round report](../backend/benchmarks/results/merge-v0220/performance-summary.md)
and [raw data / reproduction](../backend/benchmarks/results/merge-v0220/README.md)
compare the already optimized parent with the merged code. Both use Go 1.27.3,
`GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4` and identical synthetic fixtures.
Extended retests of noisy short-latency scenarios are recorded separately.

- 2,000 accounts shared across 100 groups retain about **44.1 MiB** of decoded cache on both revisions.
- Queued billing retains about **3.4–3.8 KiB per task**, independent of the discarded 1 MiB source payload.
- A 32 MiB Engine SSE fixture allocates about **197 MiB per stream** on both revisions; the two-frame implementation remains in place. This allocation total includes parsing and observation, rather than describing a resident 197 MiB frame buffer.
- Extended cache reads at 100 groups show median `ns/op` **+0.16%**, p95 **-10.40%**, retained bytes **+0.05%**.

The [focused SSE report](../backend/benchmarks/results/merge-v0220/sse-optimization-summary.md) records six pairs before and after the final trim change, followed by three same-window direct pairs. Original candidate vs parent: median latency **+4.84%**, p95 **+4.25%**. Final implementation vs parent in the next batch: **+14.48% / +38.92%**. Same-window candidate-to-final comparison: **-3.12% / -13.75%**, improving in every latency pair. All retain **17,232 B/op, 314 allocs/op**. Profile TrimRight CPU share falls from **5.42% to 2.16%**. The fixture's p95 covers at most the first 1,024 calls per invocation.

内存保留量及分配量维持合并前水平。WS 多行帧时延在批次间仍有明显波动；原始、延长及直接对照的全部轮次分别保留，包括较慢批次，不拼接跨批次数据推算最终收益。最终定向 race、Linux 测试及后端编译通过。未进行生产模型压测或部署。
