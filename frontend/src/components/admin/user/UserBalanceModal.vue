<template>
  <BaseDialog :show="show" :title="operation === 'add' ? t('admin.users.deposit') : t('admin.users.withdraw')" width="narrow" @close="$emit('close')">
    <form v-if="user" id="balance-form" @submit.prevent="handleBalanceSubmit" class="space-y-5">
      <div class="flex items-center gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-primary-100"><span class="text-lg font-medium text-primary-700">{{ user.email.charAt(0).toUpperCase() }}</span></div>
        <div class="flex-1">
          <p class="font-medium text-gray-900 dark:text-gray-100">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.users.currentBalance') }}: ${{ formatBalance(totalBalance) }}
          </p>
          <p class="text-xs text-gray-400 dark:text-dark-500">
            {{ t('admin.users.ordinaryBalance') }} ${{ formatBalance(ordinaryBalance) }} ·
            {{ t('admin.users.giftBalance') }} ${{ formatBalance(giftBalance) }}
          </p>
        </div>
      </div>
      <div>
        <label class="input-label">{{ operation === 'add' ? t('admin.users.ordinaryDepositAmount') : t('admin.users.ordinaryWithdrawAmount') }}</label>
        <div class="relative flex gap-2">
          <div class="relative flex-1"><div class="absolute left-3 top-1/2 -translate-y-1/2 font-medium text-gray-500">$</div><input v-model.number="form.amount" data-test="ordinary-amount" type="number" step="any" min="0" class="input pl-8" /></div>
          <button v-if="operation === 'subtract'" data-test="withdraw-all-ordinary" type="button" @click="fillAllBalance('ordinary')" class="btn btn-secondary whitespace-nowrap">{{ t('admin.users.withdrawAllBalance') }}</button>
        </div>
      </div>
      <div>
        <label class="input-label">{{ operation === 'add' ? t('admin.users.giftDepositAmount') : t('admin.users.giftWithdrawAmount') }}</label>
        <div class="relative flex gap-2">
          <div class="relative flex-1">
            <div class="absolute left-3 top-1/2 -translate-y-1/2 font-medium text-gray-500">$</div>
            <input v-model.number="form.giftAmount" data-test="gift-amount" type="number" step="any" min="0" class="input pl-8" />
          </div>
          <button v-if="operation === 'subtract'" data-test="withdraw-all-gift" type="button" @click="fillAllBalance('gift')" class="btn btn-secondary whitespace-nowrap">{{ t('admin.users.withdrawAllBalance') }}</button>
        </div>
        <p v-if="operation === 'add'" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.users.giftDepositHint') }}</p>
      </div>
      <div><label class="input-label">{{ t('admin.users.notes') }}</label><textarea v-model="form.notes" rows="3" class="input"></textarea></div>
      <div v-if="hasAdjustment" class="space-y-2 rounded-xl border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-950">
        <div class="flex items-center justify-between text-xs text-gray-600 dark:text-gray-400"><span>{{ t('admin.users.ordinaryBalance') }}</span><span data-test="new-ordinary-balance">${{ formatBalance(newOrdinaryBalance) }}</span></div>
        <div class="flex items-center justify-between text-xs text-gray-600 dark:text-gray-400"><span>{{ t('admin.users.giftBalance') }}</span><span data-test="new-gift-balance">${{ formatBalance(newGiftBalance) }}</span></div>
        <div class="flex items-center justify-between border-t border-blue-200 pt-2 text-sm dark:border-blue-800"><span class="text-gray-700 dark:text-gray-300">{{ t('admin.users.newBalance') }}:</span><span data-test="new-total-balance" class="font-bold text-gray-900 dark:text-gray-100">${{ formatBalance(newTotalBalance) }}</span></div>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="balance-form" :disabled="submitting || !hasAdjustment" class="btn" :class="operation === 'add' ? 'bg-emerald-600 text-white' : 'btn-danger'">{{ submitting ? t('common.saving') : t('common.confirm') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { AdminUser } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean, user: AdminUser | null, operation: 'add' | 'subtract' }>()
const emit = defineEmits(['close', 'success']); const { t } = useI18n(); const appStore = useAppStore()

const submitting = ref(false)
const form = reactive<{ amount: number | string; giftAmount: number | string; notes: string }>({ amount: 0, giftAmount: 0, notes: '' })
watch([() => props.show, () => props.user?.id, () => props.operation], () => {
  if (props.show) { form.amount = 0; form.giftAmount = 0; form.notes = '' }
})

const ordinaryBalance = computed(() => Number(props.user?.balance || 0))
const giftBalance = computed(() => Number(props.user?.gift_balance || 0))
const totalBalance = computed(() => Number(props.user?.total_balance ?? ordinaryBalance.value + giftBalance.value))
// An empty optional wallet input is zero; invalid/non-finite input cannot be submitted.
const ordinaryAmount = computed(() => Number(form.amount))
const giftAmount = computed(() => Number(form.giftAmount))
const hasAdjustment = computed(() => (
  Number.isFinite(ordinaryAmount.value) && Number.isFinite(giftAmount.value) &&
  ordinaryAmount.value >= 0 && giftAmount.value >= 0 &&
  (ordinaryAmount.value > 0 || giftAmount.value > 0)
))

// 格式化余额：显示完整精度，去除尾部多余的0
const formatBalance = (value: number) => {
  if (value === 0) return '0.00'
  // 最多保留8位小数，去除尾部的0
  const formatted = value.toFixed(8).replace(/\.?0+$/, '')
  // 确保至少有2位小数
  const parts = formatted.split('.')
  if (parts.length === 1) return formatted + '.00'
  if (parts[1].length === 1) return formatted + '0'
  return formatted
}

// 填入全部余额
const fillAllBalance = (wallet: 'ordinary' | 'gift') => {
  if (props.user) {
    if (wallet === 'ordinary') form.amount = Math.max(0, ordinaryBalance.value)
    else form.giftAmount = Math.max(0, giftBalance.value)
  }
}

const normalizeZero = (value: number) => Math.abs(value) < 1e-10 ? 0 : value
const newOrdinaryBalance = computed(() => normalizeZero(
  props.operation === 'add' ? ordinaryBalance.value + ordinaryAmount.value : ordinaryBalance.value - ordinaryAmount.value
))
const newGiftBalance = computed(() => normalizeZero(
  props.operation === 'add' ? giftBalance.value + giftAmount.value : giftBalance.value - giftAmount.value
))
const newTotalBalance = computed(() => normalizeZero(newOrdinaryBalance.value + newGiftBalance.value))

const handleBalanceSubmit = async () => {
  if (!props.user || submitting.value) return
  if (!hasAdjustment.value) {
    appStore.showError(t('admin.users.amountRequired'))
    return
  }
  // 退款时验证金额不超过实际余额
  if (props.operation === 'subtract' && ordinaryAmount.value > 0 && ordinaryAmount.value > ordinaryBalance.value) {
    appStore.showError(t('admin.users.insufficientOrdinaryBalance'))
    return
  }
  if (props.operation === 'subtract' && giftAmount.value > 0 && giftAmount.value > giftBalance.value) {
    appStore.showError(t('admin.users.insufficientGiftBalance'))
    return
  }
  submitting.value = true
  try {
    await adminAPI.users.updateBalance(
      props.user.id,
      ordinaryAmount.value,
      props.operation,
      form.notes,
      giftAmount.value
    )
    appStore.showSuccess(t('common.success')); emit('success'); emit('close')
  } catch (e: any) {
    console.error('Failed to update balance:', e)
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally { submitting.value = false }
}
</script>
