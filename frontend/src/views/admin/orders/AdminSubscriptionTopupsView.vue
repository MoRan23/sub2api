<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex flex-wrap justify-end gap-2">
        <button class="btn btn-secondary" :disabled="loading" @click="loadProducts">{{ t('common.refresh') }}</button>
        <button class="btn btn-primary" @click="editProduct()">{{ t('subscriptionTopup.admin.createProduct') }}</button>
      </div>
      <div v-if="!loading && products.length === 0" class="card p-10 text-center text-gray-500">{{ t('subscriptionTopup.admin.noProducts') }}</div>
      <section v-for="product in products" :key="product.id" class="card p-5">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="break-words text-lg font-semibold text-gray-900 dark:text-white">{{ product.name }}</h2>
            <p class="mt-1 whitespace-pre-wrap text-sm text-gray-500 dark:text-gray-400">{{ product.description }}</p>
            <span :class="product.enabled ? 'badge badge-success' : 'badge badge-gray'">{{ t(product.enabled ? 'subscriptionTopup.admin.enabled' : 'subscriptionTopup.admin.disabled') }}</span>
          </div>
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-secondary btn-sm" @click="editProduct(product)">{{ t('common.edit') }}</button>
            <button class="btn btn-secondary btn-sm" @click="archiveTarget = { kind: 'product', id: product.id }">{{ t('subscriptionTopup.admin.archive') }}</button>
            <button class="btn btn-primary btn-sm" @click="editSKU(product.id)">{{ t('subscriptionTopup.admin.createSKU') }}</button>
          </div>
        </div>
        <div class="mt-4 overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="text-xs text-gray-500 dark:text-gray-400"><tr>
              <th class="py-2">{{ t('subscriptionTopup.admin.skuName') }}</th><th>{{ t('subscriptionTopup.price') }}</th>
              <th>{{ t('subscriptionTopup.stock') }}</th><th>{{ t('common.status') }}</th><th class="text-right">{{ t('common.actions') }}</th>
            </tr></thead>
            <tbody>
              <tr v-for="sku in product.skus" :key="sku.id" class="border-t border-gray-100 dark:border-dark-700">
                <td class="py-3 pr-3 text-gray-900 dark:text-white">{{ sku.name }}</td>
                <td class="pr-3 tabular-nums">¥{{ sku.price.toFixed(2) }}</td><td class="pr-3 tabular-nums">{{ sku.available_stock }}</td>
                <td class="pr-3">{{ t(sku.enabled ? 'subscriptionTopup.admin.enabled' : 'subscriptionTopup.admin.disabled') }}</td>
                <td><div class="flex justify-end gap-2 whitespace-nowrap">
                  <button class="btn btn-secondary btn-sm" @click="openPool(sku)">{{ t('subscriptionTopup.admin.pool') }}</button>
                  <button class="btn btn-secondary btn-sm" @click="editSKU(product.id, sku)">{{ t('common.edit') }}</button>
                  <button class="btn btn-secondary btn-sm" @click="archiveTarget = { kind: 'sku', id: sku.id }">{{ t('subscriptionTopup.admin.archive') }}</button>
                </div></td>
              </tr>
              <tr v-if="product.skus.length === 0"><td colspan="5" class="py-4 text-center text-gray-500">{{ t('subscriptionTopup.admin.noSKUs') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <BaseDialog :show="productDialog" :title="t(productID ? 'subscriptionTopup.admin.editProduct' : 'subscriptionTopup.admin.createProduct')" @close="productDialog = false">
      <form class="space-y-4" @submit.prevent="saveProduct">
        <label class="block text-sm">{{ t('subscriptionTopup.admin.productName') }}<input v-model="productForm.name" class="input mt-1" required maxlength="100" /></label>
        <label class="block text-sm">{{ t('subscriptionTopup.admin.description') }}<textarea v-model="productForm.description" class="input mt-1" rows="3" maxlength="4000" /></label>
        <label class="block text-sm">{{ t('subscriptionTopup.instructions') }}<textarea v-model="productForm.instructions" class="input mt-1" rows="5" maxlength="10000" /></label>
        <label class="block text-sm">{{ t('subscriptionTopup.admin.sortOrder') }}<input v-model.number="productForm.sort_order" type="number" step="1" class="input mt-1" required /></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="productForm.enabled" type="checkbox" />{{ t('subscriptionTopup.admin.enabled') }}</label>
        <div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" @click="productDialog = false">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button></div>
      </form>
    </BaseDialog>

    <BaseDialog :show="skuDialog" :title="t(skuID ? 'subscriptionTopup.admin.editSKU' : 'subscriptionTopup.admin.createSKU')" @close="skuDialog = false">
      <form class="space-y-4" @submit.prevent="saveSKU">
        <label class="block text-sm">{{ t('subscriptionTopup.admin.skuName') }}<input v-model="skuForm.name" class="input mt-1" required maxlength="100" /></label>
        <label class="block text-sm">{{ t('subscriptionTopup.price') }}<input v-model.number="skuForm.price" class="input mt-1" type="number" min="0.01" max="1000000000000" step="0.01" required /></label>
        <label class="block text-sm">{{ t('subscriptionTopup.admin.sortOrder') }}<input v-model.number="skuForm.sort_order" type="number" step="1" class="input mt-1" required /></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="skuForm.enabled" type="checkbox" />{{ t('subscriptionTopup.admin.enabled') }}</label>
        <div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" @click="skuDialog = false">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button></div>
      </form>
    </BaseDialog>

    <BaseDialog :show="pool !== null" :title="`${t('subscriptionTopup.admin.pool')} · ${pool?.name || ''}`" width="wide" @close="closePool">
      <div class="space-y-4">
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
          <label class="block text-sm font-medium" for="topup-codes">{{ t('subscriptionTopup.admin.import') }}</label>
          <p class="my-2 text-xs text-gray-500">{{ t('subscriptionTopup.admin.importHint') }}</p>
          <textarea id="topup-codes" v-model="importText" class="input font-mono" rows="5" autocomplete="off" spellcheck="false" />
          <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
            <label class="text-sm">{{ t('subscriptionTopup.admin.importFile') }}<input type="file" accept=".txt,text/plain" class="mt-1 block text-xs" @change="readImportFile" /></label>
            <button class="btn btn-primary btn-sm" :disabled="busy || !importText.trim()" @click="importCodes">{{ t('subscriptionTopup.admin.import') }}</button>
          </div>
        </div>
        <div class="flex flex-wrap gap-x-5 gap-y-2 text-sm text-gray-500 dark:text-gray-400">
          <span v-for="status in cardStatuses" :key="status">{{ cardStatusLabel(status) }}: <strong class="tabular-nums text-gray-900 dark:text-white">{{ cards.counts[status] }}</strong></span>
        </div>
        <div class="flex items-center justify-between gap-3">
          <select v-model="cardStatus" class="input max-w-48" @change="changeCardFilter">
            <option value="">{{ t('subscriptionTopup.admin.allStatuses') }}</option>
            <option v-for="status in cardStatuses" :key="status" :value="status">{{ cardStatusLabel(status) }}</option>
          </select>
          <button class="btn btn-secondary btn-sm" :disabled="cardsLoading" @click="loadCards">{{ t('common.refresh') }}</button>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm"><thead class="text-xs text-gray-500"><tr>
            <th class="py-2">{{ t('subscriptionTopup.admin.maskedCode') }}</th><th>{{ t('common.status') }}</th><th>{{ t('subscriptionTopup.admin.order') }}</th><th class="text-right">{{ t('common.actions') }}</th>
          </tr></thead><tbody>
            <tr v-for="card in cards.items" :key="card.id" class="border-t border-gray-100 dark:border-dark-700">
              <td class="max-w-sm break-all py-3 pr-3 font-mono">{{ revealedCodes[card.id] ?? card.masked_code }}</td>
              <td class="pr-3">{{ cardStatusLabel(card.status) }}</td><td class="pr-3">{{ card.order_id ? `#${card.order_id}` : '—' }}</td>
              <td><div class="flex justify-end gap-2 whitespace-nowrap">
                <button class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleReveal(card)">{{ t(revealedCodes[card.id] ? 'subscriptionTopup.admin.hideCode' : 'subscriptionTopup.admin.showCode') }}</button>
                <button v-if="card.status === 'available' || card.status === 'disabled'" class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleCard(card)">{{ t(card.status === 'available' ? 'subscriptionTopup.admin.disable' : 'subscriptionTopup.admin.enable') }}</button>
              </div></td>
            </tr>
            <tr v-if="!cardsLoading && cards.items.length === 0"><td colspan="4" class="py-6 text-center text-gray-500">{{ t('subscriptionTopup.admin.noCards') }}</td></tr>
          </tbody></table>
        </div>
        <Pagination v-if="cards.total > 0" :page="cardPage" :total="cards.total" :page-size="20" @update:page="changeCardPage" />
      </div>
    </BaseDialog>

    <ConfirmDialog :show="archiveTarget !== null" :title="t('subscriptionTopup.admin.archive')" :message="t('subscriptionTopup.admin.archiveConfirm')" :confirm-text="t('subscriptionTopup.admin.archive')" danger @confirm="archive" @cancel="archiveTarget = null" />
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import { adminSubscriptionTopupsAPI as api } from '@/api/admin/subscriptionTopups'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { SubscriptionTopupProduct, SubscriptionTopupSKU, SubscriptionTopupCard, TopupCardPage, TopupCardStatus, TopupProductInput, TopupSKUInput } from '@/types/subscriptionTopup'

const { t } = useI18n()
const app = useAppStore()
const products = ref<SubscriptionTopupProduct[]>([])
const loading = ref(false)
const busy = ref(false)
const productDialog = ref(false)
const productID = ref<number | null>(null)
const productForm = reactive<TopupProductInput>({ name: '', description: '', instructions: '', enabled: false, sort_order: 0 })
const skuDialog = ref(false)
const skuID = ref<number | null>(null)
const skuProductID = ref(0)
const skuForm = reactive<TopupSKUInput>({ name: '', price: 0, enabled: false, sort_order: 0 })
const archiveTarget = ref<{ kind: 'product' | 'sku'; id: number } | null>(null)
const pool = ref<SubscriptionTopupSKU | null>(null)
const cards = ref<TopupCardPage>({ items: [], total: 0, page: 1, page_size: 20, counts: { available: 0, reserved: 0, issued: 0, disabled: 0 } })
const cardsLoading = ref(false)
const cardPage = ref(1)
const cardStatus = ref<TopupCardStatus | ''>('')
const cardStatuses: TopupCardStatus[] = ['available', 'reserved', 'issued', 'disabled']
const importText = ref('')
const revealedCodes = ref<Record<number, string>>({})
let productsRequest: AbortController | undefined
let cardsRequest: AbortController | undefined
let poolGeneration = 0

function error(err: unknown) {
  app.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
}

async function loadProducts() {
  productsRequest?.abort()
  const request = new AbortController()
  productsRequest = request
  loading.value = true
  try {
    const result = await api.listProducts(request.signal)
    if (request.signal.aborted) return
    products.value = result.data
  } catch (err: unknown) {
    if (!request.signal.aborted) error(err)
  } finally {
    if (!request.signal.aborted) loading.value = false
  }
}

function editProduct(product?: SubscriptionTopupProduct) {
  productID.value = product?.id ?? null
  Object.assign(productForm, { name: product?.name ?? '', description: product?.description ?? '', instructions: product?.instructions ?? '', enabled: product?.enabled ?? false, sort_order: product?.sort_order ?? 0 })
  productDialog.value = true
}

async function saveProduct() {
  if (busy.value) return
  busy.value = true
  try {
    await (productID.value ? api.updateProduct(productID.value, productForm) : api.createProduct(productForm))
    productDialog.value = false
    await loadProducts()
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

function editSKU(product: number, sku?: SubscriptionTopupSKU) {
  skuID.value = sku?.id ?? null
  skuProductID.value = product
  Object.assign(skuForm, { name: sku?.name ?? '', price: sku?.price ?? 0, enabled: sku?.enabled ?? false, sort_order: sku?.sort_order ?? 0 })
  skuDialog.value = true
}

async function saveSKU() {
  if (busy.value) return
  busy.value = true
  try {
    await (skuID.value ? api.updateSKU(skuID.value, skuForm) : api.createSKU(skuProductID.value, skuForm))
    skuDialog.value = false
    await loadProducts()
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

async function archive() {
  if (busy.value || !archiveTarget.value) return
  busy.value = true
  try {
    await (archiveTarget.value.kind === 'product' ? api.archiveProduct(archiveTarget.value.id) : api.archiveSKU(archiveTarget.value.id))
    archiveTarget.value = null
    await loadProducts()
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

function closePool() {
  poolGeneration += 1
  cardsRequest?.abort()
  pool.value = null
  revealedCodes.value = {}
  importText.value = ''
}

function openPool(sku: SubscriptionTopupSKU) {
  closePool()
  pool.value = sku
  cardPage.value = 1
  cardStatus.value = ''
  void loadCards()
}

async function loadCards() {
  if (!pool.value) return
  poolGeneration += 1
  cardsRequest?.abort()
  const request = new AbortController()
  cardsRequest = request
  revealedCodes.value = {}
  cards.value = { items: [], total: 0, page: cardPage.value, page_size: 20, counts: { available: 0, reserved: 0, issued: 0, disabled: 0 } }
  cardsLoading.value = true
  try {
    const result = await api.listCards(pool.value.id, { page: cardPage.value, page_size: 20, status: cardStatus.value || undefined }, request.signal)
    if (!request.signal.aborted) cards.value = result.data
  } catch (err: unknown) {
    if (!request.signal.aborted) error(err)
  } finally {
    if (!request.signal.aborted) cardsLoading.value = false
  }
}

function changeCardFilter() { cardPage.value = 1; void loadCards() }
function changeCardPage(page: number) { cardPage.value = page; void loadCards() }
function cardStatusLabel(status: TopupCardStatus) { return t(`subscriptionTopup.admin.${status === 'disabled' ? 'disabledCard' : status}`) }

async function readImportFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const generation = poolGeneration
  input.value = ''
  if (file.size > 1024 * 1024) { app.showError(t('subscriptionTopup.admin.invalidFile')); return }
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(await file.arrayBuffer())
    if (generation === poolGeneration) importText.value = text
  } catch { app.showError(t('subscriptionTopup.admin.invalidFile')) }
}

async function importCodes() {
  if (!pool.value || busy.value) return
  const codes = importText.value.split(/\r?\n/).map(code => code.trim()).filter(Boolean)
  const encoder = new TextEncoder()
  if (!codes.length || codes.length > 1000 || codes.some(code => encoder.encode(code).length > 512)) {
    app.showError(t('subscriptionTopup.admin.invalidImport')); return
  }
  if (new Set(codes).size !== codes.length) { app.showError(t('subscriptionTopup.admin.duplicateImport')); return }
  const generation = poolGeneration
  busy.value = true
  try {
    const result = await api.importCards(pool.value.id, codes)
    app.showSuccess(t('subscriptionTopup.admin.importSuccess', { count: result.data.imported }))
    if (generation === poolGeneration) { importText.value = ''; await loadCards() }
    await loadProducts()
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

async function toggleReveal(card: SubscriptionTopupCard) {
  if (busy.value) return
  if (revealedCodes.value[card.id]) { delete revealedCodes.value[card.id]; return }
  const generation = poolGeneration
  busy.value = true
  try {
    const result = await api.revealCard(card.id)
    if (generation === poolGeneration) revealedCodes.value[card.id] = result.data.code
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

async function toggleCard(card: SubscriptionTopupCard) {
  if (busy.value) return
  const generation = poolGeneration
  busy.value = true
  try {
    await api.setCardEnabled(card.id, card.status === 'disabled')
    if (generation === poolGeneration) await loadCards()
    await loadProducts()
  } catch (err: unknown) { error(err) }
  finally { busy.value = false }
}

onMounted(loadProducts)
onUnmounted(() => { productsRequest?.abort(); closePool() })
</script>
