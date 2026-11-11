import { describe, expect, it } from 'vitest'
import type { PromptAuditConfig } from '../types'
import {
  buildUpdateRequest,
  configToDraft,
  draftFingerprint,
  emptyEventFilters,
  eventFilterPayload,
  hasExplicitDeleteRange,
  SCANNER_CATALOG,
  enforcementPolicyError,
} from '../viewModel'

const config = (): PromptAuditConfig => ({
  enabled: true,
  blocking_enabled: false,
  blocking_latest_turn_only: false,
  keyword_blocking_enabled: false,
  keyword_blocking_active: false,
  blocked_keywords: [],
  keyword_all_groups: true,
  keyword_group_ids: [],
  keyword_whitelist_keys: [],
  store_pass_events: false,
  effective_mode: 'async_audit',
  strategy: 'priority',
  worker_count: 4,
  queue_capacity: 100,
  scanners: SCANNER_CATALOG.map((item) => item.id),
  all_groups: true,
  group_ids: [],
  endpoints: [{
    id: 'guard-1', name: 'Guard One', protocol: 'openai_compatible', base_url: 'http://127.0.0.1:8000',
    model: 'sileader/qwen3guard:0.6b', timeout_ms: 3000, input_limit: 4000, enabled: true,
    has_token: true, token_status: 'configured',
  }],
  config_version: 7,
  updated_at: '2026-07-16T00:00:00Z',
  updated_by: 1,
  change_summary: '{}',
})

describe('Prompt Audit view model', () => {
  it('starts with no policies and only uses templates returned by the server', () => {
    expect(configToDraft(config())).toMatchObject({ combination_policies: [], combination_templates: [] })
    expect(configToDraft({ ...config(), cbrn_topic_rules: null, cbrn_intent_rules: null } as unknown as PromptAuditConfig).combination_policies).toEqual([])
  })

  it('round-trips ordered custom policies and clones templates without saving legacy switches', () => {
    const template = { id: 'government', name: 'Government', enabled: false, topic_rules: ['regex:(?i)\\.gov'], intent_rules: ['attack'], migrate_keywords: ['.gov', '.gov.'] }
    const saved = { ...config(), enabled: false, combination_policies: [
      { id: ' custom-rule ', name: ' Custom policy ', enabled: true, topic_rules: [' Topic ', 'topic', 'regex:(?i)Topic'], intent_rules: ['regex:Build', 'regex:build'], migrate_keywords: [' old ', 'OLD'] },
      template,
    ], combination_templates: [template] }
    const draft = configToDraft(saved)
    const before = draftFingerprint(draft)
    draft.combination_policies[0]!.topic_rules.push('extra')
    draft.combination_templates[0]!.topic_rules.push('template extra')
    expect(saved.combination_policies[0]!.topic_rules).not.toContain('extra')
    expect(template.topic_rules).not.toContain('template extra')
    expect(draftFingerprint(draft)).not.toBe(before)
    const payload = buildUpdateRequest(draft)
    expect(payload).toMatchObject({ enabled: false, keyword_blocking_enabled: false, combination_policies: [
      { id: 'custom-rule', name: 'Custom policy', enabled: true, topic_rules: ['Topic', 'regex:(?i)Topic', 'extra'], intent_rules: ['regex:Build', 'regex:build'], migrate_keywords: ['old'] },
      template,
    ] })
    expect(payload).not.toHaveProperty('combination_templates')
    expect(payload).not.toHaveProperty('cbrn_blocking_enabled')
    expect(payload).not.toHaveProperty('government_blocking_enabled')
    const edited = draftFingerprint(draft)
    draft.combination_policies.reverse()
    expect(draftFingerprint(draft)).not.toBe(edited)
  })

  it('projects old CBRN settings only when the generic list is absent, preserving an explicit empty list', () => {
    const legacy = { ...config(), cbrn_blocking_enabled: true, cbrn_topic_rules: ['Topic'], cbrn_intent_rules: ['intent'] }
    expect(configToDraft(legacy).combination_policies).toEqual([{ id: 'cbrn', name: 'CBRN', enabled: true, topic_rules: ['Topic'], intent_rules: ['intent'], keyword_whitelist_keys: [] }])
    expect(configToDraft({ ...legacy, combination_policies: [] }).combination_policies).toEqual([])
    expect(buildUpdateRequest(configToDraft({ ...legacy, combination_policies: [] })).combination_policies).toEqual([])
  })

  it('round-trips exact user/key pairs and explicitly clears the keyword allowlist', () => {
    const saved = { ...config(), keyword_whitelist_keys: [{ user_id: 7, api_key_id: 12 }] }
    const draft = configToDraft(saved)
    expect(draft.keyword_whitelist_keys).toEqual([{ user_id: 7, api_key_id: 12 }])
    expect(buildUpdateRequest(draft).keyword_whitelist_keys).toEqual([{ user_id: 7, api_key_id: 12 }])
    const previous = draftFingerprint(draft)
    draft.keyword_whitelist_keys[0]!.api_key_id = 13
    expect(saved.keyword_whitelist_keys[0]!.api_key_id).toBe(12)
    expect(draftFingerprint(draft)).not.toBe(previous)
    draft.keyword_whitelist_keys = []
    expect(buildUpdateRequest(draft).keyword_whitelist_keys).toEqual([])
    const { keyword_whitelist_keys: _removed, ...legacy } = saved
    expect(configToDraft(legacy as PromptAuditConfig).keyword_whitelist_keys).toEqual([])
    expect(configToDraft({ ...saved, keyword_whitelist_keys: null } as unknown as PromptAuditConfig).keyword_whitelist_keys).toEqual([])
  })

  it('clones each combination allowlist, saves only IDs, and clears only the edited group', () => {
    const pair = { user_id: 7, api_key_id: 12, key_name: 'display-only', key: 'secret-must-not-copy' }
    const policy = { id: 'government', name: 'Existing government rule', enabled: true, topic_rules: ['regex:(?i)\\.gov'], intent_rules: ['attack'], keyword_whitelist_keys: [pair] }
    const saved = { ...config(), keyword_whitelist_keys: [pair], combination_policies: [policy, { ...policy, id: 'other' }], combination_templates: [policy] }
    const draft = configToDraft(saved)
    expect(draft.combination_policies[0]!.keyword_whitelist_keys).toEqual([{ user_id: 7, api_key_id: 12 }])
    expect(JSON.stringify(draft.combination_policies)).not.toContain('secret-must-not-copy')
    const fingerprint = draftFingerprint(draft)
    draft.combination_policies[0]!.keyword_whitelist_keys![0]!.api_key_id = 13
    expect(draftFingerprint(draft)).not.toBe(fingerprint)
    expect(pair.api_key_id).toBe(12)
    expect(draft.combination_policies[1]!.keyword_whitelist_keys![0]!.api_key_id).toBe(12)
    expect(draft.combination_templates[0]!.keyword_whitelist_keys![0]!.api_key_id).toBe(12)
    draft.combination_policies[0]!.name = 'Renamed existing rule'
    draft.combination_policies[0]!.enabled = false
    const payload = buildUpdateRequest(draft)
    expect(payload.combination_policies[0]).toMatchObject({ name: 'Renamed existing rule', enabled: false, topic_rules: policy.topic_rules, keyword_whitelist_keys: [{ user_id: 7, api_key_id: 13 }] })
    expect(payload.keyword_whitelist_keys).toEqual([{ user_id: 7, api_key_id: 12 }])
    draft.combination_policies[0]!.keyword_whitelist_keys = []
    expect(buildUpdateRequest(draft).combination_policies.map((group) => group.keyword_whitelist_keys)).toEqual([[], [{ user_id: 7, api_key_id: 12 }]])
    draft.combination_policies[1]!.keyword_whitelist_keys = [pair]
    expect(buildUpdateRequest(draft).combination_policies[1]!.keyword_whitelist_keys).toEqual([{ user_id: 7, api_key_id: 12 }])
  })

  it.each([undefined, null, []])('initializes a legacy combination allowlist %s as an empty independent array', (keyword_whitelist_keys) => {
    const policy = { id: 'existing', name: 'Existing', enabled: false, topic_rules: ['original'], intent_rules: ['intent'], keyword_whitelist_keys }
    const draft = configToDraft({ ...config(), combination_policies: [policy], combination_templates: [policy] })
    expect(draft.combination_policies[0]!.keyword_whitelist_keys).toEqual([])
    expect(draft.combination_templates[0]!.keyword_whitelist_keys).toEqual([])
    expect(buildUpdateRequest(draft).combination_policies[0]!.keyword_whitelist_keys).toEqual([])
  })
  it('normalizes legacy null collections from the public config', () => {
    const legacy = { ...config(), keyword_group_ids: null, group_ids: null, scanners: null, endpoints: null } as unknown as PromptAuditConfig
    expect(configToDraft(legacy)).toMatchObject({ keyword_group_ids: [], group_ids: [], scanners: [], endpoints: [] })
  })

  it('inherits the Guard scope when an older server omits keyword scope fields', () => {
    const { keyword_all_groups: _keywordAllGroups, keyword_group_ids: _keywordGroupIds, ...legacy } = {
      ...config(), all_groups: false, group_ids: [7, 3],
    }
    expect(configToDraft(legacy as PromptAuditConfig)).toMatchObject({
      all_groups: false,
      group_ids: [7, 3],
      keyword_all_groups: false,
      keyword_group_ids: [7, 3],
    })
  })

  it('models all nine official input scanners', () => {
    expect(SCANNER_CATALOG).toHaveLength(9)
    expect(SCANNER_CATALOG.map((item) => item.id)).toContain('suicide_and_self_harm')
  })

  it('keeps, replaces, or explicitly clears a saved token without copying plaintext from the server', () => {
    const draft = configToDraft(config())
    expect(draft.endpoints[0].token).toBe('')
    expect(buildUpdateRequest(draft).endpoints[0]).toMatchObject({ token: undefined, clear_token: false })

    draft.endpoints[0].token = 'temporary-canary-token'
    expect(buildUpdateRequest(draft).endpoints[0]).toMatchObject({ token: 'temporary-canary-token', clear_token: false })

    draft.endpoints[0].token = ''
    draft.endpoints[0].clear_token = true
    expect(buildUpdateRequest(draft).endpoints[0]).toMatchObject({ token: undefined, clear_token: true })
  })

  it('includes the optional narrow blocking scope in the update payload', () => {
    const draft = configToDraft(config())
    draft.blocking_latest_turn_only = true
    expect(buildUpdateRequest(draft)).toMatchObject({ blocking_latest_turn_only: true })
  })

  it('round-trips keyword policy state and includes canonical payload fields', () => {
    const draft = configToDraft({
      ...config(), keyword_blocking_enabled: true, keyword_blocking_active: true,
      blocked_keywords: ['  Alpha ', '', 'Beta'], keyword_all_groups: false, keyword_group_ids: [9, 3],
    })
    expect(draft.jailbreak_keywords).toEqual(['  Alpha ', '', 'Beta'])
    expect(buildUpdateRequest(draft)).toMatchObject({
      keyword_blocking_enabled: true,
      jailbreak_keywords: ['Alpha', 'Beta'],
      keyword_all_groups: false,
      keyword_group_ids: [3, 9],
    })
  })

  it('tracks dirty state from the full normalized save payload', () => {
    const original = configToDraft(config())
    const changed = configToDraft(config())
    expect(draftFingerprint(changed)).toBe(draftFingerprint(original))
    changed.queue_capacity += 1
    expect(draftFingerprint(changed)).not.toBe(draftFingerprint(original))
  })

  it('saves regex case and 2000-character rules without folding literal namespaces', () => {
    const longRule = 'regex:' + 'a'.repeat(1994)
    const draft = configToDraft({ ...config(), blocked_keywords: [' Alpha ', 'alpha', 'regex:Alpha', 'regex:alpha', 'REGEX:Alpha', 'regex:Alpha', longRule] })
    expect(buildUpdateRequest(draft).jailbreak_keywords).toEqual(['Alpha', 'regex:Alpha', 'regex:alpha', 'REGEX:Alpha', longRule])
  })

  it('migrates legacy keywords to jailbreak and initializes enforcement defaults', () => {
    const draft = configToDraft({ ...config(), blocked_keywords: ['old-rule'] })
    expect(draft).toMatchObject({ jailbreak_keywords: ['old-rule'], other_keywords: [], warning_email_enabled: true,
      warning_email_window_minutes: 5, auto_ban_enabled: true, violation_window_hours: 24, ban_threshold: 10 })
    expect(buildUpdateRequest(draft)).not.toHaveProperty('blocked_keywords')
  })

  it.each([
    ['warning_email_window_minutes', 0, 'prompt_audit_invalid_warning_email_window'],
    ['warning_email_window_minutes', 61, 'prompt_audit_invalid_warning_email_window'],
    ['violation_window_hours', 721, 'prompt_audit_invalid_violation_window'],
    ['ban_threshold', 1.5, 'prompt_audit_invalid_ban_threshold'],
    ['ban_threshold', Number.NaN, 'prompt_audit_invalid_ban_threshold'],
  ] as const)('rejects invalid enforcement setting %s=%s', (field, value, expected) => {
    const draft = configToDraft(config())
    draft[field] = value
    expect(enforcementPolicyError(draft)).toBe(expected)
  })

  it.each([[], null])('does not restore retired legacy keywords over an explicit new list %s', (keywords) => {
    const draft = configToDraft({ ...config(), blocked_keywords: ['retired'], jailbreak_keywords: keywords,
      other_keywords: [' Other ', 'other'], warning_email_enabled: false, auto_ban_enabled: false,
      warning_email_window_minutes: 12, violation_window_hours: 48, ban_threshold: 20 })
    expect(buildUpdateRequest(draft)).toMatchObject({ jailbreak_keywords: [], other_keywords: ['Other'],
      warning_email_enabled: false, auto_ban_enabled: false, warning_email_window_minutes: 12, violation_window_hours: 48, ban_threshold: 20 })
    const fingerprint = draftFingerprint(draft)
    draft.other_keywords = []
    expect(draftFingerprint(draft)).not.toBe(fingerprint)
  })

  it('requires a valid explicit range and sends canonical ISO timestamps for filter deletion', () => {
    const filters = emptyEventFilters()
    expect(hasExplicitDeleteRange(filters)).toBe(false)
    filters.start_at = '2026-07-15T10:00'
    filters.end_at = '2026-07-16T10:00'
    filters.group_id = '9'
    expect(hasExplicitDeleteRange(filters)).toBe(true)
    expect(eventFilterPayload(filters)).toMatchObject({
      group_id: 9,
      start_at: new Date(filters.start_at).toISOString(),
      end_at: new Date(filters.end_at).toISOString(),
    })
  })
})
