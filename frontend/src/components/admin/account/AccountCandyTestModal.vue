<template>
  <BaseDialog :show="show" :title="t('candyTests.title')" width="extra-wide" @close="emit('close')">
    <div class="space-y-5">
      <div class="space-y-1 text-sm text-gray-500 dark:text-gray-400">
        <p class="font-medium text-gray-800 dark:text-gray-200">{{ t('candyTests.selected', { count: frozenAccountIds.length }) }}</p>
        <p>{{ t('candyTests.description') }}</p>
        <p>{{ t('candyTests.backgroundNotice') }}</p>
      </div>

      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>

      <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label for="candy-test-model" class="mb-1 block text-sm font-medium">{{ t('candyTests.model') }}</label>
            <Select id="candy-test-model" v-model="model" :options="modelOptions" :searchable="modelOptions.length > 5" :disabled="loading || creating" :aria-label="t('candyTests.model')" />
          </div>
          <div>
            <label for="candy-test-effort" class="mb-1 block text-sm font-medium">{{ t('candyTests.effort') }}</label>
            <Select id="candy-test-effort" v-model="effort" :options="effortOptions" :disabled="loading || creating" :aria-label="t('candyTests.effort')" />
          </div>
        </div>
        <p class="mt-3 text-xs text-gray-500" data-testid="candy-model-source">{{ t(hasExcelCatalog ? 'candyTests.excelModelSource' : 'candyTests.modelSource') }}</p>
        <p v-if="hasExcelCatalog" class="mt-1 text-xs text-gray-500">{{ t('candyTests.excelDefaultEffort') }}</p>
        <p v-if="loading" role="status" class="mt-3 text-sm text-gray-500">{{ t('candyTests.loadingModels', { count: frozenAccountIds.length }) }}</p>
        <p v-if="!loading && !modelOptions.length" class="mt-3 text-sm text-gray-500">{{ t('candyTests.noModels') }}</p>
        <div v-if="modelLoadFailures.length" class="mt-3 rounded-lg bg-amber-50 p-3 text-sm dark:bg-amber-900/20" data-testid="candy-model-failures">
          <p class="font-medium text-amber-800 dark:text-amber-200">{{ t('candyTests.modelLoadFailures') }}</p>
          <ul class="mt-2 max-h-40 space-y-1 overflow-auto text-amber-700 dark:text-amber-300">
            <li v-for="account in modelLoadFailures" :key="account.account_id">{{ account.account_name || `#${account.account_id}` }}: {{ optionFailureReason(account.skip_reason) }}</li>
          </ul>
        </div>
        <p class="mt-3 text-xs text-gray-500">{{ t('candyTests.unsupportedNotice') }}</p>
        <div class="mt-3 flex flex-col items-start justify-between gap-3 sm:flex-row sm:items-center">
          <p class="text-xs text-amber-700 dark:text-amber-300">{{ t('candyTests.quotaNotice') }}</p>
          <button type="button" class="btn btn-primary shrink-0" data-testid="candy-start" :disabled="loading || creating || !model || !frozenAccountIds.length" @click="startBatch">
            {{ t(creating ? 'candyTests.starting' : 'candyTests.start') }}
          </button>
        </div>
      </div>

      <div v-if="knownBatchIds.length" class="flex flex-wrap items-center gap-2">
        <label for="candy-test-batch" class="text-sm font-medium">{{ t('candyTests.batch') }}</label>
        <Select id="candy-test-batch" :model-value="batchId" :options="knownBatchIds.map(id => ({ value: id, label: id }))" :aria-label="t('candyTests.batch')" @update:model-value="selectBatch(String($event))" />
      </div>

      <section v-if="batch" class="space-y-3" aria-live="polite">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <p class="text-sm font-medium">{{ t('candyTests.progress', { completed: completedCount, total: batch.total }) }}</p>
          <button v-if="batchActive" type="button" class="btn btn-secondary text-sm" data-testid="candy-cancel-batch" :disabled="cancelling" @click="cancelTests()">{{ t('candyTests.cancelBatch') }}</button>
        </div>
        <div class="flex flex-wrap gap-2 text-xs">
          <span v-for="status in statuses.filter(status => batch?.counts[status])" :key="status" class="flex items-center gap-1">
            <AccountCandyTestStatus :status="status" /> {{ batch.counts[status] }}
          </span>
        </div>
        <div class="overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-600">
          <table class="w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800">
              <tr><th class="px-3 py-2">{{ t('candyTests.account') }}</th><th class="px-3 py-2">{{ t('candyTests.status') }}</th><th class="px-3 py-2">{{ t('candyTests.model') }}</th><th class="px-3 py-2">{{ t('candyTests.completedAt') }}</th><th class="px-3 py-2"><span class="sr-only">{{ t('candyTests.details') }}</span></th></tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="item in batch.items" :key="item.id" :data-testid="`candy-item-${item.id}`">
                <td class="px-3 py-3">{{ item.account_name || `#${item.account_id}` }}</td>
                <td class="px-3 py-3"><AccountCandyTestStatus :status="item.status" /><div v-if="item.cancel_requested && isCandyTestActive(item.status)" class="mt-1 text-xs text-gray-500">{{ t('candyTests.cancelRequested') }}</div></td>
                <td class="px-3 py-3 text-xs">{{ item.model }}<div class="mt-1 text-gray-500">{{ item.reasoning_effort || t('candyTests.defaultEffort') }}</div></td>
                <td class="px-3 py-3 text-xs text-gray-500">{{ formatDateTime(item.finished_at) || '—' }}</td>
                <td class="px-3 py-3"><div class="flex flex-wrap gap-3">
                  <button type="button" class="text-xs text-primary-600 dark:text-primary-400" @click="selectedItem = item">{{ t('candyTests.details') }}</button>
                  <button v-if="isCandyTestActive(item.status)" type="button" class="text-xs text-gray-500" :disabled="cancelling || item.cancel_requested" @click="cancelTests([item.id])">{{ t('candyTests.cancelItem') }}</button>
                </div></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="totalPages > 1" class="flex items-center justify-end gap-3 text-sm">
          <button type="button" class="btn btn-secondary" :disabled="page <= 1 || refreshing" @click="changePage(page - 1)">{{ t('candyTests.previous') }}</button>
          <span>{{ t('candyTests.page', { page, pages: totalPages }) }}</span>
          <button type="button" class="btn btn-secondary" :disabled="page >= totalPages || refreshing" @click="changePage(page + 1)">{{ t('candyTests.next') }}</button>
        </div>
      </section>

      <AccountCandyTestResult v-if="selectedItem" :item="selectedItem" expanded />

      <section class="space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h4 class="font-medium">{{ t('candyTests.history') }}</h4>
          <Select v-if="historyAccountOptions.length > 1" :model-value="historyAccountId" :options="historyAccountOptions" :aria-label="t('candyTests.historyAccount')" searchable @update:model-value="changeHistoryAccount(Number($event))" />
        </div>
        <p class="text-xs text-gray-500">{{ t('candyTests.retentionNotice') }}</p>
        <p v-if="!historyLoading && !history.length" class="py-3 text-sm text-gray-400">{{ t('candyTests.noHistory') }}</p>
        <details v-for="item in history" :key="item.id" class="rounded-xl border border-gray-200 p-3 dark:border-dark-600">
          <summary class="cursor-pointer text-sm">
            <AccountCandyTestStatus :status="item.status" />
            <span class="mx-2">{{ item.model }} · {{ item.reasoning_effort || t('candyTests.defaultEffort') }}</span>
            <span class="text-xs text-gray-500">{{ formatDateTime(item.finished_at) }}</span>
          </summary>
          <AccountCandyTestResult class="mt-3" :item="item" />
        </details>
      </section>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" data-testid="candy-refresh" :disabled="loading || refreshing || creating" @click="refreshVisible(true)">{{ t('candyTests.refresh') }}</button>
      <button type="button" class="btn btn-primary" @click="emit('close')">{{ t('candyTests.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { candyTestsAPI, isCandyTestActive } from '@/api/admin/candyTests'
import type { CandyTestBatch, CandyTestItem, CandyTestOptions, CandyTestStatus, CandyTestSummary } from '@/api/admin/candyTests'
import { formatDateTime } from '@/utils/format'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import AccountCandyTestStatus from './AccountCandyTestStatus.vue'
import AccountCandyTestResult from './AccountCandyTestResult.vue'

const props = withDefaults(defineProps<{ show: boolean; accountIds: number[]; accounts?: Account[] }>(), { accounts: () => [] })
const emit = defineEmits<{ (event: 'close'): void; (event: 'updated'): void }>()
const { t, te } = useI18n()
const statuses: CandyTestStatus[] = ['queued', 'running', 'normal', 'abnormal', 'failed', 'cancelled', 'skipped']
const frozenAccountIds = ref<number[]>([])
const options = ref<CandyTestOptions>({ models: [], accounts: [] })
const hasExcelCatalog = computed(() => options.value.accounts.some(account => account.catalog_source === 'excel_builtin' || account.upstream_kind === 'excel'))
const loading = ref(false)
const creating = ref(false)
const refreshing = ref(false)
const cancelling = ref(false)
const error = ref('')
const model = ref('')
const effort = ref('')
const batchId = ref('')
const batch = ref<CandyTestBatch | null>(null)
const knownBatchIds = ref<string[]>([])
const page = ref(1)
const selectedItem = ref<CandyTestItem | null>(null)
const historyAccountId = ref<number>(0)
const history = ref<CandyTestItem[]>([])
const historyLoading = ref(false)
let lifecycle = 0
let batchRequest = 0
let historyRequest = 0
let timer: ReturnType<typeof setTimeout> | undefined
let submission: { fingerprint: string; key: string } | undefined
let optionsController: AbortController | undefined

const modelOptions = computed(() => options.value.models.map(option => ({ value: option.id, label: option.display_name || option.id })))
const modelLoadFailures = computed(() => options.value.accounts.filter(account => account.skip_reason))
const effortOptions = computed(() => [
  { value: '', label: t('candyTests.defaultEffort') },
  ...new Set((options.value.models.find(option => option.id === model.value)?.reasoning_efforts || []).filter(Boolean)),
].map(option => typeof option === 'string' ? { value: option, label: option } : option))
const historyAccountOptions = computed(() => frozenAccountIds.value.map(id => ({
  value: id,
  label: options.value.accounts.find(account => account.account_id === id)?.account_name || props.accounts.find(account => account.id === id)?.name || `#${id}`,
})))
const batchActive = computed(() => Boolean(batch.value && ((batch.value.counts.queued || 0) + (batch.value.counts.running || 0) > 0)))
const completedCount = computed(() => batch.value ? batch.value.total - (batch.value.counts.queued || 0) - (batch.value.counts.running || 0) : 0)
const totalPages = computed(() => batch.value ? Math.max(1, Math.ceil(batch.value.retained_total / batch.value.page_size)) : 1)

watch(model, () => { effort.value = '' })
watch(() => props.show, (show) => {
  lifecycle++
  stopPolling()
  optionsController?.abort()
  if (show) void initialize()
}, { immediate: true })
onBeforeUnmount(() => { lifecycle++; stopPolling(); optionsController?.abort() })

function isCurrent(version: number): boolean { return props.show && lifecycle === version }
function stopPolling() { if (timer) clearTimeout(timer); timer = undefined }
function schedulePoll() {
  stopPolling()
  if (!props.show || !batchId.value || (batch.value && !batchActive.value)) return
  timer = setTimeout(() => { void refreshVisible() }, 5000)
}
function rememberBatch(id: string) {
  if (id && !knownBatchIds.value.includes(id)) knownBatchIds.value.push(id)
}

async function initialize() {
  frozenAccountIds.value = [...new Set(props.accountIds)]
  options.value = { models: [], accounts: [] }
  batchId.value = ''
  batch.value = null
  knownBatchIds.value = []
  selectedItem.value = null
  history.value = []
  error.value = ''
  model.value = ''
  loading.value = true
  refreshing.value = false
  historyLoading.value = false
  historyAccountId.value = frozenAccountIds.value[0] || 0
  for (const account of props.accounts) {
    if (!frozenAccountIds.value.includes(account.id)) continue
    const summary = (account as Account & { candy_test?: CandyTestSummary }).candy_test
    if (summary?.active) rememberBatch(summary.active.batch_id)
  }
  if (knownBatchIds.value[0]) void selectBatch(knownBatchIds.value[0])
  await Promise.all([loadOptions(), loadHistory()])
}

function optionFailureReason(reason?: string): string {
  const key = `candyTests.failureReasons.${reason}`
  return reason && te(key) ? t(key) : t('candyTests.modelLoadError')
}

async function loadOptions() {
  const version = lifecycle
  optionsController?.abort()
  const controller = new AbortController()
  optionsController = controller
  loading.value = true
  try {
    const result = await candyTestsAPI.options(frozenAccountIds.value, controller.signal)
    if (!isCurrent(version) || controller.signal.aborted) return
    options.value = { models: result.models || [], accounts: result.accounts || [] }
    if (!options.value.models.some(option => option.id === model.value)) model.value = options.value.models[0]?.id || ''
    if (!effortOptions.value.some(option => option.value === effort.value)) effort.value = ''
  } catch {
    if (isCurrent(version) && !controller.signal.aborted) {
      options.value = { models: [], accounts: [] }
      model.value = ''
      error.value = t('candyTests.modelLoadError')
    }
  } finally {
    if (optionsController === controller) {
      optionsController = undefined
      if (isCurrent(version)) loading.value = false
    }
  }
}

async function loadHistory() {
  const version = lifecycle
  const request = ++historyRequest
  const accountId = historyAccountId.value
  if (!accountId) return
  historyLoading.value = true
  try {
    const result = await candyTestsAPI.history(accountId)
    if (!isCurrent(version) || request !== historyRequest || accountId !== historyAccountId.value) return
    history.value = result.items || []
    const activeId = result.summary?.active?.batch_id
    if (activeId) {
      rememberBatch(activeId)
      if (!batchId.value) await selectBatch(activeId)
    }
  } catch {
    if (isCurrent(version) && request === historyRequest) error.value = t('candyTests.loadError')
  } finally {
    if (isCurrent(version) && request === historyRequest) historyLoading.value = false
  }
}

async function selectBatch(id: string) {
  if (!id) return
  stopPolling()
  batchId.value = id
  batch.value = null
  page.value = 1
  selectedItem.value = null
  rememberBatch(id)
  await loadBatch()
}

async function loadBatch() {
  const id = batchId.value
  if (!id) return
  const version = lifecycle
  const request = ++batchRequest
  refreshing.value = true
  try {
    const result = await candyTestsAPI.getBatch(id, page.value)
    if (!isCurrent(version) || request !== batchRequest || id !== batchId.value) return
    const changed = batch.value?.finished_at !== result.finished_at || JSON.stringify(batch.value?.counts) !== JSON.stringify(result.counts)
    batch.value = result
    if (selectedItem.value) selectedItem.value = result.items?.find(item => item.id === selectedItem.value?.id) || selectedItem.value
    if (changed) emit('updated')
  } catch {
    if (isCurrent(version) && request === batchRequest) error.value = t('candyTests.loadError')
  } finally {
    if (isCurrent(version) && request === batchRequest) {
      refreshing.value = false
      schedulePoll()
    }
  }
}

async function refreshVisible(includeOptions = false) {
  if (!props.show) return
  error.value = ''
  await Promise.all([loadBatch(), loadHistory(), ...(includeOptions ? [loadOptions()] : [])])
}

async function startBatch() {
  if (creating.value || !model.value) return
  const version = lifecycle
  const payload = { account_ids: [...frozenAccountIds.value], model: model.value, reasoning_effort: effort.value }
  const fingerprint = JSON.stringify(payload)
  if (!submission || submission.fingerprint !== fingerprint) submission = { fingerprint, key: crypto.randomUUID() }
  creating.value = true
  error.value = ''
  try {
    const result = await candyTestsAPI.create({ ...payload, idempotency_key: submission.key })
    submission = undefined
    emit('updated')
    if (!isCurrent(version)) return
    batchRequest++
    batch.value = result
    batchId.value = result.id
    page.value = result.page || 1
    selectedItem.value = null
    rememberBatch(result.id)
    schedulePoll()
  } catch {
    if (isCurrent(version)) error.value = t('candyTests.createError')
  } finally {
    creating.value = false
  }
}

async function cancelTests(itemIds?: number[]) {
  if (cancelling.value || !batchId.value) return
  const version = lifecycle
  cancelling.value = true
  error.value = ''
  try {
    await candyTestsAPI.cancel(batchId.value, itemIds)
    emit('updated')
    if (isCurrent(version)) await refreshVisible()
  } catch {
    if (isCurrent(version)) error.value = t('candyTests.cancelError')
  } finally {
    cancelling.value = false
  }
}

async function changePage(next: number) {
  page.value = next
  await loadBatch()
}

async function changeHistoryAccount(accountId: number) {
  historyAccountId.value = accountId
  history.value = []
  await loadHistory()
}
</script>
