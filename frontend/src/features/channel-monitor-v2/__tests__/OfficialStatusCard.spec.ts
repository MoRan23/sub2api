import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { OfficialStatusResponse } from '@/api/officialStatus'
import messages from '@/i18n/locales/zh/channelMonitorV2'
import OfficialStatusCard from '../OfficialStatusCard.vue'

const { getOfficialStatus } = vi.hoisted(() => ({ getOfficialStatus: vi.fn() }))
vi.mock('@/api/officialStatus', () => ({ getOfficialStatus }))

function response(incidentCount = 0): OfficialStatusResponse {
  return {
    source_url: 'https://status.openai.com/', refresh_interval_seconds: 120,
    groups: (['apis', 'chatgpt', 'codex'] as const).map((id, index) => ({
      id, name: ['APIs', 'ChatGPT', 'Codex'][index]!, status: 'operational',
      fetched_at: '2026-11-20T01:00:00Z', stale: false,
    })),
    incidents: Array.from({ length: incidentCount }, (_, index) => ({
      id: String(index), title: `Official incident ${index}`, url: `https://status.openai.com/incidents/${index}`,
      status: 'resolved', groups: ['apis'], components: ['Responses'],
      updated_at: '2026-11-20T01:00:00Z', summary: '<img src=x onerror="alert(1)"> Official summary',
    })),
    incidents_fetched_at: '2026-11-20T01:00:00Z', incidents_stale: false,
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}
function mountCard() {
  return mount(OfficialStatusCard, { global: {
    plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh: { ...messages, common: { refresh: '刷新' } } } })],
    stubs: { Icon: true },
  } })
}
let wrapper: ReturnType<typeof mountCard> | undefined
function setHidden(hidden: boolean) {
  Object.defineProperty(document, 'hidden', { configurable: true, value: hidden })
  document.dispatchEvent(new Event('visibilitychange'))
}
beforeEach(() => {
  vi.useFakeTimers()
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  getOfficialStatus.mockReset().mockResolvedValue(response())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers(); vi.restoreAllMocks() })

describe('official status card', () => {
  it('keeps the three products compact and opens five original incidents, capped at twenty', async () => {
    getOfficialStatus.mockResolvedValue(response(21))
    wrapper = mountCard(); await flushPromises()
    expect(getOfficialStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-official-group]')).toHaveLength(3)
    expect(wrapper.get('[data-official-group="apis"] span').classes()).toContain('bg-emerald-500')
    expect(wrapper.find('[data-testid="official-incident"]').exists()).toBe(false)
    await wrapper.get('[data-testid="official-status-toggle"]').trigger('click')
    expect(wrapper.findAll('[data-testid="official-incident"]')).toHaveLength(5)
    expect(wrapper.text()).toContain('Official incident 0')
    expect(wrapper.text()).toContain('<img src=x onerror="alert(1)"> Official summary')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="official-incident"] a').attributes('rel')).toBe('noopener noreferrer')
    await wrapper.get('[data-testid="official-status-more"]').trigger('click')
    expect(wrapper.findAll('[data-testid="official-incident"]')).toHaveLength(20)
    await wrapper.get('[data-testid="official-status-more"]').trigger('click')
    expect(wrapper.findAll('[data-testid="official-incident"]')).toHaveLength(5)
  })

  it('automatically expands a new outage but preserves a manual collapse on unchanged refreshes', async () => {
    const snapshot = response(1)
    snapshot.groups[2]!.status = 'partial_outage'
    getOfficialStatus.mockResolvedValue(snapshot)
    wrapper = mountCard(); await flushPromises()
    expect(wrapper.get('[data-testid="official-status-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('[data-official-group="codex"] span').classes()).toContain('bg-rose-500')
    await wrapper.get('[data-testid="official-status-toggle"]').trigger('click')
    await vi.advanceTimersByTimeAsync(120000)
    expect(wrapper.get('[data-testid="official-status-toggle"]').attributes('aria-expanded')).toBe('false')
    getOfficialStatus.mockResolvedValue({ ...snapshot, incidents: [{ ...snapshot.incidents[0]!, id: 'new', status: 'investigating' }] })
    await wrapper.get('[data-testid="official-status-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="official-status-toggle"]').attributes('aria-expanded')).toBe('true')
  })

  it('shows unknown cold data in gray and retains the last snapshot when refresh fails', async () => {
    const cold = response()
    cold.groups = cold.groups.map(group => ({ ...group, fetched_at: null, stale: true, status: 'unknown' }))
    cold.incidents_fetched_at = null
    cold.incidents_stale = true
    getOfficialStatus.mockResolvedValueOnce(cold)
    wrapper = mountCard(); await flushPromises()
    expect(wrapper.text()).toContain('暂时无法获取官方状态')
    expect(wrapper.findAll('[data-official-group] > span:first-child').every(dot => dot.classes().includes('bg-gray-400'))).toBe(true)
    await wrapper.get('[data-testid="official-status-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-official-group="apis"]').text()).toContain('正常')
    getOfficialStatus.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('[data-testid="official-status-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-official-group="apis"]').text()).toContain('正常')
    expect(wrapper.get('[data-official-group="apis"] span').classes()).toContain('bg-gray-400')
    expect(wrapper.get('[data-testid="official-status-stale"]').text()).toContain('上次同步结果')
  })

  it('keeps independent product freshness and does not claim an empty incident feed before its first success', async () => {
    const snapshot = response()
    snapshot.groups[0]!.stale = true
    snapshot.incidents_fetched_at = null
    snapshot.incidents_stale = true
    getOfficialStatus.mockResolvedValue(snapshot)
    wrapper = mountCard(); await flushPromises()
    expect(wrapper.get('[data-official-group="apis"] span').classes()).toContain('bg-gray-400')
    expect(wrapper.get('[data-official-group="codex"] span').classes()).toContain('bg-emerald-500')
    await wrapper.get('[data-testid="official-status-toggle"]').trigger('click')
    expect(wrapper.get('#official-status-incidents').text()).toContain('暂时无法获取官方状态')
    expect(wrapper.text()).not.toContain('暂无近期事件')
  })

  it('does not expand stale incidents and renders postmortems as recovered history', async () => {
    const snapshot = response(1)
    snapshot.incidents[0]!.status = 'investigating'
    snapshot.incidents_stale = true
    getOfficialStatus.mockResolvedValueOnce(snapshot)
    wrapper = mountCard(); await flushPromises()
    expect(wrapper.get('[data-testid="official-status-toggle"]').attributes('aria-expanded')).toBe('false')
    await wrapper.get('[data-testid="official-status-toggle"]').trigger('click')
    expect(wrapper.get('[data-testid="official-incident"] span').classes()).toContain('text-gray-500')
    getOfficialStatus.mockResolvedValue({ ...snapshot, incidents_stale: false, incidents: [{ ...snapshot.incidents[0]!, status: 'postmortem' }] })
    await wrapper.get('[data-testid="official-status-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="official-incident"] span').text()).toBe('事后分析')
    expect(wrapper.get('[data-testid="official-incident"] span').classes()).toContain('text-emerald-700')
  })

  it('coalesces in-flight refreshes and aborts hidden or disposed requests without accepting late results', async () => {
    const first = deferred<OfficialStatusResponse>()
    const second = deferred<OfficialStatusResponse>()
    getOfficialStatus.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    wrapper = mountCard()
    const firstSignal = getOfficialStatus.mock.calls[0]![0] as AbortSignal
    await vi.advanceTimersByTimeAsync(240000)
    expect(getOfficialStatus).toHaveBeenCalledTimes(1)
    setHidden(true)
    expect(firstSignal.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(240000)
    expect(getOfficialStatus).toHaveBeenCalledTimes(1)
    setHidden(false)
    expect(getOfficialStatus).toHaveBeenCalledTimes(2)
    second.resolve(response()); await flushPromises()
    const old = response()
    old.groups[0]!.name = 'obsolete'
    first.resolve(old); await flushPromises()
    expect(wrapper.text()).not.toContain('obsolete')
    const pending = deferred<OfficialStatusResponse>()
    getOfficialStatus.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="official-status-refresh"]').trigger('click')
    const lastSignal = getOfficialStatus.mock.calls[2]![0] as AbortSignal
    wrapper.unmount(); wrapper = undefined
    expect(lastSignal.aborted).toBe(true)
    pending.resolve(response()); await flushPromises()
    await vi.advanceTimersByTimeAsync(240000)
    expect(getOfficialStatus).toHaveBeenCalledTimes(3)
  })
})
