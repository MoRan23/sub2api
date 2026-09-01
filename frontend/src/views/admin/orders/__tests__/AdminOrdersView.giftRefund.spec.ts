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
})
