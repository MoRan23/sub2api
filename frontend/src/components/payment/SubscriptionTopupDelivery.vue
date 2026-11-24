<template>
  <section class="space-y-4" data-testid="topup-delivery">
    <div class="flex items-center justify-between gap-3">
      <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('subscriptionTopup.cards') }}</h3>
      <button class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="loading && !delivery" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <template v-if="delivery">
      <p class="text-sm font-medium text-gray-900 dark:text-white">{{ delivery.product_name }} · {{ delivery.sku_name }} × {{ delivery.quantity }}</p>
      <div class="space-y-1 text-sm text-gray-500 dark:text-gray-400">
        <p>{{ t('subscriptionTopup.deliveryStatus') }}: {{ deliveryLabel }}</p>
        <p>{{ t('subscriptionTopup.emailStatus') }}: {{ emailLabel }}</p>
      </div>
      <div v-if="delivery.cards.length" class="space-y-2">
        <div v-for="(code, index) in delivery.cards" :key="index" class="flex items-start gap-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
          <code class="min-w-0 flex-1 break-all text-sm text-gray-900 dark:text-white">{{ code }}</code>
          <button class="btn btn-secondary btn-sm shrink-0" @click="copyToClipboard(code)">{{ t('subscriptionTopup.copy') }}</button>
        </div>
        <button class="btn btn-secondary" @click="copyToClipboard(delivery.cards.join('\n'))">{{ t('subscriptionTopup.copyAll') }}</button>
      </div>
      <div v-if="delivery.instructions" class="space-y-1">
        <h4 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('subscriptionTopup.instructions') }}</h4>
        <p class="whitespace-pre-wrap break-words text-sm text-gray-600 dark:text-gray-300">{{ delivery.instructions }}</p>
      </div>
    </template>
    <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('subscriptionTopup.refundNotice') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { paymentAPI } from '@/api/payment'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { SubscriptionTopupOrderCards } from '@/types/subscriptionTopup'

const props = defineProps<{ orderId: number }>()
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const delivery = ref<SubscriptionTopupOrderCards | null>(null)
const loading = ref(false)
const error = ref('')
let request: AbortController | undefined
const deliveryLabel = computed(() => {
  const status = delivery.value?.delivery_status
  if (status === 'delivered' || status === 'issued') return t('subscriptionTopup.delivered')
  if (status === 'waiting_stock') return t('subscriptionTopup.waiting_stock')
  if (status === 'reserved') return t('subscriptionTopup.reserved')
  if (status === 'released') return t('subscriptionTopup.released')
  return t('subscriptionTopup.awaitingPayment')
})
const emailLabel = computed(() => {
  const status = delivery.value?.email_status
  if (status === 'sent') return t('subscriptionTopup.mailSent')
  if (status === 'failed') return t('subscriptionTopup.mailFailed')
  if (status === 'pending' || status === 'queued' || status === 'sending') return t('subscriptionTopup.mailPending')
  return t('subscriptionTopup.notSent')
})
async function load() {
  request?.abort()
  const current = new AbortController()
  request = current
  loading.value = true
  error.value = ''
  try {
    const response = await paymentAPI.getOrderCards(props.orderId, current.signal)
    if (!current.signal.aborted) delivery.value = response.data
  } catch (cause: unknown) {
    if (!current.signal.aborted) error.value = extractApiErrorMessage(cause, t('common.error'))
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
watch(() => props.orderId, () => { delivery.value = null; void load() }, { immediate: true })
onBeforeUnmount(() => request?.abort())
</script>
