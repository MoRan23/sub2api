import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexTurnStateModelStatus, CodexTurnStateObservation, CodexTurnStateStatus } from '@/api/admin/accounts'
import en from '@/i18n/locales/en/admin/accounts'
import zh from '@/i18n/locales/zh/admin/accounts'

const { getCodexTurnState } = vi.hoisted(() => ({ getCodexTurnState: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getCodexTurnState }))
vi.mock('@/api/admin/proxies', () => ({ getAll: vi.fn().mockResolvedValue([]) }))
import CodexTurnStateStatusModal from '../CodexTurnStateStatusModal.vue'

function runtimeMessages(messages: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? () => value : runtimeMessages(value as Record<string, unknown>),
  ]))
}

function model(code: string, patch: Partial<CodexTurnStateModelStatus> = {}): CodexTurnStateModelStatus {
  return {
    model: code, state: 'missing', shape: 'extended', source: '', token_length: 312, cipher_blocks: 11,
    remaining_seconds: 0, collector_paused: false, cache_available: false,
    collection_status: 'backoff', collection_reason: code, last_error: code,
    next_collect_at: '2026-09-20T12:00:30Z', ...patch,
  }
}

function state(models: CodexTurnStateModelStatus[]): CodexTurnStateStatus {
  return {
    account_id: 1, owner_account_id: 1, inherited: false, enabled: true,
    account_type: 'personal', resolved_account_type: 'personal', collector_proxy_id: 5,
    expected_length: 292, reason: '', models,
  }
}

function render(locale: 'zh' | 'en' = 'zh') {
  return mount(CodexTurnStateStatusModal, {
    props: { show: true, account: { id: 1, name: 'OAuth' } },
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: {
        en: runtimeMessages({ admin: en, common: { refresh: 'Refresh', close: 'Close' } }),
        zh: runtimeMessages({ admin: zh, common: { refresh: '刷新', close: '关闭' } }),
      } })],
      stubs: { BaseDialog: defineComponent({ template: '<div><slot/><slot name="footer"/></div>' }) },
    },
  })
}

describe('Codex turn-state collection diagnostics', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-20T12:00:00Z'))
    getCodexTurnState.mockReset()
  })
  afterEach(() => vi.useRealTimers())

  it.each(['zh', 'en'] as const)('renders distinct safe collection causes and guidance in %s without repeating the same error', async (locale) => {
    const codes = [
      'collector_transport_failed', 'collector_empty_response', 'collector_stream_failed',
      'collector_response_failed', 'collector_response_incomplete', 'collector_event_too_large',
      'collector_upstream_unavailable', 'collector_http_rejected', 'collector_proxy_auth_required',
      'collection_timeout', 'collector_proxy_unavailable', 'collector_auth_rejected', 'collector_rate_limited',
      'account_inactive', 'account_scheduling_disabled', 'account_expired',
      'collector_dns_failed', 'collector_connection_refused', 'collector_connection_closed',
      'collector_tls_failed', 'collector_proxy_tunnel_failed', 'collector_connect_timeout',
      'collector_tls_timeout', 'collector_response_header_timeout',
    ] as const
    getCodexTurnState.mockResolvedValue(state(codes.map(code => model(code))))
    const wrapper = render(locale)
    await flushPromises()
    const messages = (locale === 'zh' ? zh : en).accounts.codexTurnState
    for (const code of codes) {
      const card = wrapper.get(`[data-testid="codex-turn-state-cache-${code}"]`)
      expect(card.get(`[data-testid="codex-turn-state-reason-${code}"]`).text()).toBe(messages.reasons[code])
      expect(card.get(`[data-testid="codex-turn-state-guidance-${code}"]`).text()).toBe(messages.reasonHints[code])
      expect(card.find(`[data-testid="codex-turn-state-previous-error-${code}"]`).exists()).toBe(false)
      expect(card.text()).toContain(messages.retryHint)
    }
    expect(wrapper.text()).toContain('401/403')
    expect(wrapper.text()).toContain(locale === 'zh' ? '不能证明是代理并发限制' : 'does not prove a proxy concurrency limit')
    wrapper.unmount()
  })

  it.each(['zh', 'en'] as const)('explains legacy failures without inventing a detailed cause in %s', async (locale) => {
    getCodexTurnState.mockResolvedValue(state([model('gpt-test', { collection_reason: 'collection_failed', last_error: 'collection_failed' })]))
    const wrapper = render(locale)
    await flushPromises()
    const messages = (locale === 'zh' ? zh : en).accounts.codexTurnState
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe(messages.reasons.collection_failed)
    expect(wrapper.get('[data-testid="codex-turn-state-guidance-gpt-test"]').text()).toBe(messages.reasonHints.collection_failed)
    expect(wrapper.find('[data-testid="codex-turn-state-previous-error-gpt-test"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each(['unrecognized_failure', '<img src=x onerror=alert(1)> Authorization: Bearer private-token https://user:pass@proxy/', 'constructor', '__proto__'])('does not echo unrecognized or unsafe reason text (%s)', async (unsafe) => {
    getCodexTurnState.mockResolvedValue({
      ...state([model('gpt-test', { collection_reason: unsafe, last_error: unsafe, refresh_reason: unsafe })]), reason: unsafe,
    })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).not.toContain(unsafe)
    expect(wrapper.text()).not.toContain('private-token')
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe('原因未分类')
    expect(wrapper.get('[data-testid="codex-turn-state-guidance-gpt-test"]').text()).toContain('无法据此判断是否由代理或并发引起')
    wrapper.unmount()
  })

  it('separates a previous failure from the current account scheduling blocker', async () => {
    getCodexTurnState.mockResolvedValue(state([model('gpt-test', {
      collection_status: 'blocked', collection_reason: 'account_scheduling_disabled', last_error: 'collector_stream_failed',
    })]))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe('账号已关闭参与调度')
    expect(wrapper.get('[data-testid="codex-turn-state-guidance-gpt-test"]').text()).toContain('开启后才能继续采集')
    const previous = wrapper.get('[data-testid="codex-turn-state-previous-error-gpt-test"]')
    expect(previous.text()).toContain('上次采集错误: 响应流读取失败或提前中断')
    expect(previous.text()).toContain('请检查采集代理和上游连接的稳定性')
    expect(wrapper.text()).not.toContain(zh.accounts.codexTurnState.retryHint)
    wrapper.unmount()
  })

  it('safely explains an unknown historical error without replacing the known current reason', async () => {
    getCodexTurnState.mockResolvedValue(state([model('gpt-test', {
      collection_reason: 'collector_rate_limited', last_error: 'dial https://private-password:secret@proxy',
    })]))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe('采集受到限流')
    expect(wrapper.get('[data-testid="codex-turn-state-previous-error-gpt-test"]').text()).toContain('上次采集错误: 原因未分类')
    expect(wrapper.text()).not.toContain('private-password')
    wrapper.unmount()
  })

  it('clears old diagnostics when automatic refresh reports recovery', async () => {
    const initial = state([model('gpt-test', { collection_reason: 'collector_transport_failed', last_error: 'collector_transport_failed' })])
    const recovered = state([model('gpt-test', { collection_status: 'idle', collection_reason: 'idle', last_error: '', next_collect_at: undefined })])
    getCodexTurnState.mockResolvedValueOnce(initial).mockResolvedValueOnce(recovered)
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('采集连接或发送失败')
    await vi.advanceTimersByTimeAsync(5000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe('当前无需采集')
    expect(wrapper.find('[data-testid="codex-turn-state-guidance-gpt-test"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="codex-turn-state-previous-error-gpt-test"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('采集连接或发送失败')
    expect(wrapper.text()).not.toContain(zh.accounts.codexTurnState.retryHint)
    wrapper.unmount()
  })

  it.each(['zh', 'en'] as const)('shows a collector proxy change as idle with a usable target cache in %s', async (locale) => {
    getCodexTurnState.mockResolvedValue(state([model('gpt-test', {
      state: 'ready', shape: 'target', token_length: 292, cipher_blocks: 10, cache_available: true,
      expires_at: '2026-09-20T12:30:00Z', remaining_seconds: 1800,
      collection_status: 'idle', collection_reason: 'collector_proxy_changed', last_error: '', next_collect_at: undefined,
    })]))
    const wrapper = render(locale)
    await flushPromises()
    const messages = (locale === 'zh' ? zh : en).accounts.codexTurnState
    expect(wrapper.get('[data-testid="codex-turn-state-cache-availability-gpt-test"]').text()).toBe(messages.cacheAvailable)
    expect(wrapper.get('[data-testid="codex-turn-state-collection-gpt-test"]').text()).toBe(messages.collectionStatuses.idle)
    expect(wrapper.get('[data-testid="codex-turn-state-reason-gpt-test"]').text()).toBe(messages.reasons.collector_proxy_changed)
    expect(wrapper.get('[data-testid="codex-turn-state-guidance-gpt-test"]').text()).toBe(messages.reasonHints.collector_proxy_changed)
    expect(wrapper.find('[data-testid="codex-turn-state-previous-error-gpt-test"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain(messages.retryHint)
    wrapper.unmount()
  })

  it.each(['zh', 'en'] as const)('explains independent collection outbound zero without suggesting a failed injection in %s', async (locale) => {
    getCodexTurnState.mockResolvedValue({ ...state([]), observations: [
      { model: 'gpt-sol', request_source: 'collector', observed_at: '2026-09-20T11:59:55Z', request_sent_at: '2026-09-20T11:59:50Z',
        outbound_action: 'collector_omitted', outbound_length: 0, response_length: 292, response_shape: 'target' },
    ] })
    const wrapper = render(locale)
    await flushPromises()
    const messages = (locale === 'zh' ? zh : en).accounts.codexTurnState
    const card = wrapper.get('[data-testid="codex-turn-state-observation-gpt-sol"]')
    expect(card.get('[data-testid="codex-turn-state-outbound-gpt-sol"]').text()).toBe(messages.outboundCollectorOmitted)
    expect(card.get('[data-testid="codex-turn-state-outbound-action-gpt-sol"]').text()).toBe(messages.outboundActions.collector_omitted)
    expect(card.get('[data-testid="codex-turn-state-collector-outbound-hint-gpt-sol"]').text()).toBe(messages.collectorOutboundHint)
    expect(card.find('[data-testid="codex-turn-state-delivery-gpt-sol"]').exists()).toBe(false)
    expect(wrapper.text()).toContain(messages.observationRequestHint)
    wrapper.unmount()
  })

  it.each(['zh', 'en'] as const)('shows successful and failed historical business delivery independently of current cache in %s', async (locale) => {
    const observation: CodexTurnStateObservation = {
      model: 'gpt-sol', request_source: 'business', observed_at: '2026-09-20T11:20:05Z', request_sent_at: '2026-09-20T11:20:00Z',
      outbound_action: 'passthrough', outbound_length: 0, response_length: 312, response_shape: 'extended',
      maintenance_reason: 'cache_unavailable', business_delivered: false,
    }
    getCodexTurnState.mockResolvedValue({ ...state([model('gpt-sol', {
      state: 'ready', shape: 'target', token_length: 292, cipher_blocks: 10, expires_at: '2026-09-20T12:30:00Z',
      remaining_seconds: 1800, cache_available: true, last_business_at: '2026-09-20T11:59:55Z',
    })]), observations: [observation, { ...observation, model: 'gpt-astra', business_delivered: true }] })
    const wrapper = render(locale)
    await flushPromises()
    const messages = (locale === 'zh' ? zh : en).accounts.codexTurnState
    const card = wrapper.get('[data-testid="codex-turn-state-observation-gpt-sol"]')
    expect(card.get('[data-testid="codex-turn-state-outbound-gpt-sol"]').text()).toBe(messages.outboundNotCarried)
    expect(card.get('[data-testid="codex-turn-state-delivery-gpt-sol"]').text()).toBe(messages.businessNotDelivered)
    expect(card.get('[data-testid="codex-turn-state-delivery-hint-gpt-sol"]').text()).toBe(messages.businessNotDeliveredHint)
    expect(card.get('[data-testid="codex-turn-state-maintenance-gpt-sol"]').text()).toBe(messages.reasons.cache_unavailable)
    expect(card.get('[data-testid="codex-turn-state-sent-at-gpt-sol"]').text()).toBe(new Date(observation.request_sent_at!).toLocaleString())
    expect(card.text()).not.toContain(new Date('2026-09-20T11:59:55Z').toLocaleString())
    expect(wrapper.get('[data-testid="codex-turn-state-delivery-gpt-astra"]').text()).toBe(messages.businessDelivered)
    expect(wrapper.find('[data-testid="codex-turn-state-delivery-hint-gpt-astra"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not echo unrecognized maintenance errors, outbound actions, cache sources or observation references', async () => {
    const unsafe = 'Authorization: Bearer private-token https://user:pass@proxy/'
    getCodexTurnState.mockResolvedValue({ ...state([]), observations: [
      { model: 'gpt-sol', request_source: 'business', observed_at: '2026-09-20T11:59:55Z', outbound_action: 'injected',
        outbound_length: 292, response_length: 312, response_shape: 'extended', maintenance_reason: unsafe, outbound_source: unsafe, observation_id: unsafe },
      { model: 'gpt-astra', request_source: 'business', observed_at: '2026-09-20T11:59:55Z', outbound_action: unsafe,
        outbound_length: 0, response_length: 312, response_shape: 'extended' },
    ] })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).not.toContain('private-token')
    expect(wrapper.text()).not.toContain('user:pass')
    expect(wrapper.get('[data-testid="codex-turn-state-maintenance-gpt-sol"]').text()).toBe(zh.accounts.codexTurnState.unknownReason)
    expect(wrapper.get('[data-testid="codex-turn-state-outbound-action-gpt-astra"]').text()).toBe('—')
    expect(wrapper.find('details').exists()).toBe(false)
    wrapper.unmount()
  })
})
