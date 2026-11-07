import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import SubscriptionsView from '../SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ getMySubscriptions: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/subscriptions', () => ({ default: mocks }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())

async function renderQuota(expiresAt: string | null, remaining = 75.125) {
  mocks.getMySubscriptions.mockResolvedValue([{
    id: 1, group_id: 2, status: 'active', starts_at: '2026-01-01T00:00:00Z', expires_at: expiresAt,
    total_limit_usd: 123.75, total_used_usd: 48.625, total_remaining_usd: remaining,
    group: { id: 2, name: 'Quota group', platform: 'openai', subscription_type: 'total_quota', total_limit_usd: 99999 }
  }])
  const wrapper = mount(SubscriptionsView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } }
  })
  await flushPromises()
  return wrapper
}

describe('user total-quota entitlement display', () => {
  it('shows only actual remaining credit for a permanent entitlement', async () => {
    const wrapper = await renderQuota(null)
    expect(wrapper.text()).toContain('subscription.permanent')
    expect(wrapper.text()).toContain('subscription.remainingQuota')
    expect(wrapper.text()).toContain('$75.125')
    expect(wrapper.text()).not.toContain('$123.75')
    expect(wrapper.text()).not.toContain('$48.63')
    expect(wrapper.text()).not.toContain('99999')
    expect(wrapper.text()).not.toContain('userSubscriptions.unlimited')
    expect(wrapper.find('[style*="width:"]').exists()).toBe(false)
  })

  it('shows exhausted permanent credit as zero, not unlimited', async () => {
    const wrapper = await renderQuota(null, 0)
    expect(wrapper.text()).toContain('subscription.remainingQuota')
    expect(wrapper.text()).toContain('$0.00')
    expect(wrapper.text()).not.toContain('userSubscriptions.unlimited')
  })

  it('keeps finite entitlement progress based on its own amount', async () => {
    const wrapper = await renderQuota('2099-01-01T00:00:00Z')
    expect(wrapper.text()).toContain('$123.75')
    expect(wrapper.text()).toContain('48.63')
    expect(wrapper.text()).not.toContain('99999')
    expect(wrapper.text()).not.toContain('subscription.permanent')
    expect(wrapper.find('[style*="width:"]').exists()).toBe(true)
  })
})
