# Codex 0.165.0 compatibility

The model catalog is pinned to official commit
`e742db9f7ba98e63e502b310857d49d047888518`. Its `models.json` matches the
provided Codex 0.165.0 source snapshot. Protocol and telemetry fixtures were
checked against that source snapshot; the original `316795b` wire fixtures
remain as regression coverage.

## Models and client version

The bundled catalog contains 12 models, including `gpt-6.1-terra`. Terra keeps
its own descriptor and collaboration/confirmation policies, with default low
reasoning, six reasoning levels and 500000/872000 context limits. Its main
instructions exactly match Astra and explicitly reuse that template.

Existing aliases, upstream explicit model metadata and Sol's local 500000
context default retain their precedence. Ordinary API Key routes retain their
existing Responses Lite restrictions. Catalog membership grants neither account
access nor Daybreak eligibility; capability evidence still comes from the real
account's upstream directory. This update does not change prices or stored
model mappings.

The built-in client version fallback is `0.165.0`. Administrator configuration
and synchronized versions continue to take precedence; saved values are not
migrated. UA, originator and version retain the existing coordinated identity
policy.

## Request metadata and cache keys

Canonical turn metadata supports `history_initialization` (`new`, `cleared`,
`cold_resume`, `warm_fork`, `cold_fork`, `supplied_history`), optional boolean
`guardian_recorded_items`, and optional boolean `analytics_enabled`. Missing
values are not inferred. Explicit booleans survive canonical rebuilding.

`mcp_attribution` remains a flat, body-only `client_metadata` JSON string. The
fingerprint UI records bounded presence/type/size diagnostics, never its content.
New observations also show the typed turn fields and the final cache-key shape.

Native Responses and WS requests that omit `prompt_cache_key` retain that
absence. Chat/Messages conversion and explicitly identified synthetic probes
retain the session-derived default. Explicit custom keys retain owner-scoped
mapping, and explicit invalid values retain the previous fallback. Session and
thread identity are resolved independently. Retries resolve from the original
capture; every WS create captures its own key rather than inheriting a previous
turn's override. Final projection removes intermediary keys when the frozen
decision is Omit. The same rule applies to synchronous requests.

Codex Responses accepts an explicitly supplied
`stream_options.reasoning_summary_delivery="sequential_cutoff"`. The existing
unsupported members are removed, and legacy compact does not receive streaming
options. No experimental feature is enabled automatically. Developer-message
base instructions and opaque encrypted replay data remain intact.

## WebSocket lifecycle

Interrupt acknowledgements are control frames: they are forwarded without
starting another turn or settling usage. Native connection pools use one
connection-owned downstream reader with one pending create slot so interrupts
can reach the currently active response. A valid but idle/mismatched response
ID receives `response.interrupt.failed`; malformed IDs receive a control error.

Terminal usage settles once, independently of ACK order. Interrupted requests
are not recovered by replay. Pending or failed interrupt cleanup retires the
upstream connection after bounded draining instead of returning it to the pool.
Active responses retain their ordinary read timeout; terminal/failed-ACK and
disconnect cleanup uses a 1200 ms drain window. Cleanup errors do not mark the
account unhealthy. Connection-owned readers survive account failover and are
joined when the downstream connection ends.

The HTTP-to-WS bridge carries valid upstream `Retry-After` advice in the error
header envelope, preserving nested advice precedence. This does not change
account scheduling or retry policy.

## Telemetry upgrade

See [CODEX_TELEMETRY.md](CODEX_TELEMETRY.md) for analytics schemas, actual auxiliary
header sources and simulation labels. Metric snapshot v3 changes only the four
skills histogram distributions. Restoring v1/v2 drops those old unsealed
distributions, whose raw samples are unavailable, while retaining all other
aggregates, pool identities, startup markers and sealed batches. The existing
v1 byte-histogram compatibility handling remains in effect.

The upgrade uses the existing JSON storage and needs no SQL migration. The
embedded catalog and new protocol behavior take effect when the new binary is
deployed; this implementation does not deploy it automatically.

## Verification

Local verification covers the affected Go test files, WS/telemetry/metadata race
tests, the backend build, frontend tests and production build. Isolated
PostgreSQL/Redis tests exercise v1/v2 migration through real transactions,
multiple runtime instances and restart, retaining existing sealed batches.

The broader pre-existing test suites also contain failures in authorization
fixtures, auth-cache version assertions and older model/guidance expectations;
these reproduce with the original `550aceb35` source overlay. The two existing
model-catalog OAuth401 fixture failures are excluded from the affected-file
regression run after confirming that baseline. No production data is changed
by the verification.
