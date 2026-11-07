import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'
const store = vi.hoisted(() => ({ activeSubscriptions: [] as unknown[], hasActiveSubscriptions: true, fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined) }))
vi.mock('@/stores', () => ({ useSubscriptionStore: () => store }))
vi.mock('@/utils/featureFlags', () => ({ FeatureFlags: { subscription: 'subscription' }, isFeatureFlagEnabled: () => true }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date(2026, 8, 22, 12)) })
afterEach(() => vi.useRealTimers())
describe('subscription expiry calendar labels', () => {
  it.each([75.125, 0])('shows permanent remaining credit %s without a total or usage bar', async (remaining) => {
    store.activeSubscriptions = [{ id: 1, group_id: 1, expires_at: null,
      total_limit_usd: 123.75, total_used_usd: 48.625, total_remaining_usd: remaining,
      group: { name: 'Permanent', subscription_type: 'total_quota', total_limit_usd: 99999 } }]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    expect(w.text()).toContain('subscription.permanent')
    expect(w.text()).toContain('subscription.remainingQuota')
    expect(w.text()).toContain(remaining > 0 ? '$75.125' : '$0.00')
    expect(w.text()).not.toContain('123.75')
    expect(w.text()).not.toContain('48.63')
    expect(w.text()).not.toContain('99999')
    expect(w.text()).not.toContain('subscriptionProgress.unlimited')
    expect(w.find('[style*="width:"]').exists()).toBe(false)
  })

  it.each([
    [new Date(2026, 8, 22, 18), 'expiresToday'],
    [new Date(2026, 8, 23, 18), 'expiresTomorrow'],
    [new Date(2026, 8, 22, 12), 'expired'],
    [new Date(2026, 8, 25, 12), 'daysRemaining'],
  ])('labels %s as %s', async (expires, label) => {
    store.activeSubscriptions = [{ id: 1, group_id: 1, expires_at: expires.toISOString(), group: { name: 'Plan' } }]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    expect(w.text()).toContain('subscriptionProgress.' + label)
  })
})
