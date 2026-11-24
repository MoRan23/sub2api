import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import SubscriptionTopupStorefront from '../SubscriptionTopupStorefront.vue'
import PaymentMethodSelector from '../PaymentMethodSelector.vue'
import zh from '@/i18n/locales/zh'
import type { MethodLimit } from '@/types/payment'

const getTopupProducts = vi.hoisted(() => vi.fn())
vi.mock('@/api/payment', () => ({ paymentAPI: { getTopupProducts } }))

const method: MethodLimit = { currency: 'CNY', daily_limit: 0, daily_used: 0, daily_remaining: 0, single_min: 0, single_max: 0, fee_rate: 0, available: true }
const product = {
  id: 1, name: 'ChatGPT', description: 'Plan code', instructions: 'Redeem at the provider.', enabled: true, sort_order: 0,
  skus: [{ id: 2, product_id: 1, name: 'Plus', price: 12.34, currency: 'CNY', enabled: true, sort_order: 0, available_stock: 120 }],
}
async function mountStore() {
  const wrapper = mount(SubscriptionTopupStorefront, {
    props: { methods: { usdt: method, stripe: { ...method, currency: 'USD' } }, feeRate: 2.5, submitting: false },
    global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh } })], stubs: { PaymentMethodSelector: true } },
  })
  await flushPromises()
  return wrapper
}
beforeEach(() => getTopupProducts.mockReset().mockResolvedValue({ data: [product] }))

describe('Subscription top-up storefront', () => {
  it('uses SKU CNY prices, fees and quantities and excludes non-CNY methods', async () => {
    const wrapper = await mountStore()
    await wrapper.get('button[aria-pressed]').trigger('click')
    await wrapper.get('[data-testid="topup-quantity"]').setValue(3)
    const options = wrapper.getComponent(PaymentMethodSelector).props('methods')
    expect(options.map(option => option.type)).toEqual(['usdt'])
    expect(wrapper.get('[data-testid="topup-buy"]').text()).toContain('37.95')
    await wrapper.get('[data-testid="topup-buy"]').trigger('click')
    expect(wrapper.emitted('purchase')).toEqual([[{ skuId: 2, quantity: 3, amount: 37.02, paymentType: 'usdt' }]])
    wrapper.unmount()
  })

  it('rejects fractional, zero, excessive and out-of-stock quantities', async () => {
    const wrapper = await mountStore()
    await wrapper.get('button[aria-pressed]').trigger('click')
    for (const value of [0, 1.5, 101]) {
      await wrapper.get('[data-testid="topup-quantity"]').setValue(value)
      expect(wrapper.get('[data-testid="topup-buy"]').attributes('disabled')).toBeDefined()
    }
    wrapper.unmount()
    getTopupProducts.mockResolvedValue({ data: [{ ...product, skus: [{ ...product.skus[0], available_stock: 0 }] }] })
    const soldOut = await mountStore()
    expect(soldOut.get('button[aria-pressed]').attributes('disabled')).toBeDefined()
    expect(soldOut.text()).toContain('售罄')
    soldOut.unmount()
  })

  it('cancels the product request when leaving the page', async () => {
    const wrapper = await mountStore()
    const signal = getTopupProducts.mock.calls[0][0] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
  })
})
