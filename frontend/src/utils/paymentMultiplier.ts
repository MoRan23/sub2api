export const BALANCE_RECHARGE_MULTIPLIER_MAX_DECIMALS = 6

// 余额充值倍率展示：最多 6 位小数、至少 2 位（0.140845 → "0.140845"，0.5 → "0.50"，1 → "1.00"）。
export function formatBalanceRechargeMultiplier(value: number): string {
  const normalized = Number.isFinite(value) && value > 0 ? value : 1
  const text = String(Number(normalized.toFixed(BALANCE_RECHARGE_MULTIPLIER_MAX_DECIMALS)))
  const [integerPart, fractionPart = ''] = text.split('.')
  return `${integerPart}.${fractionPart.padEnd(2, '0')}`
}
