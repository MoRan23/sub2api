import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CodexTurnStateFields from '../CodexTurnStateFields.vue'
import { defaultCodexTurnStateConfig, supportsCodexTurnState } from '../codexTurnState'
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : '') })
}))

function render(inheritedFrom?: number) {
  return mount(CodexTurnStateFields, {
    props: { modelValue: defaultCodexTurnStateConfig(), proxies: [], inheritedFrom },
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
    expect(updated).toEqual({ enabled: true, account_type: 'auto', collector_proxy_id: null })
    await wrapper.setProps({ modelValue: updated as ReturnType<typeof defaultCodexTurnStateConfig> })
    expect(wrapper.get('select').text()).toContain('admin.accounts.codexTurnState.types.team_business')
    expect(wrapper.getComponent({ name: 'ProxySelector' }).props('noProxyLabel')).toBe('admin.accounts.codexTurnState.noCollectorProxy')
  })

  it('explains inheritance and prevents edits to shadow settings', async () => {
    const wrapper = render(17)
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.inherited{"id":17}')
    expect(wrapper.get('input').attributes('disabled')).toBeDefined()
    await wrapper.get('input').trigger('change')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it.each(['personalAccessToken', 'personal_access_token', 'agentIdentity', 'agent_identity'])('excludes unsupported auth mode %s', (mode) => {
    expect(supportsCodexTurnState({ platform: 'openai', type: 'oauth', credentials: { auth_mode: mode } })).toBe(false)
  })
})
