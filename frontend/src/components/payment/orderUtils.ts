/**
 * Shared utility functions for payment order display.
 * Used by AdminOrderDetail, AdminOrderTable, AdminRefundDialog, AdminOrdersView, etc.
 */

import type { PaymentOrder } from '@/types/payment'

const STATUS_BADGE_MAP: Record<string, string> = {
  PENDING: 'badge-warning',
  PAID: 'badge-info',
  RECHARGING: 'badge-info',
  COMPLETED: 'badge-success',
  EXPIRED: 'badge-secondary',
  CANCELLED: 'badge-secondary',
  FAILED: 'badge-danger',
  REFUND_REQUESTED: 'badge-warning',
  REFUNDING: 'badge-warning',
  REFUND_PENDING: 'badge-warning',
  PARTIALLY_REFUNDED: 'badge-warning',
  REFUNDED: 'badge-info',
  REFUND_FAILED: 'badge-danger',
}

const REFUNDABLE_STATUSES = ['COMPLETED', 'PARTIALLY_REFUNDED', 'REFUND_REQUESTED', 'REFUND_FAILED']

export function statusBadgeClass(status: string): string {
  return STATUS_BADGE_MAP[status] || 'badge-secondary'
}

export function canRefund(status: string): boolean {
  return REFUNDABLE_STATUSES.includes(status)
}

export function formatOrderDateTime(dateStr: string): string {
  if (!dateStr) return '-'
  return new Date(dateStr).toLocaleString()
}

export function paymentOrderGiftAmount(order: Pick<PaymentOrder, 'gift_amount'> | null | undefined): number {
  return Number(order?.gift_amount || 0)
}

export function paymentOrderTotalCredit(order: Pick<PaymentOrder, 'amount' | 'gift_amount'> | null | undefined): number {
  return Number(order?.amount || 0) + paymentOrderGiftAmount(order)
}

export function formatWalletAmount(value: number): string {
  const normalized = Number.isFinite(value) ? value : 0
  const formatted = normalized.toFixed(8).replace(/\.?0+$/, '')
  const parts = formatted.split('.')
  if (parts.length === 1) return `${formatted}.00`
  if (parts[1].length === 1) return `${formatted}0`
  return formatted
}

export function calculateRefundGiftAmount(orderAmount: number, giftAmount: number, refundAmount: number): number {
  if (orderAmount <= 0 || giftAmount <= 0 || refundAmount <= 0) return 0
  const roundWallet = (value: number) => Math.round(value * 1e8) / 1e8
  const roundedOrder = roundWallet(orderAmount)
  const roundedRefund = roundWallet(refundAmount)
  if (roundedRefund === roundedOrder) return roundWallet(giftAmount)
  return roundWallet((giftAmount * roundedRefund) / roundedOrder)
}

export function walletAmountLessThan(actual: number, requested: number): boolean {
  const roundWallet = (value: number) => Math.round(value * 1e8) / 1e8
  return roundWallet(actual) < roundWallet(requested)
}
