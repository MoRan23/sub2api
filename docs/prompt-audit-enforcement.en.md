# Prompt audit: keyword categories, violation counts, and automatic bans

This document describes enforcement for keyword categories and combination rules, including upgrade and rollback requirements. Configuration continues to use the administrator prompt-audit page and `GET/PUT /api/v1/admin/prompt-audit/config`.

## Configuration and defaults

| Field | Default | Meaning / range |
| --- | --- | --- |
| `jailbreak_keywords` | All existing `blocked_keywords` entries | Jailbreak keywords; literal text or `regex:`, one entry per line |
| `other_keywords` | `[]` | Other keywords; blocking and audit events only |
| `warning_email_enabled` | `true` | Create warning-mail tasks for new jailbreak / combination hits |
| `warning_email_window_minutes` | `5` | Fixed email aggregation window, 1–60 minutes |
| `auto_ban_enabled` | `true` | Disable ordinary users when a new hit reaches the threshold |
| `violation_window_hours` | `24` | Rolling counting window, 1–720 hours |
| `ban_threshold` | `10` | Automatic-ban threshold, 1–10,000 hits |

The two normalized keyword lists together allow at most 10,000 entries. Each literal entry allows 200 Unicode characters; a complete regex rule, including the `regex:` prefix, allows 2,000 Unicode characters and uses the existing Go/RE2 validation. `keyword_blocking_enabled` controls both keyword categories. Each entry in `combination_policies` retains its own `enabled` switch. The existing risk-control switch, keyword group scope, and exact user-plus-API-Key whitelist still apply.

Omitted new fields preserve their saved values on update. Explicit `jailbreak_keywords: []` or `other_keywords: []` clears that list; disable the keyword switch when clearing both lists. Legacy clients can still write `blocked_keywords` as the jailbreak list; when both fields are supplied, `jailbreak_keywords` takes precedence. Existing entries are not classified by their content. Configured combination-rule migrations can still remove explicitly designated exact entries from either list.

After upgrade, previously enabled keywords operate as jailbreak rules with the default enforcement settings. Review their classification before enabling the release. Move entries that should retain ordinary keyword behavior into **Other keywords**.

## Matching, responses, and counting

Local matching order is **jailbreak keywords → topic-and-intent combination rules → other keywords**, followed by the existing audit flow. This priority selects one actual block; multiple matching words or categories in one request produce only one enforcement hit.

- **Jailbreak keywords:** HTTP 403, `prompt_keyword_blocked`. The response warns against jailbreak attempts and reports the committed count within the window. It does not reveal the keyword, matching text, or regex.
- **Combination rules:** HTTP 403, `prompt_combination_blocked`. The response includes only the rule-group name, a stop warning, and the count. Topic/intent excerpts and rule expressions are withheld.
- **Other keywords:** Keep the existing HTTP 403 and actual matched-text message without exposing a complete regex. They create audit events only: no count toward this enforcement policy, no automatic ban, and no warning email from this feature.
- External Guard events retain their existing behavior and do not count toward this policy. External responses cannot populate reserved local-category or matching-evidence fields.

Counts are aggregated per user across their API Keys and the two enforced categories. HTTP requests and actual WS turns receive server-generated identifiers; the database deduplicates by user plus identifier. Retries, duplicate completion, and duplicate submissions from multiple instances do not count the same request again. Client-provided request IDs do not control this identity. A duplicate returns the original committed count snapshot.

By default, the tenth hit in the current counting cycle's rolling 24-hour window disables the user. Saving policy settings does not retrospectively ban users: the next qualifying new hit evaluates the current threshold. Administrator-role users are still blocked, recorded, and counted, but this feature does not automatically disable them. Users already disabled for another reason are not taken over by prompt auditing.

The audit event, hit ledger, user status, and email task commit in one transaction. If the transaction fails, the request remains blocked and the response reports that the recorded count is unavailable. It does not claim a successful record or ban. The server also freezes the request start time: a request that began before unbanning but finishes afterward is recorded without contributing to the new cycle.

## Switches and history

| Action | Subsequent behavior |
| --- | --- |
| Disable the keyword switch | Stop both keyword lists; enabled combination groups continue |
| Disable a combination group | Stop new checks for that group; other rules continue |
| Disable `warning_email_enabled` | Continue blocking, counting, and configured bans; do not enqueue new email tasks. Already committed tasks continue |
| Disable `auto_ban_enabled` | Continue blocking, counting, and configured warnings; do not impose new automatic bans |
| Delete an audit event | Remove that audit detail without resetting enforcement counts |

These switches and the passage of the rolling window do not reactivate disabled users. The ledger is retained independently of audit-detail deletion for at least the maximum configurable window plus one overlapping day (31 days); records still linked to audit events remain longer. Old history without the new category or enforcement snapshot displays as unclassified / not recorded. It is not reclassified or counted retrospectively.

Event responses add optional `keyword_kind` (`jailbreak` or `other`) and `enforcement`. `violation_count`, `window_hours`, `ban_threshold`, `auto_banned`, and `epoch` describe the original event. `user_status` and `can_unban` are read from the user's current status and ban ownership. Email states are `not_requested`, `queued`, `sending`, `sent`, `retry`, `failed`, and `cancelled`.

## Quick unban and explicit administrator actions

The administrator UI offers quick unban according to the current `can_unban` value:

```text
POST /api/v1/admin/prompt-audit/users/:user_id/unban
```

Unban validates the user's **current prompt-audit ban ownership and revision**, not the epoch of the historical event being viewed. Success restores `active`, starts a new counting cycle, clears automatic-ban ownership, and cancels unsent mail from the old cycle. Historical events and ledger entries remain. An already-active user returns `already_active`; repeated quick unban does not reset the cycle again. A disablement not currently owned by prompt auditing returns HTTP 409, `prompt_audit_unban_ownership`, and must be handled in user management.

An explicit status write in user management, including another write of `disabled`, revokes prompt-audit unban ownership. An explicit `active` write additionally starts a new cycle and cancels pending old-cycle mail. Profile updates that do not explicitly change status preserve ownership. Automatic bans follow the normal user-disable rule for revoking administrative MCP grants; reactivation does not revive revoked grants. Committed status changes refresh the user's authentication cache.

## Email aggregation and delivery

An unsent warning for the same user and cycle waits for a fixed window from its first hit, five minutes by default. Further hits merge without pushing the deadline back. Reaching the ban threshold promotes a pending digest into an immediately claimable ban notification. Mail contains categories, optional rule-group names, counts, window, threshold, and account status. It excludes original prompts, keywords, regexes, and matched excerpts.

Delivery uses a durable database outbox, a two-minute claim lease, and version checks. SMTP runs outside the transaction with a 45-second delivery-call timeout. Each pending delivery version has at most **five delivery attempts, including the first**, with bounded backoff before reaching `failed`. Hits arriving during delivery remain pending; completion acknowledges only the claimed portion. Unfinished tasks can resume after a process restart. If SMTP accepts a message but the database acknowledgement fails, a later delivery can repeat it; this acknowledgement boundary does not duplicate hit counts or ban transactions.

Unbanning or explicitly enabling a user invalidates old-cycle mail that has not started. Messages already handed to SMTP cannot be recalled. Disabling the mail switch affects new enqueueing only; check task status for existing messages.

## Upgrade and rollback

1. Back up the database, prompt-audit configuration, and current application. Review migrated keyword categories and default enforcement settings in a test environment.
2. Drain old processes and switch application instances together. Avoid mixed versions writing keyword configuration, user status, or enforcement state.
3. Apply forward migration `281_prompt_audit_enforcement.sql` at startup. It only adds the enforcement-state, hit-ledger, and notification tables and their indexes. It does not rewrite historical events or reactivate users.
4. Check administrator configuration, rule categories, ordinary-keyword behavior, unban controls, and email task status. Validate with synthetic requests, test users, and a test mail channel before enabling the intended live policy.

For rollback, stop the new processes and restore a matching old application and configuration backup. Old versions understand `blocked_keywords`, but not the two-category model or new enforcement behavior. Keep the new configuration backup for a later return to this release. The added tables can remain for future upgrades; the old application does not continue this feature's counting, automatic bans, or email processing. Existing database users with `disabled` status stay disabled after rollback and require administrator review. If restoring the entire database, use a consistent backup matching the application and plan how to recover business changes made afterward.

This is an operating guide for the local implementation. Deployment and real SMTP delivery require acceptance checks in the target environment.
