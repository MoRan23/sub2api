import { describe, expect, it } from 'vitest'

import {
  calculateRefundGiftAmount,
  formatWalletAmount,
  paymentOrderTotalCredit,
  walletAmountLessThan,
} from '../orderUtils'

describe('payment order gift amounts', () => {
  it('calculates proportional and full gift recovery with eight-decimal wallet rounding', () => {
    expect(calculateRefundGiftAmount(100, 10, 99.995)).toBe(9.9995)
    expect(calculateRefundGiftAmount(100, 10, 100.000000004)).toBe(10)
    expect(calculateRefundGiftAmount(3, 1, 1)).toBe(0.33333333)
  })

  it('formats wallet precision without dropping the two-decimal minimum', () => {
    expect(formatWalletAmount(12)).toBe('12.00')
    expect(formatWalletAmount(12.5)).toBe('12.50')
    expect(formatWalletAmount(12.12345678)).toBe('12.12345678')
  })

  it('keeps wallet comparisons and total credit independent from gateway currency amounts', () => {
    expect(walletAmountLessThan(4.999999996, 5)).toBe(false)
    expect(walletAmountLessThan(4.99999998, 5)).toBe(true)
    expect(paymentOrderTotalCredit({ amount: 100, gift_amount: 20 })).toBe(120)
  })
})
