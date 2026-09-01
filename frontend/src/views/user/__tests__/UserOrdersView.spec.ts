import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UserOrdersView from '../UserOrdersView.vue'

const getMyOrders = vi.hoisted(() => vi.fn())
const getRefundEligibleProviders = vi.hoisted(() => vi.fn())
const requestRefund = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const routerPush = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRouter: () => ({ push: routerPush }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getMyOrders,
    getRefundEligibleProviders,
    requestRefund,
    cancelOrder,
  },
}))

const OrderTableStub = {
  props: ['orders'],
  template: `
    <div>
      <slot v-if="orders[0]" name="actions" :row="orders[0]" />
    </div>
  `,
}

describe('UserOrdersView refund wallet details', () => {
  beforeEach(() => {
    getMyOrders.mockReset().mockResolvedValue({
      data: {
        items: [
          {
            id: 42,
            user_id: 9,
            amount: 0.01,
            gift_amount: 0.00000001,
            pay_amount: 0.01,
            fee_rate: 0,
            payment_type: 'alipay',
            out_trade_no: 'sub2-small-gift',
            status: 'COMPLETED',
            order_type: 'balance',
            provider_instance_id: 'provider-1',
            created_at: '2026-09-01T00:00:00Z',
            expires_at: '2026-09-01T00:30:00Z',
            refund_amount: 0,
          },
        ],
        total: 1,
      },
    })
    getRefundEligibleProviders.mockReset().mockResolvedValue({
      data: { provider_instance_ids: ['provider-1'] },
    })
    requestRefund.mockReset()
    cancelOrder.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    routerPush.mockReset()
  })

  it('shows the snapshotted ordinary, gift, and total credit before requesting a refund', async () => {
    const wrapper = mount(UserOrdersView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          OrderTable: OrderTableStub,
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>',
          },
          Pagination: true,
          Select: true,
          Icon: true,
        },
      },
    })

    await flushPromises()
    const refundButton = wrapper.findAll('button').find((button) =>
      button.text().includes('payment.orders.requestRefund'),
    )
    expect(refundButton).toBeDefined()
    await refundButton!.trigger('click')

    expect(wrapper.get('[data-test="refund-ordinary-credit"]').text()).toContain('$0.01')
    expect(wrapper.get('[data-test="refund-gift-credit"]').text()).toContain('$0.00000001')
    expect(wrapper.get('[data-test="refund-total-credit"]').text()).toContain('$0.01000001')
  })
})
