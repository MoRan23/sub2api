import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import DashboardView from '../DashboardView.vue'

const mocks = vi.hoisted(() => ({ stats: vi.fn(), refresh: vi.fn() }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { total_balance: 12.5 }, isSimpleMode: false, refreshUser: mocks.refresh }) }))
vi.mock('@/api/usage', () => ({ usageAPI: {
  getDashboardStats: mocks.stats,
  getDashboardTrend: vi.fn().mockResolvedValue({ trend: [] }),
  getDashboardModels: vi.fn().mockResolvedValue({ models: [] }),
  getByDateRange: vi.fn().mockResolvedValue({ items: [] }),
} }))
vi.mock('@/api/user', () => ({ getMyPlatformQuotas: vi.fn().mockResolvedValue({ platform_quotas: [] }) }))

describe('dashboard statistics initialization', () => {
  beforeEach(() => { vi.useFakeTimers(); mocks.stats.mockReset(); mocks.refresh.mockResolvedValue(undefined) })
  afterEach(() => { vi.useRealTimers() })
  it('keeps balance available and retries without showing fake zero statistics', async () => {
    mocks.stats.mockRejectedValueOnce({ reason: 'USAGE_STATS_INITIALIZING', retry_after_seconds: 10 }).mockResolvedValueOnce({ total_requests: 5 })
    const wrapper = shallowMount(DashboardView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
    await flushPromises()
    expect(wrapper.text()).toContain('dashboard.statsInitializing')
    expect(wrapper.text()).toContain('12.50000000')
    expect(wrapper.findComponent({ name: 'UserDashboardStats' }).exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(10000)
    await flushPromises()
    expect(mocks.stats).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('dashboard.statsInitializing')
    wrapper.unmount()
  })
  it('cancels retry on unmount', async () => {
    mocks.stats.mockRejectedValue({ code: 'USAGE_STATS_INITIALIZING' })
    const wrapper = shallowMount(DashboardView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
    await flushPromises(); wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60000)
    expect(mocks.stats).toHaveBeenCalledTimes(1)
  })
})
