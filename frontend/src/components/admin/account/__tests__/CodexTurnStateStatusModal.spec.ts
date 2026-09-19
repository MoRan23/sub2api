import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexTurnStateStatus } from '@/api/admin/accounts'

const { getCodexTurnState } = vi.hoisted(() => ({ getCodexTurnState: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getCodexTurnState }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : ''), te: () => true }) }))
import CodexTurnStateStatusModal from '../CodexTurnStateStatusModal.vue'

const status: CodexTurnStateStatus = {
  account_id: 2, owner_account_id: 1, inherited: true, enabled: true,
  account_type: 'auto', resolved_account_type: 'team_business', collector_proxy_id: null,
  expected_length: 332, reason: '',
  models: [{ model: 'gpt-test', state: 'ready', shape: 'target', source: 'business', token_length: 332,
    cipher_blocks: 12, expires_at: '2026-09-19T12:00:00Z', remaining_seconds: 1000, collector_paused: false }]
}
function render() {
  return mount(CodexTurnStateStatusModal, {
    props: { show: true, account: { id: 2, name: 'Spark' } },
    global: {
      stubs: { BaseDialog: defineComponent({ template: '<div><slot/><slot name="footer"/></div>' }) }
    }
  })
}

describe('Codex turn-state status modal', () => {
  beforeEach(() => getCodexTurnState.mockReset())

  it('loads sanitized per-model state and displays parent inheritance', async () => {
    getCodexTurnState.mockResolvedValue(status)
    const wrapper = render()
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.loading')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.inherited{"id":1}')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.characters{"count":332}')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.shapes.target')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.sources.business')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.noCollectorProxy')
    expect(getCodexTurnState).toHaveBeenCalledWith(2, expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('handles failure and retry without leaking an upstream error body', async () => {
    getCodexTurnState.mockRejectedValueOnce(new Error('secret-token')).mockResolvedValueOnce({ ...status, models: [] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.accounts.codexTurnState.loadFailed')
    expect(wrapper.text()).not.toContain('secret-token')
    await wrapper.findAll('button').find(button => button.text() === 'common.refresh')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.empty')
    wrapper.unmount()
  })

  it.each(['model_excluded', 'model_policy_unavailable'] as const)('shows %s without claiming the account cache is disabled', async (state) => {
    getCodexTurnState.mockResolvedValue({ ...status, models: [{ ...status.models[0], model_allowed: false, state }] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain(`admin.accounts.codexTurnState.states.${state}`)
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.disabled')
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.states.ready')
    wrapper.unmount()
  })

  it('ignores a previous account response after selection changes', async () => {
    let resolveFirst!: (value: CodexTurnStateStatus) => void
    getCodexTurnState.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ ...status, inherited: false, expected_length: 0, models: [] })
    const wrapper = render()
    await wrapper.setProps({ account: { id: 3, name: 'New account' } })
    await flushPromises()
    resolveFirst(status)
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.unresolved')
    expect(wrapper.text()).not.toContain('gpt-test')
    wrapper.unmount()
  })
})
