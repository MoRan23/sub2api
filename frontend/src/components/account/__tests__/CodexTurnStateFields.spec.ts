import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CodexTurnStateFields from '../CodexTurnStateFields.vue'
import type { CodexTurnStateConfig, Proxy } from '@/types'
import { codexTurnStateConfigChanged, defaultCodexTurnStateConfig, readCodexTurnStateConfig, supportsCodexTurnState } from '../codexTurnState'
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : '') })
}))

const proxies = [1, 2, 3].map(id => ({ id, name: `Proxy ${id}` }) as Proxy)
function render(inheritedFrom?: number, config: CodexTurnStateConfig = defaultCodexTurnStateConfig()) {
  return mount(CodexTurnStateFields, {
    props: { modelValue: config, proxies, inheritedFrom },
    global: { stubs: { ProxySelector: true } }
  })
}

describe('Codex turn-state configuration', () => {
  it('starts disabled and reveals classification and a non-direct proxy choice when enabled', async () => {
    const wrapper = render()
    expect((wrapper.get('input').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('select').exists()).toBe(false)
    await wrapper.get('input').setValue(true)
    const updated = wrapper.emitted('update:modelValue')?.[0]?.[0]
    expect(updated).toEqual({ enabled: true, account_type: 'auto', collector_proxy_ids: [] })
    await wrapper.setProps({ modelValue: updated as ReturnType<typeof defaultCodexTurnStateConfig> })
    expect(wrapper.get('select').text()).toContain('admin.accounts.codexTurnState.types.team_business')
    expect(wrapper.get('[data-testid="codex-turn-state-bundle-routing-hint"]').text()).toBe('admin.accounts.codexTurnState.bundleRoutingHint')
    expect(wrapper.get('[data-testid="codex-turn-state-proxy-empty"]').text()).toBe('admin.accounts.codexTurnState.noCollectorProxy')
    expect(wrapper.findComponent({ name: 'ProxySelector' }).exists()).toBe(false)
    await wrapper.get('[data-testid="codex-turn-state-proxy-add"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ enabled: true, account_type: 'auto', collector_proxy_ids: [1] })
  })

  it('explains inheritance and prevents edits to shadow settings', async () => {
    const wrapper = render(17, { enabled: true, account_type: 'auto', collector_proxy_ids: [1, 2] })
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.inherited{"id":17}')
    expect(wrapper.get('input').attributes('disabled')).toBeDefined()
    await wrapper.get('input').trigger('change')
    expect(wrapper.get('select').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').every(button => button.attributes('disabled') !== undefined)).toBe(true)
    for (const selector of wrapper.findAllComponents({ name: 'ProxySelector' })) {
      expect(selector.props('disabled')).toBe(true)
      selector.vm.$emit('update:modelValue', 3)
    }
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps an ordered list, filters other selections, and never emits duplicates or empty rows', async () => {
    const original = { enabled: true, account_type: 'personal' as const, collector_proxy_ids: [1, 2] }
    const wrapper = render(undefined, original)
    const selectors = () => wrapper.findAllComponents({ name: 'ProxySelector' })
    expect(selectors()[0]!.props('proxies').map((proxy: Proxy) => proxy.id)).toEqual([1, 3])
    expect(selectors()[1]!.props('proxies').map((proxy: Proxy) => proxy.id)).toEqual([2, 3])
    selectors()[0]!.vm.$emit('update:modelValue', 2)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.findAll('[data-testid="codex-turn-state-proxy-down"]')[0]!.trigger('click')
    let next = wrapper.emitted('update:modelValue')!.at(-1)![0] as CodexTurnStateConfig
    expect(next.collector_proxy_ids).toEqual([2, 1])
    expect(original.collector_proxy_ids).toEqual([1, 2])
    await wrapper.setProps({ modelValue: next })
    await wrapper.findAll('[data-testid="codex-turn-state-proxy-up"]')[1]!.trigger('click')
    next = wrapper.emitted('update:modelValue')!.at(-1)![0] as CodexTurnStateConfig
    expect(next.collector_proxy_ids).toEqual([1, 2])
    await wrapper.setProps({ modelValue: next })
    selectors()[0]!.vm.$emit('update:modelValue', 3)
    next = wrapper.emitted('update:modelValue')!.at(-1)![0] as CodexTurnStateConfig
    expect(next.collector_proxy_ids).toEqual([3, 2])
    await wrapper.setProps({ modelValue: next })
    await wrapper.get('[data-testid="codex-turn-state-proxy-add"]').trigger('click')
    next = wrapper.emitted('update:modelValue')!.at(-1)![0] as CodexTurnStateConfig
    expect(next.collector_proxy_ids).toEqual([3, 2, 1])
    await wrapper.setProps({ modelValue: next })
    expect(wrapper.get('[data-testid="codex-turn-state-proxy-add"]').attributes('disabled')).toBeDefined()
    for (let count = 2; count >= 0; count--) {
      await wrapper.get('[data-testid="codex-turn-state-proxy-remove"]').trigger('click')
      next = wrapper.emitted('update:modelValue')!.at(-1)![0] as CodexTurnStateConfig
      expect(next.collector_proxy_ids).toHaveLength(count)
      await wrapper.setProps({ modelValue: next })
    }
    expect(wrapper.findAllComponents({ name: 'ProxySelector' })).toHaveLength(0)
  })

  it('reads legacy values, honors explicit clearing, and clones arrays for form snapshots and requests', () => {
    const legacy = { enabled: true, account_type: 'auto' as const, collector_proxy_id: 9 }
    expect(readCodexTurnStateConfig(legacy)).toEqual({ enabled: true, account_type: 'auto', collector_proxy_ids: [9] })
    expect(readCodexTurnStateConfig({ ...legacy, collector_proxy_ids: [] }).collector_proxy_ids).toEqual([])
    const source = { ...legacy, collector_proxy_ids: [9, 7] }
    const editable = readCodexTurnStateConfig(source)
    const initial = readCodexTurnStateConfig(source)
    editable.collector_proxy_ids.reverse()
    expect(source.collector_proxy_ids).toEqual([9, 7])
    expect(initial.collector_proxy_ids).toEqual([9, 7])
    expect(codexTurnStateConfigChanged(editable, initial)).toBe(true)
    expect(codexTurnStateConfigChanged(legacy, readCodexTurnStateConfig(legacy))).toBe(false)
    expect(codexTurnStateConfigChanged({ ...legacy, collector_proxy_ids: [] }, legacy)).toBe(true)
  })

  it.each(['personalAccessToken', 'personal_access_token', 'agentIdentity', 'agent_identity'])('excludes unsupported auth mode %s', (mode) => {
    expect(supportsCodexTurnState({ platform: 'openai', type: 'oauth', credentials: { auth_mode: mode } })).toBe(false)
  })
})
