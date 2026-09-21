<template>
  <section class="flex h-full min-h-[320px] flex-col overflow-auto" :aria-label="t('admin.fingerprintObservation.telemetry.title')">
    <div class="shrink-0 space-y-3 border-b border-gray-200 p-4 dark:border-dark-700">
      <div class="flex flex-wrap items-center gap-2 text-xs">
        <template v-if="response">
          <span class="rounded-full px-2.5 py-1 font-semibold" :class="response.effective_enabled ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'">
            {{ t(response.effective_enabled ? 'admin.fingerprintObservation.telemetry.effectiveOn' : 'admin.fingerprintObservation.telemetry.effectiveOff') }}
          </span>
          <span class="text-gray-500 dark:text-gray-400">{{ t(response.configured_enabled ? 'admin.fingerprintObservation.telemetry.configuredOn' : 'admin.fingerprintObservation.telemetry.configuredOff') }}</span>
        </template>
        <span v-if="mode" class="rounded-full bg-blue-100 px-2.5 py-1 font-medium text-blue-800 dark:bg-blue-900/30 dark:text-blue-300">{{ t(`admin.fingerprintObservation.telemetry.modes.${mode}`) }}</span>
      </div>
      <p class="text-xs leading-relaxed text-gray-500 dark:text-gray-400">{{ t('admin.fingerprintObservation.telemetry.hint') }}</p>
      <p v-if="response?.forced_off_reason" class="break-words text-xs text-amber-700 dark:text-amber-300" role="status">{{ t('admin.fingerprintObservation.telemetry.forcedOff', { reason: response.forced_off_reason }) }}</p>
      <dl v-if="response" class="grid grid-cols-3 gap-2 sm:grid-cols-5 lg:grid-cols-9">
        <div v-for="metric in metrics" :key="metric.key" class="min-w-0 rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/60">
          <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t(`admin.fingerprintObservation.telemetry.counters.${metric.key}`) }}</dt>
          <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ metric.value }}</dd>
        </div>
      </dl>
      <form class="grid grid-cols-2 items-end gap-2 sm:flex sm:flex-wrap" @submit.prevent="applyFilters">
        <label class="col-span-2 min-w-0 flex-1 text-xs text-gray-500 sm:max-w-44 dark:text-gray-400">
          {{ t('admin.fingerprintObservation.telemetry.accountId') }}
          <input v-model="accountFilter" type="number" min="1" step="1" inputmode="numeric" class="input mt-1 w-full" :placeholder="t('admin.fingerprintObservation.telemetry.allAccounts')" />
        </label>
        <label class="min-w-0 flex-1 text-xs text-gray-500 sm:max-w-44 dark:text-gray-400">
          {{ t('common.status') }}
          <select v-model="statusFilter" :aria-label="t('common.status')" class="input mt-1 w-full">
            <option value="">{{ t('admin.fingerprintObservation.telemetry.allStatuses') }}</option>
            <option v-for="status in statuses" :key="status" :value="status">{{ t(`admin.fingerprintObservation.telemetry.status.${status}`) }}</option>
          </select>
        </label>
        <label class="min-w-0 flex-1 text-xs text-gray-500 sm:max-w-44 dark:text-gray-400">
          {{ t('admin.fingerprintObservation.telemetry.type') }}
          <select v-model="typeFilter" :aria-label="t('admin.fingerprintObservation.telemetry.type')" class="input mt-1 w-full">
            <option value="">{{ t('admin.fingerprintObservation.telemetry.allTypes') }}</option>
            <option value="analytics">{{ t('admin.fingerprintObservation.telemetry.types.analytics') }}</option>
            <option value="metrics">{{ t('admin.fingerprintObservation.telemetry.types.metrics') }}</option>
          </select>
        </label>
        <label class="min-w-0 flex-1 text-xs text-gray-500 sm:max-w-36 dark:text-gray-400">
          {{ t('admin.fingerprintObservation.telemetry.os') }}
          <select v-model="osFilter" :aria-label="t('admin.fingerprintObservation.telemetry.os')" class="input mt-1 w-full">
            <option value="">{{ t('admin.fingerprintObservation.telemetry.allSystems') }}</option>
            <option v-for="os in systems" :key="os" :value="os">{{ t(`admin.fingerprintObservation.telemetry.systems.${os}`) }}</option>
          </select>
        </label>
        <label class="min-w-0 flex-1 text-xs text-gray-500 sm:max-w-36 dark:text-gray-400">
          {{ t('admin.fingerprintObservation.telemetry.source') }}
          <select v-model="sourceFilter" :aria-label="t('admin.fingerprintObservation.telemetry.source')" class="input mt-1 w-full">
            <option value="">{{ t('admin.fingerprintObservation.telemetry.allSources') }}</option>
            <option v-for="source in sources" :key="source" :value="source">{{ t(`admin.fingerprintObservation.telemetry.sources.${source}`) }}</option>
          </select>
        </label>
        <button type="submit" class="btn btn-secondary col-span-2" :disabled="loading">{{ t('common.filter') }}</button>
      </form>
    </div>

    <div v-if="error" role="alert" class="flex flex-wrap items-center justify-between gap-2 border-b border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">
      <span>{{ error }}</span>
      <button type="button" class="font-medium underline" :disabled="loading" @click="refresh">{{ t('common.retry') }}</button>
    </div>

    <div class="min-h-48 flex-1 overflow-auto" :aria-busy="loading">
      <div v-if="loading && !items.length" class="flex min-h-48 items-center justify-center gap-2 text-sm text-gray-500 dark:text-gray-400">
        <Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}
      </div>
      <ul v-else-if="items.length" class="divide-y divide-gray-200 dark:divide-dark-700">
        <li v-for="entry in items" :key="entry.id" class="min-w-0 px-4 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs">
              <span class="rounded-full bg-indigo-100 px-2 py-0.5 font-semibold text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300">{{ t(`admin.fingerprintObservation.telemetry.types.${entry.type}`) }}</span>
              <span class="font-semibold" :class="statusClass(entry.status)">{{ t(`admin.fingerprintObservation.telemetry.status.${entry.status}`) }}</span>
              <span v-if="entry.http_status" class="font-mono text-gray-500 dark:text-gray-400">HTTP {{ entry.http_status }}</span>
              <span v-if="entry.os_family" class="text-gray-600 dark:text-gray-300">{{ t(`admin.fingerprintObservation.telemetry.systems.${entry.os_family}`) }}</span>
              <span v-if="entry.source" :class="entry.source === 'observed' ? 'text-blue-700 dark:text-blue-300' : 'text-amber-700 dark:text-amber-300'">{{ t(`admin.fingerprintObservation.telemetry.sources.${entry.source}`) }}</span>
              <span v-else-if="entry.contains_simulated" class="text-amber-700 dark:text-amber-300">{{ t('admin.fingerprintObservation.telemetry.simulated') }}</span>
            </div>
            <time class="text-xs text-gray-400" :datetime="entry.created_at">{{ formatTime(entry.created_at) }}</time>
          </div>
          <div class="mt-2 flex min-w-0 flex-wrap gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-300">
            <span class="break-all">{{ entry.account_name || `#${entry.account_id}` }} <span v-if="entry.account_name" class="text-gray-400">#{{ entry.account_id }}</span></span>
            <span v-if="entry.model" class="break-all">{{ entry.model }}</span>
            <span v-if="entry.type === 'metrics'">{{ t(entry.turn_count > 1 ? 'admin.fingerprintObservation.telemetry.multipleTurns' : 'admin.fingerprintObservation.telemetry.metricBatch', { count: entry.turn_count }) }}</span>
            <span>{{ t('admin.fingerprintObservation.telemetry.eventCount', { count: entry.event_names.length }) }}</span>
          </div>
          <p v-if="entry.error" class="mt-2 break-words text-xs text-amber-700 dark:text-amber-300">{{ reasonLabel(entry.error) }}</p>
          <details class="mt-3 rounded-lg border border-gray-200 dark:border-dark-700" @toggle="toggleDetails(entry.id, $event)">
            <summary class="cursor-pointer px-3 py-2 text-xs font-medium text-gray-700 dark:text-gray-300">{{ t('admin.fingerprintObservation.telemetry.details') }}</summary>
            <div v-if="openedDetails.has(entry.id)" class="space-y-3 border-t border-gray-200 p-3 dark:border-dark-700">
              <p v-if="entry.type === 'metrics'" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.fingerprintObservation.telemetry.aggregateIdentityHint') }}</p>
              <dl class="grid min-w-0 gap-3 text-xs sm:grid-cols-2 lg:grid-cols-3">
                <div v-for="field in detailFields(entry)" :key="field.key" class="min-w-0">
                  <dt class="font-medium text-gray-400">{{ t(`admin.fingerprintObservation.telemetry.fields.${field.key}`) }}</dt>
                  <dd class="mt-1 break-all font-mono text-gray-700 dark:text-gray-300">{{ field.value || '—' }}</dd>
                </div>
              </dl>
              <div v-if="entry.reasons?.length">
                <p class="text-xs font-medium text-gray-400">{{ t('admin.fingerprintObservation.telemetry.reasons') }}</p>
                <ul class="mt-1 space-y-1 text-xs text-gray-700 dark:text-gray-300"><li v-for="reason in entry.reasons" :key="reason">{{ reasonLabel(reason) }}</li></ul>
              </div>
              <div v-if="entry.field_sources && Object.keys(entry.field_sources).length">
                <p class="text-xs font-medium text-gray-400">{{ t('admin.fingerprintObservation.telemetry.fieldSources') }}</p>
                <dl class="mt-1 grid gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
                  <div v-for="(source, name) in entry.field_sources" :key="name" class="flex min-w-0 justify-between gap-3">
                    <dt class="break-all font-mono text-gray-600 dark:text-gray-300">{{ name }}</dt>
                    <dd class="shrink-0 text-gray-500 dark:text-gray-400">{{ t(`admin.fingerprintObservation.telemetry.sources.${source}`) }}</dd>
                  </div>
                </dl>
              </div>
              <div>
                <p class="text-xs font-medium text-gray-400">{{ t('admin.fingerprintObservation.telemetry.eventNames') }}</p>
                <ul class="mt-1 space-y-1 break-all font-mono text-xs text-gray-700 dark:text-gray-300"><li v-for="name in entry.event_names" :key="name">{{ name }}</li></ul>
              </div>
            </div>
          </details>
        </li>
      </ul>
      <div v-else-if="!error" class="flex min-h-48 items-center justify-center p-4 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.fingerprintObservation.telemetry.empty') }}</div>
    </div>

    <div v-if="response && response.total > 0" class="shrink-0 border-t border-gray-200 dark:border-dark-700">
      <Pagination :total="response.total" :page="page" :page-size="pageSize" :page-size-options="[20, 50, 100]" @update:page="changePage" @update:page-size="changePageSize" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CodexTelemetryEntry, CodexTelemetryListParams, CodexTelemetryObservationsResponse, CodexTelemetryStatus, CodexTelemetryType, CodexTelemetryOS, CodexTelemetrySource } from '@/api/admin/codexTelemetry'
import { extractApiErrorMessage } from '@/utils/apiError'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

const emit = defineEmits<{ stateChanged: [state: { loading: boolean; paused: boolean }] }>()
const { t, te } = useI18n()
const response = ref<CodexTelemetryObservationsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const page = ref(1)
const pageSize = ref(20)
const accountFilter = ref<string | number>('')
const statusFilter = ref<CodexTelemetryStatus | ''>('')
const typeFilter = ref<CodexTelemetryType | ''>('')
const osFilter = ref<CodexTelemetryOS | ''>('')
const sourceFilter = ref<CodexTelemetrySource | ''>('')
const systems: CodexTelemetryOS[] = ['windows', 'macos', 'linux']
const sources: CodexTelemetrySource[] = ['observed', 'simulated', 'mixed']
const appliedFilters = ref<CodexTelemetryListParams>({})
const openedDetails = ref(new Set<number>())
const statuses: CodexTelemetryStatus[] = ['queued', 'sent', 'failed', 'dropped', 'cancelled', 'skipped', 'unknown']
const mode = computed(() => {
  const data = response.value
  if (typeof data?.simulation_enabled !== 'boolean' || typeof data.observation_enabled !== 'boolean') return ''
  if (data.simulation_enabled && data.observation_enabled) return 'mixed'
  return data.simulation_enabled ? 'simulated' : data.observation_enabled ? 'observed' : 'off'
})
const items = computed(() => Array.isArray(response.value?.items) ? response.value.items : [])
const metrics = computed(() => [
  { key: 'queueDepth', value: response.value?.queue_depth ?? 0 },
  ...(['attempts', ...statuses] as const).map((key) => ({ key, value: response.value?.counters?.[key] ?? 0 })),
])
let controller: AbortController | null = null
let disposed = false

watch([loading, page, openedDetails], () => emit('stateChanged', {
  loading: loading.value,
  paused: page.value !== 1 || openedDetails.value.size > 0,
}), { immediate: true, deep: true })

async function load(targetPage: number): Promise<void> {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = ''
  try {
    const result = await adminAPI.codexTelemetry.list({ ...appliedFilters.value, page: targetPage, page_size: pageSize.value }, { signal: request.signal })
    if (disposed || request.signal.aborted || controller !== request) return
    // An evicted in-memory page is corrected once instead of leaving an empty last page.
    const lastPage = Math.max(1, Math.ceil(result.total / result.page_size))
    if (targetPage > lastPage) {
      await load(lastPage)
      return
    }
    response.value = result
    page.value = result.page
    pageSize.value = result.page_size
    openedDetails.value = new Set()
  } catch (cause: unknown) {
    if (disposed || request.signal.aborted || controller !== request) return
    error.value = extractApiErrorMessage(cause, t('admin.fingerprintObservation.telemetry.loadFailed'))
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

function refresh(): Promise<void> { return load(page.value) }
function applyFilters(): void {
  const accountID = Number(accountFilter.value)
  if (accountFilter.value !== '' && (!Number.isSafeInteger(accountID) || accountID < 1)) {
    error.value = t('admin.fingerprintObservation.telemetry.invalidAccount')
    return
  }
  appliedFilters.value = {
    ...(accountFilter.value !== '' ? { account_id: accountID } : {}),
    ...(statusFilter.value ? { status: statusFilter.value } : {}),
    ...(typeFilter.value ? { type: typeFilter.value } : {}),
    ...(osFilter.value ? { os_family: osFilter.value } : {}),
    ...(sourceFilter.value ? { source: sourceFilter.value } : {}),
  }
  void load(1)
}
function changePage(nextPage: number): void {
  if (nextPage < 1 || nextPage === page.value || nextPage > Math.ceil((response.value?.total ?? 0) / pageSize.value)) return
  void load(nextPage)
}
function changePageSize(size: number): void {
  if (![20, 50, 100].includes(size)) return
  pageSize.value = size
  void load(1)
}
function toggleDetails(id: number, event: Event): void {
  const next = new Set(openedDetails.value)
  if ((event.target as HTMLDetailsElement).open) next.add(id)
  else next.delete(id)
  openedDetails.value = next
}
function statusClass(status: CodexTelemetryStatus): string {
  if (status === 'sent') return 'text-green-700 dark:text-green-300'
  if (status === 'failed' || status === 'dropped') return 'text-red-700 dark:text-red-300'
  if (status === 'unknown') return 'text-amber-700 dark:text-amber-300'
  return 'text-gray-600 dark:text-gray-300'
}
function detailFields(entry: CodexTelemetryEntry): Array<{ key: string; value: string | number }> {
  const keys = entry.type === 'metrics'
    ? ['attempt_count', 'user_agent', 'originator', 'version', 'updated_at'] as const
    : ['session_id', 'thread_id', 'turn_id', 'parent_thread_id', 'parent_turn_id', 'root_turn_id', 'attempt_id', 'attempt_count', 'user_agent', 'originator', 'version', 'updated_at'] as const
  const fields: Array<{ key: string; value: string | number }> = keys.map((key) => ({ key, value: key === 'updated_at' ? formatTime(entry[key]) : entry[key] }))
  if (entry.pool_id) fields.unshift({ key: 'pool_id', value: entry.pool_id })
  if (entry.batch_id) fields.unshift({ key: 'batch_id', value: entry.batch_id })
  if (entry.type === 'analytics' && entry.event_names.includes('codex_thread_initialized')) {
    fields.push({
      key: 'is_worktree',
      value: entry.is_worktree === undefined
        ? t('admin.fingerprintObservation.telemetry.worktreeNotCollected')
        : entry.is_worktree === null
          ? t('admin.fingerprintObservation.telemetry.worktreeUnknown')
          : String(entry.is_worktree),
    })
  }
  return fields
}
function reasonLabel(reason: string): string {
  const key = `admin.fingerprintObservation.telemetry.reasonLabels.${reason}`
  return te(key) ? t(key) : reason
}
function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
defineExpose({ refresh })
onMounted(() => { void load(1) })
onBeforeUnmount(() => { disposed = true; controller?.abort() })
</script>
