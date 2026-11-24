import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const {
  getOrders,
  getOrder,
  cancelOrder,
  retryRecharge,
  refundOrder,
  queryRefund,
  getAdminUserByID,
  showSuccess,
  showError,
  resendCards,
} = vi.hoisted(() => ({
  getOrders: vi.fn(),
  getOrder: vi.fn(),
  cancelOrder: vi.fn(),
  retryRecharge: vi.fn(),
  refundOrder: vi.fn(),
  queryRefund: vi.fn(),
  getAdminUserByID: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  resendCards: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    getOrders,
    getOrder,
    cancelOrder,
    retryRecharge,
    refundOrder,
    queryRefund,
  },
  default: {
    getOrders,
    getOrder,
    cancelOrder,
    retryRecharge,
    refundOrder,
    queryRefund,
  },
}))

vi.mock('@/api/admin/users', () => ({
  getById: getAdminUserByID,
  default: { getById: getAdminUserByID },
}))

vi.mock('@/api/admin/subscriptionTopups', () => ({ adminSubscriptionTopupsAPI: { resendCards } }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (
        params ? `${key}:${JSON.stringify(params)}` : key
      ),
    }),
  }
})

import AdminOrdersView from '../AdminOrdersView.vue'

const balanceOrder = {
  id: 12,
  user_id: 9,
  amount: 100,
  gift_ratio: 20,
  gift_amount: 20,
  pay_amount: 103,
  currency: 'USD',
  fee_rate: 3,
  payment_type: 'stripe',
  out_trade_no: 'gift-refund-12',
  status: 'COMPLETED',
  order_type: 'balance',
  created_at: '2026-09-01T10:00:00Z',
  expires_at: '2026-09-01T10:30:00Z',
  refund_amount: 0,
}

const OrderTableStub = {
  props: ['orders'],
  template: `
    <div>
      <div v-for="row in orders" :key="row.id">
        <slot name="actions" :row="row" />
      </div>
    </div>
  `,
}

const AdminRefundDialogStub = {
  props: [
    'show',
    'order',
    'userOrdinaryBalance',
    'userGiftBalance',
    'walletLoading',
  ],
  emits: ['confirm', 'cancel'],
  template: `
    <div v-if="show" data-test="refund-dialog">
      <span data-test="wallet">{{ userOrdinaryBalance }}|{{ userGiftBalance }}</span>
      <span data-test="wallet-loading">{{ walletLoading }}</span>
      <button
        data-test="confirm-refund"
        @click="$emit('confirm', { amount: 50, reason: 'partial', deduct_balance: true, force: false })"
      >confirm</button>
    </div>
  `,
}

describe('AdminOrdersView gift refunds', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getOrders.mockResolvedValue({
      data: {
        items: [balanceOrder],
        total: 1,
      },
    })
    getAdminUserByID.mockResolvedValue({
      id: 9,
      balance: 80,
      gift_balance: 12,
    })
    refundOrder.mockResolvedValue({
      data: {
        success: true,
        balance_deducted: 50,
        gift_balance_deducted: 10,
      },
    })
  })

  it('loads separate wallet balances and reports both refund deductions', async () => {
    const wrapper = mount(AdminOrdersView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          OrderTable: OrderTableStub,
          AdminRefundDialog: AdminRefundDialogStub,
          BaseDialog: true,
          Icon: true,
          OrderStatusBadge: true,
          Pagination: true,
          Select: true,
        },
      },
    })

    await flushPromises()
    const refundButton = wrapper.findAll('button').find((button) => (
      button.text().includes('payment.admin.refund')
    ))
    expect(refundButton).toBeDefined()
    await refundButton!.trigger('click')
    await flushPromises()

    expect(getAdminUserByID).toHaveBeenCalledWith(9, true)
    expect(wrapper.get('[data-test="wallet"]').text()).toBe('80|12')

    await wrapper.get('[data-test="confirm-refund"]').trigger('click')
    await flushPromises()

    expect(refundOrder).toHaveBeenCalledWith(12, {
      amount: 50,
      reason: 'partial',
      deduct_balance: true,
      force: false,
    })
    expect(showSuccess).toHaveBeenCalledTimes(1)
    const message = String(showSuccess.mock.calls[0]?.[0] || '')
    expect(message).toContain('payment.admin.refundSuccessWithDeductions')
    expect(message).toContain('$50.00')
    expect(message).toContain('$10.00')
    expect(message).toContain('$60.00')
  })

  it('shows topup delivery and email status without a refund action, and resends explicitly', async () => {
    const topupOrder = {
      ...balanceOrder,
      order_type: 'subscription_topup', currency: 'CNY', topup_quantity: 2,
      topup_snapshot: { product_name: 'Chat subscription', sku_name: 'Monthly', instructions: 'Redeem on provider site', unit_price: 50 },
      topup_delivery_status: 'delivered', topup_email_status: 'failed',
    }
    getOrders.mockResolvedValue({ data: { items: [topupOrder], total: 1 } })
    getOrder.mockResolvedValue({ data: { order: topupOrder, audit_logs: [] } })
    resendCards.mockResolvedValue({ data: {} })
    const wrapper = mount(AdminOrdersView, {
      global: { stubs: {
        AppLayout: { template: '<div><slot /></div>' }, OrderTable: OrderTableStub,
        AdminRefundDialog: AdminRefundDialogStub,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        Icon: true, OrderStatusBadge: true, Pagination: true, Select: true,
      } },
    })
    await flushPromises()
    expect(wrapper.findAll('button').some(button => button.text() === 'payment.admin.refund')).toBe(false)
    expect(wrapper.text()).toContain('subscriptionTopup.refundNotice')
    expect(resendCards).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(button => button.text() === 'common.view')!.trigger('click')
    await flushPromises()
    const detail = wrapper.get('[data-testid="topup-order-detail"]')
    expect(detail.text()).toContain('Chat subscription · Monthly')
    expect(detail.text()).toContain('subscriptionTopup.mailFailed')
    expect(detail.text()).toContain('subscriptionTopup.delivered')
    await detail.get('button').trigger('click')
    await flushPromises()
    expect(resendCards).toHaveBeenCalledWith(12)
    expect(refundOrder).not.toHaveBeenCalled()
    expect(getAdminUserByID).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores old and closed detail responses and binds resend to the confirmed selection', async () => {
    const makeTopup = (id: number) => ({
      ...balanceOrder, id, order_type: 'subscription_topup', topup_quantity: 1,
      topup_snapshot: { product_name: `Product ${id}`, sku_name: 'Monthly', instructions: '', unit_price: 100 },
      topup_delivery_status: 'delivered', topup_email_status: 'failed',
    })
    const first = makeTopup(12)
    const second = makeTopup(13)
    type DetailResponse = { data: { order: ReturnType<typeof makeTopup>; audit_logs: [] } }
    let resolveFirst!: (response: DetailResponse) => void
    let resolveSecond!: (response: DetailResponse) => void
    getOrders.mockResolvedValue({ data: { items: [first, second], total: 2 } })
    getOrder.mockReturnValueOnce(new Promise(resolve => { resolveFirst = resolve }))
      .mockReturnValueOnce(new Promise(resolve => { resolveSecond = resolve }))
    resendCards.mockResolvedValue({ data: {} })
    const wrapper = mount(AdminOrdersView, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' }, OrderTable: OrderTableStub,
      AdminRefundDialog: true,
      BaseDialog: { props: ['show'], emits: ['close'], template: '<div v-if="show"><button data-test="close-detail" @click="$emit(\'close\')">Close</button><slot /></div>' },
      Icon: true, OrderStatusBadge: true, Pagination: true, Select: true,
    } } })
    await flushPromises()
    const viewButtons = wrapper.findAll('button').filter(button => button.text() === 'common.view')
    await viewButtons[0]!.trigger('click')
    expect(wrapper.get('[data-testid="topup-order-detail"] button').attributes('disabled')).toBeDefined()
    await viewButtons[1]!.trigger('click')
    expect((getOrder.mock.calls[0]?.[1] as AbortSignal).aborted).toBe(true)
    resolveSecond({ data: { order: second, audit_logs: [] } })
    await flushPromises()
    resolveFirst({ data: { order: first, audit_logs: [] } })
    await flushPromises()
    expect(wrapper.get('[data-testid="topup-order-detail"]').text()).toContain('Product 13')
    getOrder.mockResolvedValueOnce({ data: { order: second, audit_logs: [] } })
    await wrapper.get('[data-testid="topup-order-detail"] button').trigger('click')
    await flushPromises()
    expect(resendCards).toHaveBeenCalledWith(13)

    let resolveClosed!: (response: DetailResponse) => void
    getOrder.mockReturnValueOnce(new Promise(resolve => { resolveClosed = resolve }))
    await viewButtons[0]!.trigger('click')
    await wrapper.get('[data-test="close-detail"]').trigger('click')
    expect((getOrder.mock.calls.at(-1)?.[1] as AbortSignal).aborted).toBe(true)
    resolveClosed({ data: { order: first, audit_logs: [] } })
    await flushPromises()
    expect(wrapper.find('[data-testid="topup-order-detail"]').exists()).toBe(false)
    expect(resendCards).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('shows released reservations as closed and does not offer email resend', async () => {
    const closedOrder = { ...balanceOrder, order_type: 'subscription_topup', status: 'CANCELLED', topup_delivery_status: 'released', topup_email_status: 'not_queued' }
    getOrders.mockResolvedValue({ data: { items: [closedOrder], total: 1 } })
    getOrder.mockResolvedValue({ data: { order: closedOrder, audit_logs: [] } })
    const wrapper = mount(AdminOrdersView, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' }, OrderTable: OrderTableStub,
      AdminRefundDialog: true, BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true, OrderStatusBadge: true, Pagination: true, Select: true,
    } } })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.view')!.trigger('click')
    await flushPromises()
    const detail = wrapper.get('[data-testid="topup-order-detail"]')
    expect(detail.text()).toContain('subscriptionTopup.released')
    expect(detail.text()).toContain('subscriptionTopup.notSent')
    expect(detail.find('button').exists()).toBe(false)
    wrapper.unmount()
  })
})
