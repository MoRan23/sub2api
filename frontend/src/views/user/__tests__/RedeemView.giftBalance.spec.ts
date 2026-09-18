import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import RedeemView from '../RedeemView.vue'

const redeem = vi.hoisted(() => vi.fn())
const getHistory = vi.hoisted(() => vi.fn())
const getPublicSettings = vi.hoisted(() => vi.fn())
const refreshUser = vi.hoisted(() => vi.fn())
const fetchActiveSubscriptions = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: {
      balance: 0.01,
      gift_balance: 0.00000001,
      total_balance: 0.01000001,
      concurrency: 1,
    },
    refreshUser,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError, showWarning }),
}))

vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({ fetchActiveSubscriptions }),
}))

vi.mock('@/api', () => ({
  redeemAPI: { redeem, getHistory },
  authAPI: { getPublicSettings },
}))

const balanceHistoryItem = {
  id: 1,
  code: 'SMALL-GIFT-CODE',
  type: 'balance',
  value: 0.01,
  gift_value: 0.00000001,
  used_at: '2026-09-01T00:00:00Z',
}

describe('RedeemView gift balance display', () => {
  beforeEach(() => {
    redeem.mockReset().mockResolvedValue({
      message: 'ok',
      type: 'balance',
      value: 0.01,
      gift_value: 0.00000001,
      new_balance: 0.01,
    })
    getHistory.mockReset().mockResolvedValue([balanceHistoryItem])
    getPublicSettings.mockReset().mockResolvedValue({ contact_info: '' })
    refreshUser.mockReset().mockResolvedValue(undefined)
    fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
    showSuccess.mockReset()
    showError.mockReset()
    showWarning.mockReset()
  })

  it('preserves eight-decimal gift amounts in wallet, history, and redemption result', async () => {
    const wrapper = mount(RedeemView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
          Transition: false,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('$0.01000001')
    expect(wrapper.text()).toContain('common.ordinaryBalance $0.01')
    expect(wrapper.text()).toContain('common.giftBalance $0.00000001')
    expect(wrapper.text()).toContain('+$0.01000001')

    await wrapper.get('#code').setValue('SMALL-GIFT-CODE')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(redeem).toHaveBeenCalledWith('SMALL-GIFT-CODE')
    expect(wrapper.text()).toContain('redeem.added: $0.01000001')
    expect(wrapper.text()).toContain('common.giftBalance: $0.00000001')
  })
})
