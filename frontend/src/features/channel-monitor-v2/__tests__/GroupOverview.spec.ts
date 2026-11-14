import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { GroupAttributionStatus, GroupOverviewCard, GroupOverviewResponse, MonitorRange } from '@/api/channelMonitorV2'
import messages from '@/i18n/locales/zh/channelMonitorV2'
import GroupOverview from '../GroupOverview.vue'

const { getGroupOverview } = vi.hoisted(() => ({ getGroupOverview: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => ({ getGroupOverview }))

function card(id = 1, platform = 'openai'): GroupOverviewCard {
  return {
    group_id: id, group_name: `Group ${id}`, platform, rate_multiplier: 0.25,
    monitoring_enabled: true, state: 'healthy',
    metrics: { availability_rate: 0.99, cache_rate: 0.7, ttft_p50_ms: 2456 },
    buckets: [{ bucket_start: '2026-11-14T10:00:00Z', state: 'healthy', availability_rate: 0.99 }],
    attribution: { latest: null, recent: [] },
  }
}
function response(range: MonitorRange = '90m', items = [card()], interval: 60 | 300 = 60): GroupOverviewResponse {
  return {
    range, items, refresh_interval_seconds: interval,
    coverage: {
      requested_start: '2026-11-14T10:00:00Z', coverage_start: '2026-11-14T10:00:00Z',
      data_through: '2026-11-14T11:30:00Z', computed_at: '2026-11-14T11:30:00Z',
      aggregation_lag_seconds: 0, coverage_complete: true, bucket_seconds: 300,
      availability_version: 2, availability_ready: true,
    },
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
async function mountView() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/monitor', component: GroupOverview }] })
  await router.push('/monitor')
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': { ...messages, common: { refresh: '刷新', retry: '重试' } } } })
  return mount(GroupOverview, { global: { plugins: [router, i18n], stubs: { Icon: true, PlatformIcon: true, LoadingSpinner: true } } })
}
let wrapper: Awaited<ReturnType<typeof mountView>> | undefined
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-14T11:30:00Z'))
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  getGroupOverview.mockReset().mockResolvedValue(response())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers(); vi.restoreAllMocks() })

describe('group overview', () => {
  it('loads one shared overview, groups by platform and displays aggregate metrics and gray states', async () => {
    const unmonitored = { ...card(3, 'composite'), state: 'unmonitored' as const, monitoring_enabled: false, metrics: { availability_rate: null, cache_rate: null, ttft_p50_ms: null } }
    getGroupOverview.mockResolvedValue(response('90m', [card(), card(2), unmonitored]))
    wrapper = await mountView(); await flushPromises()
    expect(getGroupOverview).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-group-id]')).toHaveLength(3)
    expect(wrapper.findAll('h2')).toHaveLength(2)
    expect(wrapper.text()).toContain('用户倍率 0.25×')
    expect(wrapper.text()).toContain('99.0%')
    expect(wrapper.text()).toContain('70.0%')
    expect(wrapper.text()).toContain('2.46s')
    expect(wrapper.text()).toContain('未纳入监控')
    expect(wrapper.text()).toContain('暂无检测记录')
    expect(wrapper.get('[data-group-id="3"] dd').text()).toBe('—')
  })

  it.each([60, 300] as const)('polls every %i seconds and refreshes when the hidden page becomes visible', async interval => {
    getGroupOverview.mockResolvedValue(response('90m', [card()], interval))
    wrapper = await mountView(); await flushPromises()
    await vi.advanceTimersByTimeAsync((interval - 1) * 1000)
    expect(getGroupOverview).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('1 秒后刷新')
    await vi.advanceTimersByTimeAsync(1000)
    expect(getGroupOverview).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(interval * 3000)
    expect(getGroupOverview).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(getGroupOverview).toHaveBeenCalledTimes(3)
  })

  it('coalesces repeated refreshes and does not let old ranges or finally handlers replace the active request', async () => {
    const old = deferred<GroupOverviewResponse>()
    const current = deferred<GroupOverviewResponse>()
    getGroupOverview.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    wrapper = await mountView(); await flushPromises()
    await wrapper.get('[data-testid="overview-refresh"]').trigger('click')
    expect(getGroupOverview).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-range="24h"]').trigger('click'); await flushPromises()
    expect(getGroupOverview).toHaveBeenCalledTimes(2)
    expect(getGroupOverview.mock.calls[0][1].aborted).toBe(true)
    old.resolve(response('90m', [card(99)])); await flushPromises()
    expect(wrapper.find('[data-group-id="99"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="overview-refresh"]').attributes('disabled')).toBeDefined()
    current.resolve(response('24h', [card(2)])); await flushPromises()
    expect(wrapper.find('[data-group-id="2"]').exists()).toBe(true)
    expect(wrapper.get('[data-range="24h"]').attributes('aria-pressed')).toBe('true')
  })

  it('clears previously authorized cards on refresh failure and retries without changing the range', async () => {
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.find('[data-group-id="1"]').exists()).toBe(true)
    getGroupOverview.mockRejectedValueOnce({ message: '不可访问' })
    await wrapper.get('[data-testid="overview-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-group-id]')).toHaveLength(0)
    expect(wrapper.get('[role="alert"]').text()).toContain('不可访问')
    getGroupOverview.mockResolvedValueOnce(response('90m', []))
    await wrapper.get('[role="alert"] button').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="overview-empty"]').exists()).toBe(true)
  })

  it('aborts pending work and stops polling after unmount', async () => {
    const pending = deferred<GroupOverviewResponse>()
    getGroupOverview.mockReturnValue(pending.promise)
    wrapper = await mountView(); await flushPromises()
    wrapper.unmount(); wrapper = undefined
    expect(getGroupOverview.mock.calls[0][1].aborted).toBe(true)
    pending.resolve(response()); await flushPromises()
    await vi.advanceTimersByTimeAsync(600000)
    expect(getGroupOverview).toHaveBeenCalledTimes(1)
  })

  it('keeps detection results independent of availability and shows no internal reason text', async () => {
    const item = card()
    item.state = 'insufficient'
    item.attribution.recent = ['failed', 'mismatch', 'passed', 'cancelled', 'skipped', 'abnormal'].map((status, index) => ({
      finished_at: `2026-11-14T11:${20 - index}:00Z`, probe_model: `model-${index}`, status: status as GroupAttributionStatus, reason_code: 'secret-account-42',
    }))
    item.attribution.latest = item.attribution.recent[0]
    getGroupOverview.mockResolvedValue(response('90m', [item]))
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="group-state"]').text()).toBe('样本不足')
    expect(wrapper.get('[data-testid="attribution-state"]').text()).toBe('执行失败')
    expect(wrapper.get('[data-result="passed"]').classes()).toContain('bg-emerald-500')
    expect(wrapper.get('[data-result="mismatch"]').classes()).toContain('bg-amber-400')
    expect(wrapper.get('[data-result="failed"]').classes()).toContain('bg-rose-500')
    expect(wrapper.get('[data-result="abnormal"]').classes()).toContain('bg-rose-500')
    expect(wrapper.get('[data-result="abnormal"]').attributes('title')).toContain('检测异常')
    expect(wrapper.get('[data-result="cancelled"]').classes()).toContain('bg-gray-300')
    expect(wrapper.html()).not.toContain('secret-account-42')
    expect(wrapper.get('[data-result="failed"]').attributes('title')).toContain('model-0')
    expect(wrapper.text()).toContain('与上方时间范围无关')
  })
})
