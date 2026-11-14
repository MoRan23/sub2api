import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { GroupAttributionStatus, GroupAttributionSummaryState, GroupOverviewCard, GroupOverviewResponse, MonitorRange } from '@/api/channelMonitorV2'
import messages from '@/i18n/locales/zh/channelMonitorV2'
import GroupOverview from '../GroupOverview.vue'

const { getGroupOverview } = vi.hoisted(() => ({ getGroupOverview: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => ({ getGroupOverview }))

const asOf = '2026-11-14T11:30:00Z'
function attributionHistory(state: GroupAttributionSummaryState = 'no_data') {
  const end = Date.parse(asOf)
  return Array.from({ length: 20 }, (_, index) => ({
    bucket_start: new Date(end - (20 - index) * 600000).toISOString(),
    bucket_end: new Date(end - (19 - index) * 600000).toISOString(),
    state,
  }))
}
function card(id = 1, platform = 'openai'): GroupOverviewCard {
  return {
    group_id: id, group_name: `Group ${id}`, platform, rate_multiplier: 0.25,
    monitoring_enabled: true, state: 'healthy',
    metrics: { availability_rate: 0.99, cache_rate: 0.7, ttft_p50_ms: 2456 },
    buckets: [{ bucket_start: '2026-11-14T10:00:00Z', state: 'healthy', availability_rate: 0.99 }],
    attribution: { summary_state: 'no_data', enabled: true, as_of: asOf, history: attributionHistory(), latest: null, recent: [] },
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
    const empty = { ...card(3, 'composite'), state: 'no_data' as const, metrics: { availability_rate: null, cache_rate: null, ttft_p50_ms: null } }
    getGroupOverview.mockResolvedValue(response('90m', [card(), card(2), empty]))
    wrapper = await mountView(); await flushPromises()
    expect(getGroupOverview).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-group-id]')).toHaveLength(3)
    expect(wrapper.findAll('h2')).toHaveLength(2)
    expect(wrapper.text()).toContain('用户倍率 0.25×')
    expect(wrapper.text()).toContain('99.0%')
    expect(wrapper.text()).toContain('70.0%')
    expect(wrapper.text()).toContain('2.46s')
    expect(wrapper.text()).toContain('暂无数据')
    expect(wrapper.text()).toContain('近期无有效检测')
    expect(wrapper.get('[data-group-id="3"] dd').text()).toBe('—')
  })

  it('updates platform sections and counts when a group leaves the monitoring scope', async () => {
    getGroupOverview.mockResolvedValueOnce(response('90m', [card(), card(2, 'composite')]))
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.findAll('h2')).toHaveLength(2)
    expect(wrapper.text()).toContain('2 个分组')
    getGroupOverview.mockResolvedValueOnce(response('90m', [card()]))
    await wrapper.get('[data-testid="overview-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-group-id="2"]').exists()).toBe(false)
    expect(wrapper.findAll('h2')).toHaveLength(1)
    expect(wrapper.text()).toContain('1 个分组')
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

  it('renders server summary and aggregate history independently of legacy checks and request availability', async () => {
    const item = card()
    item.state = 'insufficient'
    item.attribution.summary_state = 'passed'
    item.attribution.history = attributionHistory().map((bucket, index) => ({
      ...bucket, state: (['passed', 'mismatch', 'failed', 'no_data'] as const)[index % 4]!,
    }))
    item.attribution.recent = ['failed', 'mismatch', 'passed', 'cancelled', 'skipped', 'abnormal'].map((status, index) => ({
      finished_at: `2026-11-14T11:${20 - index}:00Z`, probe_model: `model-${index}`, status: status as GroupAttributionStatus, reason_code: 'secret-account-42',
    }))
    item.attribution.latest = item.attribution.recent[0]
    getGroupOverview.mockResolvedValue(response('90m', [item]))
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="group-state"]').text()).toBe('样本不足')
    expect(wrapper.get('[data-testid="attribution-state"]').text()).toBe('通过')
    const cells = wrapper.findAll('.attribution-cell')
    expect(cells).toHaveLength(20)
    expect(cells[0]!.attributes('data-result')).toBe('passed')
    expect(cells[19]!.attributes('data-result')).toBe(wrapper.get('[data-testid="attribution-state"]').attributes('data-state'))
    expect(wrapper.get('[data-result="passed"]').classes()).toContain('bg-emerald-500')
    expect(wrapper.get('[data-result="mismatch"]').classes()).toContain('bg-amber-400')
    expect(wrapper.get('[data-result="failed"]').classes()).toContain('bg-rose-500')
    expect(wrapper.get('[data-result="no_data"]').classes()).toContain('bg-gray-300')
    expect(wrapper.find('[data-result="abnormal"]').exists()).toBe(false)
    expect(wrapper.find('[data-result="cancelled"]').exists()).toBe(false)
    expect(wrapper.find('[data-result="skipped"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('secret-account-42')
    expect(wrapper.html()).not.toContain('model-0')
    const time = (value: string) => new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
    const title = cells[0]!.attributes('title')
    expect(title).toContain(time(item.attribution.history[0]!.bucket_start))
    expect(title).toContain(time(item.attribution.history[0]!.bucket_end))
    expect(title).toContain('通过')
    expect(title).toContain('OAuth：最近 15 分钟内全部有效检测')
    expect(title).toContain('API Key（含 Engine）：每账号不限时的最新有效检测')
    expect(wrapper.get('[data-testid="attribution-through"]').text()).toBe(`更新至 ${time(asOf)}`)
    expect(wrapper.get('[data-testid="attribution-through"]').text()).not.toContain(time(item.attribution.latest!.finished_at))
    expect(wrapper.text()).toContain('与上方时间范围无关')
    expect(wrapper.text()).toContain('组内调度开启账号')
  })

  it.each([
    ['passed', false, '通过', 'text-emerald-700'],
    ['mismatch', false, '不通过', 'text-amber-700'],
    ['failed', false, '执行失败', 'text-rose-600'],
    ['no_data', false, '未开启降智检测', 'text-gray-500'],
    ['no_data', true, '近期无有效检测', 'text-gray-500'],
  ] as const)('shows %s with enabled=%s and keeps the current history cell consistent', async (state, enabled, text, color) => {
    const item = card()
    item.attribution = { ...item.attribution, summary_state: state, enabled, history: attributionHistory('passed') }
    getGroupOverview.mockResolvedValue(response('90m', [item]))
    wrapper = await mountView(); await flushPromises()
    const summary = wrapper.get('[data-testid="attribution-state"]')
    expect(summary.text()).toBe(text)
    expect(summary.classes()).toContain(color)
    expect(summary.attributes('data-state')).toBe(state)
    expect(wrapper.findAll('.attribution-cell')).toHaveLength(20)
    expect(wrapper.findAll('.attribution-cell')[19]!.attributes('data-result')).toBe(state)
    expect(wrapper.get('[data-testid="group-state"]').classes()).toContain('bg-emerald-100')
    expect(wrapper.get('[data-state="healthy"]').classes()).toContain('bg-emerald-500')
  })

  it.each(['legacy', 'unknown'] as const)('safely renders gray when the %s API has no usable aggregate state', async mode => {
    const item = card()
    item.attribution = {
      latest: { finished_at: asOf, probe_model: 'legacy-model', status: 'passed', reason_code: 'reference_match' },
      recent: [{ finished_at: asOf, probe_model: 'legacy-model', status: 'passed', reason_code: 'reference_match' }],
    }
    if (mode === 'unknown') {
      item.attribution = {
        ...item.attribution, enabled: true, as_of: asOf,
        summary_state: 'future-status' as GroupAttributionSummaryState,
        history: attributionHistory('future-status' as GroupAttributionSummaryState),
      }
    }
    getGroupOverview.mockResolvedValue(response('90m', [item]))
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="attribution-state"]').attributes('data-state')).toBe('no_data')
    expect(wrapper.get('[data-testid="attribution-state"]').classes()).toContain('text-gray-500')
    const cells = wrapper.findAll('.attribution-cell')
    expect(cells).toHaveLength(20)
    expect(cells.every(cell => cell.attributes('data-result') === 'no_data' && cell.classes().includes('bg-gray-300'))).toBe(true)
    expect(wrapper.html()).not.toContain('legacy-model')
  })

  it('pads missing aggregate intervals with gray and preserves the supplied chronological states', async () => {
    const item = card()
    item.attribution.summary_state = 'failed'
    item.attribution.history = attributionHistory('passed').slice(-3)
    item.attribution.history[1]!.state = 'mismatch'
    item.attribution.history[2]!.state = 'failed'
    getGroupOverview.mockResolvedValue(response('90m', [item]))
    wrapper = await mountView(); await flushPromises()
    const cells = wrapper.findAll('.attribution-cell')
    expect(cells).toHaveLength(20)
    expect(cells.slice(0, 17).every(cell => cell.attributes('data-result') === 'no_data')).toBe(true)
    expect(cells.slice(-3).map(cell => cell.attributes('data-result'))).toEqual(['passed', 'mismatch', 'failed'])
  })

  it('keeps the 200-minute attribution window fixed when the real-request range changes', async () => {
    const item = card()
    item.attribution.summary_state = 'mismatch'
    item.attribution.history = attributionHistory('mismatch')
    getGroupOverview.mockImplementation((range: MonitorRange) => Promise.resolve(response(range, [item])))
    wrapper = await mountView(); await flushPromises()
    const history = wrapper.findAll('.attribution-cell').map(cell => ({ state: cell.attributes('data-result'), title: cell.attributes('title') }))
    expect(history).toHaveLength(20)
    expect(wrapper.text()).toContain('200')
    expect(wrapper.text()).toContain('10')
    expect(wrapper.text()).toContain('15')
    await wrapper.get('[data-range="30d"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-range="30d"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.findAll('.attribution-cell').map(cell => ({ state: cell.attributes('data-result'), title: cell.attributes('title') }))).toEqual(history)
  })

  it('uses platform brand colors in section icons, card icons and labels in both themes', async () => {
    const palettes = [
      ['anthropic', 'bg-orange-500/10', 'text-orange-600', 'dark:text-orange-300'],
      ['openai', 'bg-green-500/10', 'text-green-600', 'dark:text-green-300'],
      ['gemini', 'bg-blue-500/10', 'text-blue-600', 'dark:text-blue-300'],
      ['grok', 'bg-zinc-800/10', 'text-zinc-800', 'dark:text-zinc-200'],
      ['antigravity', 'bg-purple-500/10', 'text-purple-600', 'dark:text-purple-300'],
      ['composite', 'bg-cyan-500/10', 'text-cyan-700', 'dark:text-cyan-300'],
    ] as const
    getGroupOverview.mockResolvedValue(response('90m', palettes.map(([platform], index) => card(index + 1, platform))))
    wrapper = await mountView(); await flushPromises()
    expect(wrapper.findAll('[data-testid="platform-section-icon"]')).toHaveLength(6)
    for (const [index, [platform, ...colors]] of palettes.entries()) {
      const group = wrapper.get(`[data-group-id="${index + 1}"]`)
      const sectionIcon = wrapper.findAll('[data-testid="platform-section-icon"]').find(icon => icon.get('platform-icon-stub').attributes('platform') === platform)!
      for (const target of [sectionIcon, group.get('[data-testid="platform-card-icon"]'), group.get('[data-testid="platform-label"]')]) {
        expect(target.classes()).toEqual(expect.arrayContaining(colors))
        expect(target.classes()).not.toContain('bg-primary-100')
      }
      expect(group.get('[data-state="healthy"]').classes()).toContain('bg-emerald-500')
    }
  })
})
