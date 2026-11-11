import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import CombinationPolicyList from '../components/CombinationPolicyList.vue'
import type { PromptCombinationPolicy } from '../types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => values ? `${key} ${JSON.stringify(values)}` : key }) }))

function policy(id: string): PromptCombinationPolicy {
  return { id, name: `Policy ${id}`, enabled: false, topic_rules: ['topic'], intent_rules: ['intent'], migrate_keywords: [] }
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
    expect(enabled).toEqual({ id: created.id, name: 'Customer-provided group', enabled: true, topic_rules: ['first', 'regex:(?i)second'], intent_rules: ['intent', 'regex:OtherIntent'], migrate_keywords: [] })
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
})
