# Upstream v0.2.17 merge

- Upstream: `b0a02a330` (`upstream/main`, v0.2.17).
- Local parent: `610dd30a2` (`dev`).
- Regular merge preserving both histories; no deployment or Engine repository changes.

## Compatibility decisions

The merge includes 48 incoming commits and resolves nine text conflicts by
combining the upstream behavior with the fork's existing contracts.

- Configurable minimum output tokens coexist with timezone observation and
  client-specified Daybreak fields during protocol conversion. Frozen outbound
  models remain authoritative in compatibility normalization.
- Responses `created_at` compatibility retains local model rewriting, tool-name
  restoration and stream processing. Engine remains on its dedicated forwarding
  path and is excluded from web-search emulation.
- GPT-5.5 retirement applies to the upstream-defined ChatGPT subscription
  accounts. It does not turn Engine/API-key accounts into subscription accounts
  or replace the real model catalog used for diagnostic Daybreak evidence.
- Workspace-aware Codex imports retain OS authorization slots, authorization
  generation checks, shared-credential isolation and account Daybreak preferences.
- Pending refunds retain both paid-wallet and gift-wallet deductions until the
  provider confirms success or failure. Pending requests cannot start another
  refund; failed refunds return the held amounts transactionally.
- Gemini/Antigravity model catalogs, thinking compatibility, Vertex retry fixes
  and model pricing are integrated while preserving scheduler credential pruning,
  hydrated-account checks and explicitly free image prices.
- Account filtering and payment-page updates retain local diagnostic modes,
  attribution controls and Engine settings. Engine has no web-search emulation
  option because its requests use protocol passthrough.
- Diagnostic Daybreak snapshots, durable attribution 503 retries, two-pass
  promotion, telemetry pool cleanup and Outlook replies without blank separators
  remain intact.
- This upstream release changes no Ent schema, Wire providers, migrations or
  dependency manifests; generated schemas and dependency locks are not rewritten.

## Verification

Verification uses local mocks and isolated database containers, with no real
model inference:

- Backend `go build ./...` passed with Go 1.27.2 and `GOEXPERIMENT=jsonv2`.
- All config, domain, Antigravity, API compatibility and payment-provider unit
  packages passed. Payment/refund/webhook regressions include held gift-wallet
  deductions and transactional rollback of failed refund settlement.
- OpenAI compatibility tests passed apart from the existing large-frame fixture
  limitation listed below. New regressions cover output-token floors together
  with timezone observation, frozen models and unchanged Engine WS responses.
- 450 custom service tests passed; 78 critical race tests passed. The Linux
  service tests ran in a separate network namespace with only loopback available.
- The Gemini/Antigravity/Vertex/pricing/billing/Composite selection passed 1,019
  of 1,021 top-level tests. Both failures reproduced on the unmerged local parent.
  The unrelated upstream billing-probe tests were excluded to avoid live requests.
- Admin imports, diagnostics, model availability, route boundaries and API
  conversions passed. Three broader Handler failures reproduced on the parent.
- Isolated Docker PostgreSQL/Redis checks passed for attribution checkpoints,
  expected models, diagnostic Daybreak snapshots, telemetry cleanup, scheduler
  cache credential pruning and repeat migration startup. `CI=1` makes unavailable
  Docker fail rather than count a skip as a successful database run.
- Frontend: 241 related tests passed, along with type checking, production build,
  three locale-completeness tests and changed-file ESLint. Account-filter behavior
  and accessible labels remain tested after adopting the upstream layout.
- Affected-package `golangci-lint` 2.14.0 reported zero new issues, and the final
  diff passed whitespace/conflict checks.

## Existing test limitations

The following six tests fail with the same assertions on local parent
`610dd30a2` and the merged tree. They were not weakened or removed:

- `TestResolveOpenAIMessagesDispatchMappedModel`: legacy family-default model.
- `TestOpenAIResponses_FunctionCallOutputHTTPGuidanceDoesNotSuggestPreviousResponseReuse`:
  incomplete billing fixture.
- `TestOpenAIResponsesWebSocketV2PassthroughNonCyberTurnAllowsFollowup`:
  full-body equality omits existing injected instructions.
- `TestGetFallbackPricing_FamilyMatching`: legacy GPT-5.1 price expectation.
- `TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling`:
  full-body equality omits existing metadata.
- `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB`: fixture reserves only
  4 KiB for metadata, less than the existing injected identity metadata.

The broader suites are therefore not reported as completely green. The relevant
new functionality and fork regressions above passed independently of these
known failures. No real model requests, production database changes or automatic
deployment were performed.
