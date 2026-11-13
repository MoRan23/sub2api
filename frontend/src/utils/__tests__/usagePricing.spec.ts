import { describe, expect, it } from 'vitest'
import { accountBilledCost, grokSafetyFee, userTokenCost } from '../usagePricing'

describe('Grok safety rejection costs', () => {
  it('keeps existing account multipliers for ordinary usage', () => {
    expect(accountBilledCost({ total_cost: 2, account_rate_multiplier: 3 })).toBe(6)
    expect(accountBilledCost({ total_cost: 2, account_stats_cost: 4, account_rate_multiplier: 3 })).toBe(12)
    expect(accountBilledCost(null)).toBe(0)
  })

  it.each([0, 0.5, 1, 3])('adds the fixed fee once with account multiplier %s', multiplier => {
    const row = { total_cost: 2.25, actual_cost: 4.25, account_rate_multiplier: multiplier, grok_safety_rejection: { fee: 0.25 } }
    expect(accountBilledCost(row)).toBe(2 * multiplier + 0.25)
    expect(accountBilledCost({ ...row, account_stats_cost: 5.25 })).toBe(5 * multiplier + 0.25)
    expect(userTokenCost(row)).toBe(4)
    expect(grokSafetyFee(row)).toBe(0.25)
  })

  it('supports fixed-fee-only, zero-fee and eight-decimal records', () => {
    expect(accountBilledCost({ total_cost: 0.05, actual_cost: 0.05, account_rate_multiplier: 100, grok_safety_rejection: { fee: 0.05 } })).toBe(0.05)
    expect(userTokenCost({ actual_cost: 0.05, grok_safety_rejection: { fee: 0.05 } })).toBe(0)
    expect(accountBilledCost({ total_cost: 0.00000003, account_rate_multiplier: 2, grok_safety_rejection: { fee: 0.00000001 } })).toBeCloseTo(0.00000005, 12)
    expect(accountBilledCost({ total_cost: 2, account_rate_multiplier: 3, grok_safety_rejection: { fee: 0 } })).toBe(6)
  })

  it('does not display non-finite costs or accept a malformed fixed fee', () => {
    expect(accountBilledCost({ total_cost: Number.NaN })).toBe(0)
    expect(accountBilledCost({ account_rate_multiplier: Infinity, total_cost: 1 })).toBe(0)
    for (const fee of [-1, NaN, Infinity]) expect(grokSafetyFee({ grok_safety_rejection: { fee } })).toBe(0)
  })
})
