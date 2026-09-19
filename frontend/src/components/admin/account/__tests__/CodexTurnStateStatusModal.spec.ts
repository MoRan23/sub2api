import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
    cipher_blocks: 12, expires_at: '2026-09-19T12:00:00Z', remaining_seconds: 1000, collector_paused: false,
    cache_available: true, collection_status: 'idle', collection_reason: 'idle' }]
}
function render() {
  return mount(CodexTurnStateStatusModal, {
    props: { show: true, account: { id: 2, name: 'Spark' } },
    global: {
      stubs: { BaseDialog: defineComponent({ props: ['width'], template: '<div :data-width="width"><slot/><slot name="footer"/></div>' }) }
    }
  })
}

describe('Codex turn-state status modal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-19T11:30:00Z'))
    getCodexTurnState.mockReset()
  })
  afterEach(() => vi.useRealTimers())

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
    expect(wrapper.get('[data-width]').attributes('data-width')).toBe('extra-wide')
    const columns = wrapper.get('[data-testid="codex-turn-state-status-columns"]')
    expect(columns.classes()).toContain('grid-cols-1')
    expect(columns.classes()).toContain('lg:grid-cols-2')
    expect(columns.element.children[0]?.getAttribute('data-testid')).toBe('codex-turn-state-cache-section')
    expect(columns.element.children[1]?.getAttribute('data-testid')).toBe('codex-turn-state-observations-section')
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
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.cacheEmpty')
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

  it('shows disabled-cache summaries regardless of a legacy observation flag, with shape and validation details separately', async () => {
    getCodexTurnState.mockResolvedValue({ ...status, enabled: false, observation_enabled: false, observation_scope: 'instance', observations: [
      { model: 'gpt-outside-list', observed_at: '2026-09-20T12:00:00Z', outbound_length: 292, response_length: 356,
        response_shape: 'unknown', response_observed_shape: 'team_business_extended', response_cipher_blocks: 13,
        response_validation_reason: 'account_type_unknown', response_source: 'metadata', request_source: 'collector' },
      { model: 'gpt-no-response', observed_at: '2026-09-20T11:30:00Z', outbound_length: 0, response_length: 0, response_shape: 'missing' },
    ] })
    const wrapper = render()
    await flushPromises()
    const observations = wrapper.get('[data-testid="codex-turn-state-observations-section"]')
    expect(observations.text()).toContain('observationScopeHint')
    const outside = observations.get('[data-testid="codex-turn-state-observation-gpt-outside-list"]')
    expect(outside.text()).toContain('characters{"count":356}')
    expect(outside.text()).toContain('observedShapes.team_business_extended')
    expect(outside.text()).toContain('validationReasons.account_type_unknown')
    expect(outside.text()).toContain('sources.response_metadata')
    expect(outside.text()).toContain('requestSource')
    expect(outside.text()).toContain('sources.collector')
    expect(outside.text()).toContain('responseCarrier')
    expect(outside.text()).toContain(new Date('2026-09-20T12:00:00Z').toLocaleString())
    expect(outside.text()).toContain('13')
    expect(observations.text()).toContain('responseStateMissing')
    expect(wrapper.get('[data-testid="codex-turn-state-cache-section"]').text()).toContain('disabled')
    expect(wrapper.find('[data-testid="codex-turn-state-cache-gpt-test"]').exists()).toBe(false)
    expect(observations.text()).not.toContain('states.ready')
    expect(observations.text()).not.toContain('remaining')
    wrapper.unmount()
  })

  it.each([true, false, undefined])('shows an empty instance without requiring a fingerprint observation flag (%s)', async (enabled) => {
    getCodexTurnState.mockResolvedValue({ ...status, enabled: false, observation_enabled: enabled, observation_scope: 'instance', observations: [] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('observationEmpty')
    expect(wrapper.text()).not.toContain('observationDisabled')
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

  it('refreshes every five seconds without clearing content or overlapping a slow request', async () => {
    let resolveRefresh!: (value: CodexTurnStateStatus) => void
    getCodexTurnState.mockResolvedValueOnce(status)
      .mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    const wrapper = render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('gpt-test')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    const refresh = wrapper.get('[data-testid="codex-turn-state-refresh"]')
    expect(refresh.attributes('disabled')).toBeDefined()
    await refresh.trigger('click')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(2)
    resolveRefresh({ ...status, models: [{ ...status.models[0]!, collection_status: 'backoff', collection_reason: 'collector_rate_limited', cache_available: false }] })
    await flushPromises()
    expect(wrapper.text()).toContain('collectionStatuses.backoff')
    expect(wrapper.text()).toContain('reasons.collector_rate_limited')
    expect(wrapper.get('[data-testid="codex-turn-state-cache-availability-gpt-test"]').text()).toContain('cacheUnavailable')
    expect(refresh.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps existing content after a refresh fails and clears the error after recovery', async () => {
    getCodexTurnState.mockResolvedValueOnce(status).mockRejectedValueOnce(new Error('secret-token')).mockResolvedValueOnce(status)
    const wrapper = render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.text()).toContain('gpt-test')
    expect(wrapper.get('[role="alert"]').text()).toContain('refreshFailed')
    expect(wrapper.text()).not.toContain('secret-token')
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('aborts an in-flight refresh and stops polling immediately when closed', async () => {
    let resolveRefresh!: (value: CodexTurnStateStatus) => void
    getCodexTurnState.mockResolvedValueOnce(status).mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    const wrapper = render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    const signal = getCodexTurnState.mock.calls[1]![1] as AbortSignal
    await wrapper.findAll('button').find(button => button.text() === 'common.close')!.trigger('click')
    expect(signal.aborted).toBe(true)
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ show: false })
    resolveRefresh({ ...status, models: [{ ...status.models[0]!, model: 'stale-response' }] })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15_000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('stale-response')
    wrapper.unmount()
  })

  it('shows valid cache independently of paused collection and updates remaining lifetime on the shared refresh clock', async () => {
    const paused = { ...status, models: [{ ...status.models[0]!, expires_at: '2026-09-19T11:30:04Z', state: 'paused', collector_paused: true, collection_status: 'paused', collection_reason: 'collector_auth_rejected' }] }
    getCodexTurnState.mockResolvedValueOnce(paused).mockImplementation(() => new Promise(() => {}))
    const wrapper = render()
    await flushPromises()
    const available = wrapper.get('[data-testid="codex-turn-state-cache-availability-gpt-test"]')
    expect(available.text()).toContain('cacheAvailable')
    expect(wrapper.get('[data-testid="codex-turn-state-collection-gpt-test"]').text()).toContain('collectionStatuses.paused')
    expect(wrapper.text()).toContain('nextCollect')
    await vi.advanceTimersByTimeAsync(5000)
    expect(available.text()).toContain('cacheUnavailable')
    wrapper.unmount()
  })
})
