<template>
  <div class="space-y-5">
    <div v-if="loading" class="card p-8 text-center" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="error" class="card p-6" role="alert">
      <p class="text-red-600 dark:text-red-400">{{ error }}</p>
      <button class="btn btn-secondary mt-3" @click="loadProducts">{{ t('common.retry') }}</button>
    </div>
    <p v-else-if="products.length === 0" class="card p-8 text-center text-gray-500">{{ t('subscriptionTopup.noProducts') }}</p>
    <template v-else>
      <div class="grid gap-4 sm:grid-cols-2">
        <section v-for="product in products" :key="product.id" class="card space-y-3 p-5">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ product.name }}</h2>
          <p v-if="product.description" class="whitespace-pre-wrap text-sm text-gray-500 dark:text-gray-400">{{ product.description }}</p>
          <button v-for="sku in product.skus" :key="sku.id" type="button"
            :disabled="sku.available_stock < 1 || submitting"
            :aria-pressed="selected?.sku.id === sku.id"
            class="flex w-full items-center justify-between gap-3 rounded-xl border p-3 text-left disabled:opacity-50"
            :class="selected?.sku.id === sku.id ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20' : 'border-gray-200 dark:border-dark-600'"
            @click="select(product, sku)">
            <span><span class="block font-medium text-gray-900 dark:text-white">{{ sku.name }}</span>
              <span class="text-xs text-gray-500">{{ sku.available_stock > 0 ? t('subscriptionTopup.remaining', { count: sku.available_stock }) : t('subscriptionTopup.soldOut') }}</span></span>
            <span class="font-semibold text-primary-600 dark:text-primary-400">{{ money(sku.price) }}</span>
          </button>
        </section>
      </div>
      <section v-if="selected" class="card space-y-4 p-5">
        <h2 class="font-semibold text-gray-900 dark:text-white">{{ selected.product.name }} · {{ selected.sku.name }}</h2>
        <label class="block text-sm text-gray-700 dark:text-gray-300">{{ t('subscriptionTopup.quantity') }}
          <input v-model.number="quantity" data-testid="topup-quantity" type="number" min="1" :max="Math.min(100, selected.sku.available_stock)" step="1" class="input mt-2 w-32" :disabled="submitting" />
        </label>
        <p v-if="selected.product.instructions" class="whitespace-pre-wrap text-sm text-gray-500 dark:text-gray-400">{{ selected.product.instructions }}</p>
        <PaymentMethodSelector :methods="methodOptions" :selected="selectedMethod" @select="selectedMethod = $event" />
        <p v-if="methodOptions.length === 0" class="text-sm text-amber-600 dark:text-amber-300">{{ t('subscriptionTopup.noCnyMethod') }}</p>
        <div class="space-y-2 text-sm text-gray-700 dark:text-gray-300">
          <div class="flex justify-between"><span>{{ t('payment.paymentAmount') }}</span><span>{{ money(subtotal) }}</span></div>
          <div v-if="feeRate > 0" class="flex justify-between"><span>{{ t('payment.fee') }} ({{ feeRate }}%)</span><span>{{ money(fee) }}</span></div>
        </div>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('subscriptionTopup.refundNotice') }}</p>
        <button class="btn btn-primary w-full" data-testid="topup-buy" :disabled="!canBuy || submitting" @click="purchase">
          {{ submitting ? t('common.processing') : `${t('payment.createOrder')} ${money(total)}` }}
        </button>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { paymentAPI } from '@/api/payment'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { MethodLimit } from '@/types/payment'
import type { SubscriptionTopupProduct, SubscriptionTopupSKU } from '@/types/subscriptionTopup'
import PaymentMethodSelector from './PaymentMethodSelector.vue'
import { formatPaymentAmount, normalizePaymentCurrency } from './currency'

const props = defineProps<{ methods: Record<string, MethodLimit>; feeRate: number; submitting: boolean }>()
const emit = defineEmits<{ purchase: [selection: { skuId: number; quantity: number; amount: number; paymentType: string }] }>()
const { t, locale } = useI18n()
const products = ref<SubscriptionTopupProduct[]>([])
const selected = ref<{ product: SubscriptionTopupProduct; sku: SubscriptionTopupSKU } | null>(null)
const quantity = ref(1)
const selectedMethod = ref('')
const loading = ref(false)
const error = ref('')
let request: AbortController | undefined
const subtotal = computed(() => Math.round((selected.value?.sku.price ?? 0) * 100) * (Number.isInteger(quantity.value) ? quantity.value : 0) / 100)
const fee = computed(() => Math.ceil(subtotal.value * props.feeRate) / 100)
const total = computed(() => Math.round((subtotal.value + fee.value) * 100) / 100)
const methodOptions = computed(() => Object.entries(props.methods)
  .filter(([, method]) => normalizePaymentCurrency(method.currency) === 'CNY')
  .map(([type, method]) => ({ type, display_name: method.display_name, fee_rate: method.fee_rate,
    available: method.available !== false && (method.single_min <= 0 || total.value >= method.single_min)
      && (method.single_max <= 0 || total.value <= method.single_max) })))
const canBuy = computed(() => selected.value !== null && Number.isInteger(quantity.value) && quantity.value >= 1
  && quantity.value <= Math.min(100, selected.value.sku.available_stock)
  && methodOptions.value.some(method => method.type === selectedMethod.value && method.available))
watch(methodOptions, methods => {
  if (methods.some(method => method.type === selectedMethod.value && method.available)) return
  selectedMethod.value = methods.find(method => method.available)?.type ?? ''
}, { immediate: true })

function money(value: number) { return formatPaymentAmount(value, 'CNY', locale.value) }
function select(product: SubscriptionTopupProduct, sku: SubscriptionTopupSKU) {
  selected.value = { product, sku }
  quantity.value = 1
}
function purchase() {
  if (!canBuy.value || !selected.value || props.submitting) return
  emit('purchase', { skuId: selected.value.sku.id, quantity: quantity.value, amount: subtotal.value, paymentType: selectedMethod.value })
}
async function loadProducts() {
  request?.abort()
  const current = new AbortController()
  request = current
  loading.value = true
  error.value = ''
  try {
    const result = await paymentAPI.getTopupProducts(current.signal)
    if (current.signal.aborted) return
    products.value = result.data
  } catch (cause: unknown) {
    if (!current.signal.aborted) error.value = extractApiErrorMessage(cause, t('common.error'))
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
onMounted(loadProducts)
onBeforeUnmount(() => request?.abort())
</script>
