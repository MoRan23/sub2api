<template>
  <BaseDialog
    :show="show"
    :title="t('payment.admin.refundOrder')"
    width="normal"
    @close="emit('cancel')"
  >
    <form id="refund-form" @submit.prevent="handleSubmit" class="space-y-4">
      <!-- Refund Request Info -->
      <div
        v-if="order?.refund_requested_at || order?.refund_request_reason"
        class="rounded-lg border border-violet-200 bg-violet-50 p-3 dark:border-violet-800 dark:bg-violet-900/20"
      >
        <div class="flex items-center gap-2 text-sm font-medium text-violet-700 dark:text-violet-300">
          <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
          {{ t('payment.admin.refundRequestInfo') }}
        </div>
        <div v-if="order?.refund_requested_at" class="mt-2 flex justify-between text-sm">
          <span class="text-violet-600 dark:text-violet-400">{{ t('payment.admin.refundRequestedAt') }}</span>
          <span class="text-violet-800 dark:text-violet-200">{{ formatDateTime(order.refund_requested_at) }}</span>
        </div>
        <div v-if="order?.refund_request_reason" class="mt-1 text-sm">
          <span class="text-violet-600 dark:text-violet-400">{{ t('payment.admin.refundRequestReason') }}:</span>
          <span class="ml-1 text-violet-800 dark:text-violet-200">{{ order.refund_request_reason }}</span>
        </div>
      </div>

      <!-- Order Info -->
      <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
        <div class="flex justify-between text-sm">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
          <span class="font-mono text-gray-900 dark:text-white">#{{ order?.id }}</span>
        </div>
        <div class="mt-1 flex justify-between text-sm">
          <span class="text-gray-500 dark:text-gray-400">
            {{ t(order?.order_type === 'balance' ? 'payment.orders.ordinaryCredit' : 'payment.orders.creditedAmount') }}
          </span>
          <span class="font-medium text-gray-900 dark:text-white">{{ creditedAmountSymbol }}{{ formatWalletAmount(order?.amount || 0) }}</span>
        </div>
        <div v-if="isBalanceOrder" class="mt-1 flex justify-between text-sm">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.giftCredit') }}</span>
          <span class="font-medium text-emerald-600 dark:text-emerald-400">{{ creditedAmountSymbol }}{{ formatWalletAmount(orderGiftAmount) }}</span>
        </div>
        <div v-if="isBalanceOrder" class="mt-1 flex justify-between border-t border-gray-200 pt-1 text-sm dark:border-dark-600">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.totalCredit') }}</span>
          <span class="font-semibold text-gray-900 dark:text-white">{{ creditedAmountSymbol }}{{ formatWalletAmount(orderTotalCredit) }}</span>
        </div>
        <div class="mt-1 flex justify-between text-sm">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.payAmount') }}</span>
          <span class="font-medium text-gray-900 dark:text-white">{{ paymentAmountSymbol }}{{ order?.pay_amount?.toFixed(2) }}</span>
        </div>
        <div v-if="actuallyRefunded > 0" class="mt-1 flex justify-between text-sm">
          <span class="text-gray-500 dark:text-gray-400">{{ t('payment.admin.alreadyRefunded') }}</span>
          <span class="font-medium text-red-600 dark:text-red-400">{{ creditedAmountSymbol }}{{ actuallyRefunded.toFixed(2) }}</span>
        </div>
      </div>

      <!-- Deduct Balance -->
      <div>
        <div class="flex items-center gap-2">
          <input
            id="deduct-balance"
            v-model="form.deduct_balance"
            type="checkbox"
            class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
          <label for="deduct-balance" class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('payment.admin.deductBalance') }}
          </label>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.admin.deductBalanceHint') }}</span>
        </div>

        <div v-if="isBalanceOrder && form.deduct_balance && walletLoading" class="mt-3 text-xs text-gray-500 dark:text-gray-400">
          {{ t('payment.admin.loadingWallet') }}
        </div>

        <!-- Separate wallet availability; total balance cannot prove either wallet is sufficient. -->
        <div v-else-if="isBalanceOrder && form.deduct_balance && walletSnapshotAvailable" class="mt-3 grid grid-cols-2 gap-3">
          <div v-if="availableOrdinaryBalance != null" class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
            <div class="text-gray-500 dark:text-gray-400">{{ t('payment.admin.userOrdinaryBalance') }}</div>
            <div class="mt-1 font-semibold text-gray-900 dark:text-white">{{ creditedAmountSymbol }}{{ formatWalletAmount(availableOrdinaryBalance || 0) }}</div>
          </div>
          <div v-if="availableGiftBalance != null" class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
            <div class="text-gray-500 dark:text-gray-400">{{ t('payment.admin.userGiftBalance') }}</div>
            <div class="mt-1 font-semibold text-gray-900 dark:text-white">{{ creditedAmountSymbol }}{{ formatWalletAmount(availableGiftBalance || 0) }}</div>
          </div>
        </div>

        <!-- Insufficient balance warning -->
        <div
          v-if="isBalanceOrder && form.deduct_balance && walletInsufficient"
          class="mt-2 rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
        >
          <p v-if="ordinaryBalanceInsufficient">
            {{ t('payment.admin.ordinaryBalanceInsufficient', {
              available: formatWalletAmount(availableOrdinaryBalance || 0),
              required: formatWalletAmount(estimatedOrdinaryRecovery)
            }) }}
          </p>
          <p v-if="giftBalanceInsufficient">
            {{ t('payment.admin.giftBalanceInsufficient', {
              available: formatWalletAmount(availableGiftBalance || 0),
              required: formatWalletAmount(estimatedGiftRecovery)
            }) }}
          </p>
        </div>

        <!-- No deduction info -->
        <div
          v-if="!form.deduct_balance"
          class="mt-2 rounded-lg bg-blue-50 p-3 text-sm text-blue-700 dark:bg-blue-900/20 dark:text-blue-300"
        >
          {{ t('payment.admin.noDeduction') }}
        </div>
      </div>

      <!-- Refund Amount -->
      <div>
        <label class="input-label">{{ t('payment.admin.refundAmount') }}</label>
        <div class="relative">
          <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500">{{ creditedAmountSymbol }}</span>
          <input
            v-model.number="form.amount"
            type="number"
            step="0.01"
            min="0.01"
            :max="maxRefundable"
            class="input pl-7"
            required
          />
        </div>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('payment.admin.maxRefundable') }}: {{ creditedAmountSymbol }}{{ maxRefundable.toFixed(2) }}
        </p>
        <div v-if="isBalanceOrder && form.deduct_balance && form.amount > 0" class="mt-2 rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-700">
          <div class="flex justify-between text-gray-500 dark:text-gray-400">
            <span>{{ t('payment.admin.estimatedOrdinaryRecovery') }}</span>
            <span>{{ creditedAmountSymbol }}{{ formatWalletAmount(estimatedOrdinaryRecovery) }}</span>
          </div>
          <div class="flex justify-between text-gray-500 dark:text-gray-400">
            <span>{{ t('payment.admin.estimatedGiftRecovery') }}</span>
            <span>{{ creditedAmountSymbol }}{{ formatWalletAmount(estimatedGiftRecovery) }}</span>
          </div>
          <div class="mt-1 flex justify-between font-medium text-gray-700 dark:text-gray-200">
            <span>{{ t('payment.admin.estimatedTotalRecovery') }}</span>
            <span>{{ creditedAmountSymbol }}{{ formatWalletAmount(estimatedTotalRecovery) }}</span>
          </div>
        </div>
      </div>

      <!-- Reason -->
      <div>
        <label class="input-label">{{ t('payment.admin.refundReason') }}</label>
        <textarea
          v-model="form.reason"
          rows="3"
          class="input"
          :placeholder="t('payment.admin.refundReasonPlaceholder')"
          required
        ></textarea>
      </div>

      <!-- Warning -->
      <div
        v-if="warning"
        class="rounded-lg bg-yellow-50 p-3 text-sm text-yellow-700 dark:bg-yellow-900/20 dark:text-yellow-300"
      >
        {{ warning }}
      </div>

      <!-- Force Refund -->
      <div v-if="requireForce" class="flex items-center gap-2">
        <input
          id="force-refund"
          v-model="form.force"
          type="checkbox"
          class="h-4 w-4 rounded border-gray-300 text-red-600 focus:ring-red-500"
        />
        <label for="force-refund" class="text-sm font-medium text-red-600 dark:text-red-400">
          {{ t('payment.admin.forceRefund') }}
        </label>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" @click="emit('cancel')" class="btn btn-secondary">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="refund-form"
          :disabled="submitting || form.amount <= 0 || (requireForce && !form.force)"
          class="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 focus:outline-none focus:ring-2 focus:ring-red-500 focus:ring-offset-2 disabled:opacity-50 dark:focus:ring-offset-dark-800"
        >
          {{ submitting ? t('common.processing') : t('payment.admin.confirmRefund') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { PaymentOrder } from '@/types/payment'
import { currencySymbol } from '@/components/payment/currency'
import {
  calculateRefundGiftAmount,
  formatOrderDateTime,
  formatWalletAmount,
  paymentOrderGiftAmount,
  paymentOrderTotalCredit,
  walletAmountLessThan
} from '@/components/payment/orderUtils'

const { t } = useI18n()

const props = defineProps<{
  show: boolean
  order: PaymentOrder | null
  submitting?: boolean
  /** Legacy callers may pass ordinary balance through this prop. It is never treated as total balance. */
  userBalance?: number | null
  userOrdinaryBalance?: number | null
  userGiftBalance?: number | null
  walletLoading?: boolean
  requireForce?: boolean
  warning?: string
}>()

const emit = defineEmits<{
  (e: 'confirm', data: { amount: number; reason: string; deduct_balance: boolean; force: boolean }): void
  (e: 'cancel'): void
}>()

const creditedAmountSymbol = currencySymbol('USD')

const paymentAmountSymbol = computed(() => currencySymbol(props.order?.currency))

const isBalanceOrder = computed(() => props.order?.order_type === 'balance')
const orderGiftAmount = computed(() => paymentOrderGiftAmount(props.order))
const orderTotalCredit = computed(() => paymentOrderTotalCredit(props.order))

const form = reactive({
  amount: 0,
  reason: '',
  deduct_balance: true,
  force: false,
})

// In REFUND_REQUESTED / REFUND_PENDING status, refund_amount is requested/pending, not actually refunded.
// Only PARTIALLY_REFUNDED / REFUNDED have real refund amounts.
const actuallyRefunded = computed(() => {
  if (!props.order) return 0
  const s = props.order.status
  if (s === 'PARTIALLY_REFUNDED' || s === 'REFUNDED') return props.order.refund_amount || 0
  return 0
})

const maxRefundable = computed(() => {
  if (!props.order) return 0
  return props.order.amount - actuallyRefunded.value
})

const estimatedGiftRecovery = computed(() => {
  if (!props.order || !isBalanceOrder.value) return 0
  return calculateRefundGiftAmount(props.order.amount, orderGiftAmount.value, form.amount)
})

const estimatedOrdinaryRecovery = computed(() => Math.max(0, Number(form.amount) || 0))
const estimatedTotalRecovery = computed(() => estimatedOrdinaryRecovery.value + estimatedGiftRecovery.value)
const availableOrdinaryBalance = computed<number | null>(() => {
  const value = props.userOrdinaryBalance ?? props.userBalance
  return value == null ? null : Number(value)
})
const availableGiftBalance = computed<number | null>(() => (
  props.userGiftBalance == null ? null : Number(props.userGiftBalance)
))
const walletSnapshotAvailable = computed(() => (
  availableOrdinaryBalance.value != null || availableGiftBalance.value != null
))
const ordinaryBalanceInsufficient = computed(() => (
  isBalanceOrder.value &&
  availableOrdinaryBalance.value != null &&
  walletAmountLessThan(availableOrdinaryBalance.value || 0, estimatedOrdinaryRecovery.value)
))
const giftBalanceInsufficient = computed(() => (
  isBalanceOrder.value &&
  estimatedGiftRecovery.value > 0 &&
  availableGiftBalance.value != null &&
  walletAmountLessThan(availableGiftBalance.value || 0, estimatedGiftRecovery.value)
))
const walletInsufficient = computed(() => ordinaryBalanceInsufficient.value || giftBalanceInsufficient.value)

watch(() => props.show, (val) => {
  if (val && props.order) {
    // For REFUND_REQUESTED, pre-fill with the requested amount
    if (props.order.status === 'REFUND_REQUESTED' && props.order.refund_amount) {
      form.amount = props.order.refund_amount
    } else {
      form.amount = maxRefundable.value
    }
    form.reason = props.order.refund_request_reason || ''
    form.deduct_balance = true
    form.force = false
  }
})

function formatDateTime(dateStr: string): string {
  return formatOrderDateTime(dateStr)
}

function handleSubmit() {
  if (form.amount <= 0 || form.amount > maxRefundable.value) return
  if (props.requireForce && !form.force) return
  emit('confirm', { ...form })
}
</script>
