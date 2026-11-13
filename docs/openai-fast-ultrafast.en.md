# OpenAI Fast / Ultrafast controls

## Configuration

OpenAI and Composite groups support Force Fast, Force Ultrafast, Free Fast and Free Ultrafast. OpenAI OAuth, ordinary API Key, Codex-Engine and shadow accounts support the same preferences. New fields default to false; existing group Fast settings are preserved.

Account force settings take precedence over the group, then the client. Force Fast and Force Ultrafast are mutually exclusive within each scope. To change tiers through a partial API update, explicitly disable the old force flag and enable the new one. The two free flags are independent and effective when either the business account or group enables them. Shadows use their own preferences.

Saving affects new requests. Existing WebSocket sessions retain their settings until reconnection; each turn still checks capability freshness.

## Outbound behavior

Fast selects `service_tier="priority"`. Forced Ultrafast selects `service_tier="ultrafast"` only when the selected account's raw upstream catalog explicitly declares `service_tiers[].id == "ultrafast"` for the final mapped model. OAuth uses its authorization slot; API Key and Engine use their own upstreams.

ID-only, missing, empty, degraded, stale or failed catalogs preserve the client tier. Account Ultrafast never falls back to group Force Fast. Public catalogs, synthetic descriptors, model-family guesses and `additional_speed_tiers` are not capability evidence. Explicit client tiers retain existing handling.

Refresh builds an immutable capability index. Cache hits share this small index without cloning catalog bodies. Observations expire after 60 seconds; stale data triggers the existing refresh while skipping this attempt. Cold queries wait at most two seconds and never alter account health. Existing URL, key, proxy and authorization isolation applies. Disabling forced Ultrafast avoids its catalog queries.

Global Fast/Flex rules remain authoritative; OAuth Guardian still removes the tier. No fields are added to independent Images or auxiliary requests. Retries and later WS frames use their own original bodies. Unchanged tier decisions reuse the body.

## Billing and diagnostics

Free means the customer pays the equivalent Standard price rather than the acceleration premium. Actual upstream tier, original cost, account cost and account quota accounting remain unchanged. Existing channel, long-context, peak and user multipliers still apply. Free Fast covers priority/Fast only; Free Ultrafast covers Ultrafast only.

Queued billing stores compact flags without retaining complete extra maps or request bodies. Engine settlement snapshots preserve the same flags; missing fields in old snapshots default to false. Existing pricing lookup timing remains unchanged.

Fingerprint details show client tier, force source, forced tier, actual outbound tier and the capability decision. Observations are isolated per physical request or WS turn. Historical records are not inferred from current configuration.

## Storage and rollout

Migration `282_group_openai_ultrafast.sql` adds two default-false group columns. Authentication cache format advances to version 29. Account preferences remain booleans in `extra` under `force_openai_fast`, `force_openai_ultrafast`, `free_openai_fast`, and `free_openai_ultrafast`. Missing updates preserve existing values; repository locks recheck the effective force pair and protect settings from stale background snapshots.

Bulk forms submit only opted-in fields and treat the force pair atomically. Existing create, edit, copy, import and export flows are reused. Application rollback can retain the added columns and extra values; older code ignores the new controls. Disable business settings that depend on the new behavior before rollback.

No deployment, production model calls, Engine project changes or production configuration changes are part of this delivery. See the [verification record](openai-fast-ultrafast-verification.md).
