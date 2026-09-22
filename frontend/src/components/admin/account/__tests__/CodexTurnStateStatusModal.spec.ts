import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexTurnStateStatus } from '@/api/admin/accounts'

const { getCodexTurnState, getProxies } = vi.hoisted(() => ({ getCodexTurnState: vi.fn(), getProxies: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getCodexTurnState }))
vi.mock('@/api/admin/proxies', () => ({ getAll: getProxies }))
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
    getProxies.mockReset().mockResolvedValue([])
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
    expect(getCodexTurnState).toHaveBeenCalledWith(2, expect.any(AbortSignal), 'windows')
    expect(wrapper.get('[data-width]').attributes('data-width')).toBe('extra-wide')
    const columns = wrapper.get('[data-testid="codex-turn-state-status-columns"]')
    expect(columns.classes()).toContain('grid-cols-1')
    expect(columns.classes()).toContain('lg:grid-cols-2')
    expect(columns.element.children[0]?.getAttribute('data-testid')).toBe('codex-turn-state-cache-section')
    expect(columns.element.children[1]?.getAttribute('data-testid')).toBe('codex-turn-state-observations-section')
    wrapper.unmount()
  })

  it('selects the account default system and preserves the selection on auto refresh', async () => {
    getCodexTurnState.mockResolvedValue(status)
    const wrapper = render()
    await wrapper.setProps({ account: { id: 3, name: 'Linux default', openai_oauth_os_profiles: { default_os: 'linux', profiles: {} } } as any })
    await flushPromises()
    expect(getCodexTurnState).toHaveBeenLastCalledWith(3, expect.any(AbortSignal), 'linux')
    await wrapper.get('[data-testid="openai-oauth-os-select"]').setValue('macos')
    await flushPromises()
    expect(getCodexTurnState).toHaveBeenLastCalledWith(3, expect.any(AbortSignal), 'macos')
    await vi.advanceTimersByTimeAsync(5000)
    expect(getCodexTurnState).toHaveBeenLastCalledWith(3, expect.any(AbortSignal), 'macos')
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

  it('shows each model route and count using directory names without proxy addresses or credentials', async () => {
    getProxies.mockResolvedValue([{ id: 7, name: 'Collector A', host: 'private-proxy-host', password: 'private-password' }, { id: 9, name: 'Collector B' }])
    getCodexTurnState.mockResolvedValue({ ...status, inherited: false, collector_proxy_ids: [7, 9], models: [
      { ...status.models[0], collector_proxy_id: 9, last_collector_proxy_id: 7, collector_extended_count: 2 },
      { ...status.models[0], model: 'gpt-second', collector_proxy_id: 7, last_collector_proxy_id: 9, collector_extended_count: 0 },
    ] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="codex-turn-state-current-proxy-gpt-test"]').text()).toBe('Collector B')
    expect(wrapper.get('[data-testid="codex-turn-state-last-proxy-gpt-test"]').text()).toBe('Collector A')
    expect(wrapper.get('[data-testid="codex-turn-state-proxy-count-gpt-test"]').text()).toBe('2 / 3')
    expect(wrapper.get('[data-testid="codex-turn-state-current-proxy-gpt-second"]').text()).toBe('Collector A')
    expect(wrapper.get('[data-testid="codex-turn-state-proxy-count-gpt-second"]').text()).toBe('0 / 3')
    expect(wrapper.text()).not.toContain('noCollectorProxy')
    expect(wrapper.text()).not.toContain('private-proxy-host')
    expect(wrapper.text()).not.toContain('private-password')
    wrapper.unmount()
  })

  it('honors an explicit empty list over a legacy proxy and safely falls back when names cannot load', async () => {
    getProxies.mockRejectedValue(new Error('private-directory-error'))
    getCodexTurnState.mockResolvedValue({ ...status, collector_proxy_ids: [], collector_proxy_id: 7, models: [
      { ...status.models[0], last_collector_proxy_id: 9 },
    ] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('noCollectorProxy')
    expect(wrapper.get('[data-testid="codex-turn-state-current-proxy-gpt-test"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="codex-turn-state-last-proxy-gpt-test"]').text()).toBe('admin.accounts.codexTurnState.proxyFallback{"id":9}')
    expect(wrapper.text()).not.toContain('private-directory-error')
    wrapper.unmount()
  })

  it('aborts the directory request and ignores its late result on account changes', async () => {
    let resolveDirectory!: (value: { id: number; name: string }[]) => void
    getProxies.mockImplementationOnce(() => new Promise(resolve => { resolveDirectory = resolve })).mockResolvedValueOnce([{ id: 7, name: 'Current name' }])
    getCodexTurnState.mockResolvedValue({ ...status, collector_proxy_ids: [7] })
    const wrapper = render()
    await flushPromises()
    const signal = getProxies.mock.calls[0]![0] as AbortSignal
    await wrapper.setProps({ account: { id: 3, name: 'New account' } })
    await flushPromises()
    expect(signal.aborted).toBe(true)
    resolveDirectory([{ id: 7, name: 'Stale name' }])
    await flushPromises()
    expect(wrapper.get('[data-testid="codex-turn-state-current-proxy-gpt-test"]').text()).toBe('Current name')
    expect(wrapper.text()).not.toContain('Stale name')
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

  it('keeps response model evidence separate from a usable cache and from target token shape', async () => {
    getCodexTurnState.mockResolvedValue({ ...status, models: [{ ...status.models[0], latest_response_evidence: {
      upstream_response_model: 'gpt-5.6-luna', model_relation: 'different', model_conflict: false,
    } }], observations: [{ model: 'gpt-test', observed_at: '2026-09-19T11:20:05Z', request_source: 'collector',
      outbound_length: 0, response_length: 332, response_shape: 'target', upstream_response_model: 'gpt-5.6-luna',
      model_relation: 'different', model_evidence_source: 'response.model', safety_buffering_enabled: true,
      safety_buffering_faster_model: 'gpt-5.6-luna' }] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="codex-turn-state-cache-availability-gpt-test"]').text()).toContain('cacheAvailable')
    const latest = wrapper.get('[data-testid="codex-turn-state-latest-evidence-gpt-test"]')
    expect(latest.text()).toContain('latestResponseEvidenceHint')
    const observation = wrapper.get('[data-testid="codex-turn-state-observation-gpt-test"]')
    expect(observation.text()).toContain('shapes.target')
    expect(observation.get('[data-testid="codex-response-model-relation"]').text()).toContain('modelRelations.different')
    expect(observation.text()).toContain('gpt-5.6-luna')
    wrapper.unmount()
  })

  it.each([292, 332])('shows the actual injected %i-character state and historical cache source', async (length) => {
    getCodexTurnState.mockResolvedValue({ ...status, observations: [
      { model: 'gpt-test', request_source: 'business', observed_at: '2026-09-19T11:20:05Z', request_sent_at: '2026-09-19T11:20:00Z',
        outbound_length: length, outbound_action: 'injected', outbound_source: 'collector', business_delivered: true,
        response_length: length === 292 ? 312 : 356, response_shape: 'extended', observation_id: '8742d982-c755-4357-b173-9ca9478e3024',
        snapshot_expires_at: '2026-09-19T12:15:00Z', snapshot_version: 42 },
    ] })
    const wrapper = render()
    await flushPromises()
    const card = wrapper.get('[data-testid="codex-turn-state-observation-gpt-test"]')
    expect(card.get('[data-testid="codex-turn-state-outbound-gpt-test"]').text()).toBe(`admin.accounts.codexTurnState.characters{"count":${length}}`)
    expect(card.get('[data-testid="codex-turn-state-outbound-action-gpt-test"]').text()).toContain('outboundActions.injected')
    expect(card.text()).toContain('outboundCacheSource')
    expect(card.text()).toContain('sources.collector')
    expect(card.text()).toContain('businessDelivered')
    expect(card.get('details').text()).toContain('8742d982-c755-4357-b173-9ca9478e3024')
    expect(card.get('details').text()).toContain('outboundCacheVersion42')
    expect(card.get('details').text()).toContain(new Date('2026-09-19T12:15:00Z').toLocaleString())
    expect(card.find('[data-testid="codex-turn-state-delivery-hint-gpt-test"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('preserves legacy observation compatibility without inferring an injection or successful delivery', async () => {
    getCodexTurnState.mockResolvedValue({ ...status, observations: [
      { model: 'gpt-test', request_source: 'business', observed_at: '2026-09-19T11:20:05Z', outbound_length: 292, response_length: 312, response_shape: 'extended' },
    ] })
    const wrapper = render()
    await flushPromises()
    const card = wrapper.get('[data-testid="codex-turn-state-observation-gpt-test"]')
    expect(card.get('[data-testid="codex-turn-state-sent-at-gpt-test"]').text()).toBe('—')
    expect(card.get('[data-testid="codex-turn-state-delivery-gpt-test"]').text()).toContain('businessDeliveryUnknown')
    expect(card.find('[data-testid="codex-turn-state-outbound-action-gpt-test"]').exists()).toBe(false)
    expect(card.text()).not.toContain('businessDelivered')
    expect(card.find('details').exists()).toBe(false)
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
