import { describe, expect, it } from 'vitest'
import { formatBalanceRechargeMultiplier } from '../paymentMultiplier'

describe('balance recharge multiplier display', () => {
  it('formats balance recharge multiplier with up to six decimals', () => {
    expect(formatBalanceRechargeMultiplier(0.140845)).toBe('0.140845')
    expect(formatBalanceRechargeMultiplier(0.15)).toBe('0.15')
    expect(formatBalanceRechargeMultiplier(0.5)).toBe('0.50')
    expect(formatBalanceRechargeMultiplier(1)).toBe('1.00')
    expect(formatBalanceRechargeMultiplier(0.1408454)).toBe('0.140845')
    expect(formatBalanceRechargeMultiplier(Number.NaN)).toBe('1.00')
  })
})
