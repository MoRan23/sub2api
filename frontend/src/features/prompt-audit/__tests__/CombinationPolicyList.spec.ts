import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CombinationPolicyList from '../components/CombinationPolicyList.vue'
import type { PromptCombinationPolicy, PromptKeywordWhitelistIdentity, PromptKeywordWhitelistKey } from '../types'

const api = vi.hoisted(() => ({ users: vi.fn(), keys: vi.fn(), identities: vi.fn() }))
vi.mock('../api', () => ({ listKeywordWhitelistUsers: api.users, listKeywordWhitelistKeys: api.keys, getKeywordWhitelistIdentities: api.identities }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => values ? `${key} ${JSON.stringify(values)}` : key }) }))

function policy(id: string): PromptCombinationPolicy {
  return { id, name: `Policy ${id}`, enabled: false, topic_rules: ['topic'], intent_rules: ['intent'], migrate_keywords: [], keyword_whitelist_keys: [] }
}

function setup(modelValue: PromptCombinationPolicy[] = [], templates: PromptCombinationPolicy[] = []) {
  const wrapper = mount(CombinationPolicyList, { props: { modelValue, templates } })
  const accept = async () => {
    const updated = wrapper.emitted('update:modelValue')!.at(-1)![0] as PromptCombinationPolicy[]
    await wrapper.setProps({ modelValue: updated })
    return updated
  }
  return { wrapper, accept }
}

describe('Combination policy editor', () => {
  beforeEach(() => {
    api.users.mockReset().mockResolvedValue({ items: [{ id: 1, email: 'owner@example.test', username: 'Owner' }], total: 1 })
    api.keys.mockReset().mockResolvedValue({ items: [{ id: 11, user_id: 1, name: 'main-key' }], total: 1 })
    api.identities.mockReset().mockImplementation(async (keys: PromptKeywordWhitelistKey[]) => keys.map((key) => ({ ...key, user_email: `owner-${key.user_id}@example.test`, username: `User ${key.user_id}`, key_name: `key-${key.api_key_id}`, available: true })))
  })

  it('adds a disabled custom group and edits its name and rule lists without losing typed newlines', async () => {
    const { wrapper, accept } = setup()
    await wrapper.get('[data-test="combination-add-custom"]').trigger('click')
    const created = (await accept())[0]!
    expect(created.id).toMatch(/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/)
    expect(created).toMatchObject({ enabled: false, topic_rules: [], intent_rules: [], migrate_keywords: [] })
    await wrapper.get('[data-test="combination-name"]').setValue('Customer-provided group')
    await accept()
    const topic = wrapper.get<HTMLTextAreaElement>('[data-test="combination-topic-editor"] textarea')
    await topic.setValue('first\n')
    await accept()
    expect(topic.element.value).toBe('first\n')
    await topic.setValue('first\nregex:(?i)second')
    await accept()
    await wrapper.get('[data-test="combination-intent-editor"] textarea').setValue('intent\nregex:OtherIntent')
    await accept()
    await wrapper.get('[data-test="combination-enabled"]').setValue(true)
    const enabled = (await accept())[0]!
    expect(enabled).toEqual({ id: created.id, name: 'Customer-provided group', enabled: true, topic_rules: ['first', 'regex:(?i)second'], intent_rules: ['intent', 'regex:OtherIntent'], migrate_keywords: [], keyword_whitelist_keys: [] })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('[data-test="combination-enabled"]').setValue(false)
    expect((await accept())[0]!.enabled).toBe(false)
  })

  it('clones backend templates, prevents duplicate IDs, reorders and removes groups', async () => {
    const templates = [policy('cbrn'), { ...policy('government'), migrate_keywords: ['.gov', '.gov.'] }]
    const { wrapper, accept } = setup([], templates)
    const templateButton = (id: string) => wrapper.get(`[data-test="combination-add-template"][data-template-id="${id}"]`)
    await templateButton('government').trigger('click')
    const added = (await accept())[0]!
    expect(added).toEqual(templates[1])
    expect(added).not.toBe(templates[1])
    expect(added.topic_rules).not.toBe(templates[1]!.topic_rules)
    expect(templateButton('government').attributes()).toHaveProperty('disabled')
    await templateButton('cbrn').trigger('click')
    await accept()
    let cards = wrapper.findAll('[data-test="combination-policy"]')
    expect(cards[0]!.get('[data-test="combination-up"]').attributes()).toHaveProperty('disabled')
    expect(cards[1]!.get('[data-test="combination-down"]').attributes()).toHaveProperty('disabled')
    await cards[1]!.get('[data-test="combination-up"]').trigger('click')
    expect((await accept()).map((group) => group.id)).toEqual(['cbrn', 'government'])
    cards = wrapper.findAll('[data-test="combination-policy"]')
    await cards[0]!.get('[data-test="combination-down"]').trigger('click')
    expect((await accept()).map((group) => group.id)).toEqual(['government', 'cbrn'])
    await wrapper.findAll('[data-test="combination-remove"]')[0]!.trigger('click')
    expect((await accept()).map((group) => group.id)).toEqual(['cbrn'])
    expect(templateButton('government').attributes()).not.toHaveProperty('disabled')
    await wrapper.get('[data-test="combination-remove"]').trigger('click')
    expect(await accept()).toEqual([])
    expect(wrapper.text()).toContain('admin.promptAudit.combination.empty')
  })

  it('allows only literal migration entries and preserves newline entry', async () => {
    const { wrapper, accept } = setup([policy('project')])
    const migration = wrapper.get<HTMLTextAreaElement>('[data-test="combination-migrate-editor"] textarea')
    await migration.setValue(' old keyword\n')
    await accept()
    expect(migration.element.value).toBe(' old keyword\n')
    await migration.setValue(' old keyword\n.gov.\n')
    expect((await accept())[0]!.migrate_keywords).toEqual(['old keyword', '.gov.'])
    expect(wrapper.find('[data-test="combination-migrate-editor"] [role="alert"]').exists()).toBe(false)
    await migration.setValue('REGEX:pattern')
    await accept()
    expect(wrapper.get('[data-test="combination-migrate-editor"] [role="alert"]').text()).toContain('prompt_audit_invalid_combination_migration_keyword')
  })

  it('selects a user and their named key only for the chosen combination', async () => {
    const { wrapper, accept } = setup([policy('first'), policy('second')])
    const card = wrapper.get('[data-policy-id="second"]')
    expect(card.text()).toContain('admin.promptAudit.combination.whitelistDescription')
    await card.get('[data-test="search"]').trigger('click')
    await flushPromises()
    expect(card.get('[data-test="user"]').text()).toContain('owner@example.test / Owner')
    await card.get('[data-test="user"]').setValue('1')
    await flushPromises()
    await card.get('[data-test="key"]').setValue('11')
    await card.get('[data-test="add-key"]').trigger('click')
    expect((await accept()).map((group) => group.keyword_whitelist_keys)).toEqual([[], [{ user_id: 1, api_key_id: 11 }]])
    await flushPromises()
    expect(card.get('[data-test="saved-entry"]').text()).toContain('owner-1@example.test / User 1 · key-11')
    await card.get('[data-test="combination-name"]').setValue('Edited name')
    expect((await accept())[1]).toMatchObject({ name: 'Edited name', keyword_whitelist_keys: [{ user_id: 1, api_key_id: 11 }] })
    await card.get('[data-test="remove-key"]').trigger('click')
    expect((await accept()).map((group) => group.keyword_whitelist_keys)).toEqual([[], []])
    wrapper.unmount()
  })

  it('keeps identity requests attached to policy IDs while moving, editing, and deleting groups', async () => {
    let resolveFirst!: (value: PromptKeywordWhitelistIdentity[]) => void
    let resolveRemoved!: (value: PromptKeywordWhitelistIdentity[]) => void
    const first = new Promise<PromptKeywordWhitelistIdentity[]>((resolve) => { resolveFirst = resolve })
    const removed = new Promise<PromptKeywordWhitelistIdentity[]>((resolve) => { resolveRemoved = resolve })
    api.identities.mockImplementation((keys: PromptKeywordWhitelistKey[]) => keys[0]?.user_id === 1 ? first : removed)
    const pairs = [{ user_id: 1, api_key_id: 11 }, { user_id: 2, api_key_id: 22 }]
    const { wrapper, accept } = setup([{ ...policy('first'), keyword_whitelist_keys: [pairs[0]!] }, { ...policy('second'), keyword_whitelist_keys: [pairs[1]!] }])
    await wrapper.get('[data-policy-id="first"] [data-test="combination-down"]').trigger('click')
    await accept()
    await wrapper.get('[data-policy-id="second"] [data-test="combination-name"]').setValue('Changed second')
    await accept()
    const updatesBeforeMetadata = wrapper.emitted('update:modelValue')!.length
    resolveFirst([{ ...pairs[0]!, user_email: 'first@example.test', username: 'First', key_name: 'first-key', available: true }])
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toHaveLength(updatesBeforeMetadata)
    expect(wrapper.get('[data-policy-id="first"]').text()).toContain('first@example.test / First · first-key')
    expect(wrapper.get('[data-policy-id="second"]').text()).not.toContain('first@example.test')
    expect(wrapper.props('modelValue')).toMatchObject([{ id: 'second', name: 'Changed second', keyword_whitelist_keys: [pairs[1]] }, { id: 'first', keyword_whitelist_keys: [pairs[0]] }])
    await wrapper.get('[data-policy-id="second"] [data-test="combination-remove"]').trigger('click')
    await accept()
    resolveRemoved([{ ...pairs[1]!, user_email: 'removed@example.test', username: '', key_name: 'removed-key', available: true }])
    await flushPromises()
    expect(wrapper.props('modelValue')).toMatchObject([{ id: 'first', keyword_whitelist_keys: [pairs[0]] }])
    expect(wrapper.emitted('update:modelValue')).toHaveLength(updatesBeforeMetadata + 1)
    expect(wrapper.text()).not.toContain('removed@example.test')
    wrapper.unmount()
  })

  it('preserves existing rules while displaying the server’s updated government template and filtering help', async () => {
    const existing = { ...policy('government'), topic_rules: ['regex:(?i)\\.gov'] }
    const template = { ...policy('government'), topic_rules: ['regex:(?i)\\.gov\\.cn'] }
    const { wrapper, accept } = setup([existing], [template])
    expect(wrapper.get('[data-test="combination-path-filter-hint"]').text()).toContain('admin.promptAudit.combination.pathFilterHint')
    expect(wrapper.get('[data-test="combination-government-template-hint"]').text()).toContain('admin.promptAudit.combination.governmentTemplateHint')
    expect(wrapper.props('modelValue')[0]!.topic_rules).toEqual(existing.topic_rules)
    expect(wrapper.get('[data-test="combination-add-template"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="combination-remove"]').trigger('click')
    await accept()
    await wrapper.get('[data-test="combination-add-template"]').trigger('click')
    expect((await accept())[0]).toMatchObject({ enabled: false, topic_rules: template.topic_rules, keyword_whitelist_keys: [] })
    wrapper.unmount()
  })

  it('validates enabled empty groups, names, individual rule lengths and the aggregate limit', async () => {
    const empty = { ...policy('empty'), enabled: true, name: ' ', topic_rules: [], intent_rules: [] }
    const { wrapper } = setup([empty])
    expect(wrapper.text()).toContain('prompt_audit_invalid_combination_policy_name')
    expect(wrapper.get('[data-test="combination-topic-editor"] [role="alert"]').text()).toContain('prompt_audit_combination_rules_required')
    expect(wrapper.get('[data-test="combination-intent-editor"] [role="alert"]').text()).toContain('prompt_audit_combination_rules_required')
    await wrapper.setProps({ modelValue: [{ ...policy('control'), name: 'A\u0001B' }] })
    expect(wrapper.text()).toContain('prompt_audit_invalid_combination_policy_name')
    await wrapper.setProps({ modelValue: [{ ...policy('length'), topic_rules: ['a'.repeat(201)], intent_rules: ['regex:' + 'a'.repeat(1995)] }] })
    expect(wrapper.get('[data-test="combination-topic-editor"] [role="alert"]').text()).toContain('prompt_audit_keyword_too_long')
    expect(wrapper.get('[data-test="combination-intent-editor"] [role="alert"]').text()).toContain('prompt_audit_keyword_regex_too_long')
    await wrapper.setProps({ modelValue: [
      { ...policy('one'), topic_rules: Array.from({ length: 5000 }, (_, i) => `topic-${i}`), intent_rules: [] },
      { ...policy('two'), topic_rules: [], intent_rules: Array.from({ length: 5001 }, (_, i) => `intent-${i}`) },
    ] })
    expect(wrapper.text()).toContain('prompt_audit_too_many_combination_rules')
  })

  it('counts repeated pairs separately across groups toward the combination allowlist budget', async () => {
    const keys = Array.from({ length: 1000 }, (_, index) => ({ user_id: 1, api_key_id: index + 1 }))
    const policies = Array.from({ length: 10 }, (_, index) => ({ ...policy(`group-${index}`), keyword_whitelist_keys: keys }))
    const wrapper = mount(CombinationPolicyList, { props: { modelValue: policies, templates: [] }, global: { stubs: { KeywordKeyWhitelist: true } } })
    expect(wrapper.find('[data-test="combination-whitelist-limit"]').exists()).toBe(false)
    await wrapper.setProps({ modelValue: [...policies, { ...policy('extra'), keyword_whitelist_keys: [keys[0]!] }] })
    expect(wrapper.get('[data-test="combination-whitelist-limit"]').text()).toContain('prompt_audit_too_many_combination_whitelist_keys')
    wrapper.unmount()
  })
})
