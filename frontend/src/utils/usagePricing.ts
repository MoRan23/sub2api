export const TOKENS_PER_MILLION = 1_000_000

interface UsageCosts {
  total_cost?: number | null
  actual_cost?: number | null
  account_stats_cost?: number | null
  account_rate_multiplier?: number | null
  grok_safety_rejection?: { fee: number } | null
}

/** A confirmed safety fee is charged once, outside all token multipliers. */
export function grokSafetyFee(row: UsageCosts | null | undefined): number {
  const fee = row?.grok_safety_rejection?.fee
  return typeof fee === 'number' && Number.isFinite(fee) && fee >= 0 ? fee : 0
}

export function userTokenCost(row: UsageCosts | null | undefined): number {
  return Math.max(0, (row?.actual_cost ?? 0) - grokSafetyFee(row))
}

export function accountBilledCost(row: UsageCosts | null | undefined): number {
  const base = row?.account_stats_cost ?? row?.total_cost ?? 0
  const fee = grokSafetyFee(row)
  const cost = (base - fee) * (row?.account_rate_multiplier ?? 1) + fee
  return Number.isFinite(cost) ? cost : 0
}

interface TokenPriceFormatOptions {
  fractionDigits?: number
  withCurrencySymbol?: boolean
  emptyValue?: string
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

export function calculateTokenUnitPrice(
  cost: number | null | undefined,
  tokens: number | null | undefined
): number | null {
  if (!isFiniteNumber(cost) || !isFiniteNumber(tokens) || tokens <= 0) {
    return null
  }

  return cost / tokens
}

export function calculateTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined
): number | null {
  const unitPrice = calculateTokenUnitPrice(cost, tokens)
  if (unitPrice == null) {
    return null
  }

  return unitPrice * TOKENS_PER_MILLION
}

export function formatTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined,
  options: TokenPriceFormatOptions = {}
): string {
  const pricePerMillion = calculateTokenPricePerMillion(cost, tokens)
  if (pricePerMillion == null) {
    return options.emptyValue ?? '-'
  }

  const fractionDigits = options.fractionDigits ?? 4
  const formatted = pricePerMillion.toFixed(fractionDigits)
  return options.withCurrencySymbol == false ? formatted : `$${formatted}`
}
