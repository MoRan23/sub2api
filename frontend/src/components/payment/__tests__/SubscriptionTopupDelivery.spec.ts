import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import SubscriptionTopupDelivery from '../SubscriptionTopupDelivery.vue'
import zh from '@/i18n/locales/zh'

const getOrderCards = vi.hoisted(() => vi.fn())
const copyToClipboard = vi.hoisted(() => vi.fn())
vi.mock('@/api/payment', () => ({ paymentAPI: { getOrderCards } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))
const delivery = { order_id: 42, product_name: 'ChatGPT', sku_name: 'Plus', quantity: 2, instructions: 'Use at provider.', cards: ['one-code', '<script>two-code</script>'], delivery_status: 'delivered', email_status: 'queued' }
function mountDelivery() {
  return mount(SubscriptionTopupDelivery, { props: { orderId: 42 }, global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh } })] } })
}
beforeEach(() => {
  getOrderCards.mockReset().mockResolvedValue({ data: delivery })
  copyToClipboard.mockReset()
})
describe('Subscription top-up delivery', () => {
  it('renders escaped codes and queued email state, copying only on request', async () => {
    const wrapper = mountDelivery()
    await flushPromises()
    expect(wrapper.text()).toContain('待发送')
    expect(wrapper.text()).toContain('<script>two-code</script>')
    expect(wrapper.find('script').exists()).toBe(false)
    expect(copyToClipboard).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(button => button.text() === '复制全部')!.trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('one-code\n<script>two-code</script>')
    wrapper.unmount()
  })
  it('shows a closed order and discards old delivery content on order change', async () => {
    const wrapper = mountDelivery()
    await flushPromises()
    getOrderCards.mockResolvedValue({ data: { ...delivery, order_id: 43, cards: [], delivery_status: 'released', email_status: '' } })
    await wrapper.setProps({ orderId: 43 })
    await flushPromises()
    expect(wrapper.text()).toContain('订单已关闭')
    expect(wrapper.text()).not.toContain('one-code')
    expect(getOrderCards.mock.calls[0][1].aborted).toBe(true)
    wrapper.unmount()
  })
})
