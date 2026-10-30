# Codex 0.166.0 compatibility

This update follows the provided local Codex 0.166.0 source. Compared with the
official 0.165 baseline `e742db9f7ba98e63e502b310857d49d047888518`, the main
Responses request builder, request metadata, tool metadata, protocol models and
analytics event schemas are unchanged. The 12 bundled model descriptors, their
instruction templates and the full Sol descriptor also match; their pinned
source attribution remains unchanged.

The new protocol behavior is the Guardian assessment carried in response
metadata. Timing opt-in and critical-path analytics are corrections to earlier
compatibility omissions, not newly introduced 0.166 request fingerprints.

## Client identity and timing opt-in

The built-in UA fallback and compatibility marker are `0.166.0`. Saved
administrator settings and synchronized client identities retain their existing
precedence. This does not migrate saved UAs or replace TLS/HTTP2 profiles.

`x-responsesapi-include-timing-metrics` is forwarded when explicitly supplied on
HTTP or WS requests, including protocol bridges. It is never enabled by default.
Missing, explicit `true` and explicit `false` remain distinct. Other values and
duplicate values retain the existing forwarding behavior and are observed as
invalid rather than being rewritten.

Ordinary pooled sockets and prewarm reuse require matching timing headers after
account-level overrides. A continuation already bound to a particular socket
may reuse it when timing alone differs; the existing authorization, identity,
residency and transport checks still apply. No active turn is interrupted or
replayed to change an observability option. The new value takes effect on a new
physical connection.

Fingerprint observations expose optional `runtime_timing_metrics` containing
`status` (`absent`, `valid`, `invalid`), optional boolean `enabled`, and `source`
(`http_request`, `ws_handshake`). Reused WS turns display the physical socket's
handshake value, not the latest requested value. Old observations without this
object display "not recorded".

## Response evidence and administrator diagnostics

Guardian `response.metadata` events preserve `response_id`,
`metadata.guardian.item_id` and boolean `requires_review` end to end. The request's
existing `response_evidence.guardian_assessment` contains only the aggregated
status (`review_required`, `review_not_required`, `mixed`, `invalid`) and an
optional `invalid_present` flag. Repeated assessments are idempotent. This is
diagnostic evidence, not tool authorization; it does not add history or usage.

`x-codex-imagegen-request-id` is allowed by the default response-header filter,
subject to explicit administrator removal. It remains separate from
`x-request-id` and is also included in administrator header diagnostics.

`header_diagnostics` contains only optional `imagegen_request_id`, sanitized
`authorization_error`, and `identity_error_code` extracted from the standard
Base64 `x-error-json` header's `error.code`. It never stores the raw Base64 or
decoded JSON object. These authentication headers are not added to the default
client-facing header allowlist. Invalid or oversized diagnostic input does not
change request handling.

Fingerprint response evidence and the existing Ops `upstream_errors` JSON carry
the optional diagnostic object. HTTP failures, WS handshake failures and WS
response metadata are isolated by physical attempt/turn so retries cannot
inherit another account's diagnostics. User-facing error DTOs remain unchanged.
No database column or new management endpoint is needed.

## Analytics compatibility

Existing simulated main and title turns include `critical_path_status=disabled`
and seven null duration fields: `critical_path_duration_ms`,
`critical_path_inference_ms`, `critical_path_auto_review_ms`,
`critical_path_user_wait_ms`, `critical_path_tools_ms`,
`critical_path_overhead_ms`, and `critical_path_unknown_ms`.

Only the status is locally labelled `simulated`; null durations are not observed
measurements. Gateway latency is not apportioned into client timings. Local
source labels do not enter official event payloads. Observation-only mode does
not start producing simulated turn events.

Unsealed states restored from older versions use the new generator when they
are naturally sealed. Already sealed payloads remain immutable for sending and
retry. No metric snapshot version change or database migration is required.

## Verification and delivery

Use local HTTP/WS fixtures for timing opt-in, pool/prewarm compatibility, bound
continuations, Guardian forwarding, diagnostic redaction and retry isolation.
Exercise analytics recovery and sealed-payload immutability with isolated
PostgreSQL, and check the affected frontend details and legacy records.

The change preserves Daybreak, Engine protocol forwarding, account health,
retry and billing rules. Verification sends no real model requests, and delivery
does not deploy the application or modify the Engine project.
