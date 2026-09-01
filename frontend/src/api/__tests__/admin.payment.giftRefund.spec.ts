import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import { adminPaymentAPI, type RefundResult } from '@/api/admin/payment'

describe('admin payment gift refund contract', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('preserves ordinary and gift wallet deductions returned by the backend', async () => {
    const result: RefundResult = {
      success: true,
      balance_deducted: 50,
      gift_balance_deducted: 10,
    }
    post.mockResolvedValue({ data: result })

    const response = await adminPaymentAPI.refundOrder(12, {
      amount: 50,
      reason: 'partial',
      deduct_balance: true,
      force: false,
    })

    expect(post).toHaveBeenCalledWith('/admin/payment/orders/12/refund', {
      amount: 50,
      reason: 'partial',
      deduct_balance: true,
      force: false,
    })
    expect(response.data.gift_balance_deducted).toBe(10)
  })
})
