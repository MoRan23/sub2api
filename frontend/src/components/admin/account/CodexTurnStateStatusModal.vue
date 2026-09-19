<template>
  <BaseDialog :show="show" :title="t(`${prefix}.statusTitle`)" width="extra-wide" @close="close">
    <p class="mb-3 break-all font-medium text-gray-800 dark:text-gray-200">{{ account?.name }}</p>
    <p class="mb-2 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.experimental`) }}</p>
    <p class="mb-4 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.autoRefreshHint`) }}</p>
    <p v-if="loading && !status" role="status">{{ t(`${prefix}.loading`) }}</p>
    <p v-if="failed" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ t(`${prefix}.${status ? 'refreshFailed' : 'loadFailed'}`) }}</p>
    <div v-if="status" class="space-y-4 text-sm" data-testid="codex-turn-state-status">
      <p v-if="status.inherited" class="text-blue-600 dark:text-blue-400">{{ t(`${prefix}.inherited`, { id: status.owner_account_id }) }}</p>
      <div class="grid grid-cols-1 items-start gap-6 lg:grid-cols-2" data-testid="codex-turn-state-status-columns">
      <section class="min-w-0 space-y-3" data-testid="codex-turn-state-cache-section">
      <h3 class="font-semibold text-gray-900 dark:text-gray-100">{{ t(`${prefix}.cacheSection`) }}</h3>
      <p v-if="!status.enabled">{{ t(`${prefix}.disabled`) }}</p>
      <p v-else-if="!status.expected_length" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.unresolved`) }}</p>
      <p v-else>{{ t(`${prefix}.expectedLength`) }}: {{ t(`${prefix}.characters`, { count: status.expected_length }) }}</p>
      <p v-if="status.reason" class="break-words text-gray-500 dark:text-gray-400">{{ t(`${prefix}.reason`) }}: {{ label('reasons', status.reason) }}</p>
      <template v-if="status.enabled">
      <p v-if="!status.collector_proxy_id" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.noCollectorProxy`) }}</p>
      <p v-if="!status.models?.length" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.cacheEmpty`) }}</p>
      <section v-for="model in status.models" :key="model.model" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`codex-turn-state-cache-${model.model}`">
        <h3 class="break-all font-mono font-semibold text-gray-900 dark:text-gray-100">{{ model.model }}</h3>
        <dl class="grid gap-3 sm:grid-cols-2">
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cacheAvailability`) }}</dt><dd :data-testid="`codex-turn-state-cache-availability-${model.model}`">{{ t(`${prefix}.${cacheAvailable(model) ? 'cacheAvailable' : 'cacheUnavailable'}`) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.status`) }}</dt><dd>{{ label('states', model.state) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cacheShape`) }}</dt><dd>{{ label('shapes', model.shape || 'unknown') }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.length`) }}</dt><dd>{{ model.token_length || '—' }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.source`) }}</dt><dd>{{ label('sources', model.source) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.expiresAt`) }}</dt><dd>{{ date(model.expires_at) }}</dd><dd v-if="remaining(model) > 0" class="text-xs text-gray-500">{{ t(`${prefix}.remaining`, { seconds: remaining(model) }) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.collectionStatus`) }}</dt><dd :data-testid="`codex-turn-state-collection-${model.model}`">{{ label('collectionStatuses', model.collection_status || (model.collector_paused ? 'paused' : undefined)) }}</dd></div>
          <div v-if="model.collection_reason"><dt class="text-xs text-gray-500">{{ t(`${prefix}.collectionReason`) }}</dt><dd class="break-words">{{ label('reasons', model.collection_reason) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.lastBusiness`) }}</dt><dd>{{ date(model.last_business_at) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.lastCollected`) }}</dt><dd>{{ date(model.last_collected_at) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.nextCollect`) }}</dt><dd>{{ date(model.next_collect_at) }}</dd></div>
        </dl>
        <p v-if="model.refresh_reason" class="break-words text-xs text-gray-500">{{ t(`${prefix}.refreshReason`) }}: {{ label('reasons', model.refresh_reason) }}</p>
        <p v-if="model.last_error" class="break-words text-xs text-red-600 dark:text-red-400">{{ t(`${prefix}.lastError`) }}: {{ label('reasons', model.last_error) }}</p>
      </section>
      </template>
      </section>
      <section class="min-w-0 space-y-3" data-testid="codex-turn-state-observations-section">
        <h3 class="font-semibold text-gray-900 dark:text-gray-100">{{ t(`${prefix}.observationsTitle`) }}</h3>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.observationScopeHint`) }}</p>
          <p v-if="!status.observations?.length" class="text-gray-500 dark:text-gray-400" data-testid="codex-turn-state-observation-empty">{{ t(`${prefix}.observationEmpty`) }}</p>
          <section v-for="observation in status.observations" :key="observation.model" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`codex-turn-state-observation-${observation.model}`">
            <h4 class="break-all font-mono font-semibold text-gray-900 dark:text-gray-100">{{ observation.model }}</h4>
            <dl class="grid gap-3 sm:grid-cols-2">
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.observedAt`) }}</dt><dd>{{ date(observation.observed_at) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.requestSource`) }}</dt><dd>{{ label('sources', observation.request_source) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseLength`) }}</dt><dd>{{ observation.response_length > 0 ? t(`${prefix}.characters`, { count: observation.response_length }) : t(`${prefix}.responseStateMissing`) }}</dd></div>
              <div v-if="observation.response_observed_shape"><dt class="text-xs text-gray-500">{{ t(`${prefix}.observedShape`) }}</dt><dd>{{ label('observedShapes', observation.response_observed_shape) }}</dd></div>
              <div v-if="typeof observation.response_cipher_blocks === 'number'"><dt class="text-xs text-gray-500">{{ t(`${prefix}.cipherBlocks`) }}</dt><dd>{{ observation.response_cipher_blocks }}</dd></div>
              <div v-if="observation.response_length > 0"><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseEligibility`) }}</dt><dd>{{ label('shapes', observation.response_shape) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseCarrier`) }}</dt><dd>{{ label('sources', observation.response_source ? `response_${observation.response_source}` : undefined) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.outboundObservedLength`) }}</dt><dd>{{ observation.outbound_length }}</dd></div>
              <div v-if="observation.response_validation_reason"><dt class="text-xs text-gray-500">{{ t(`${prefix}.validationReason`) }}</dt><dd>{{ label('validationReasons', observation.response_validation_reason) }}</dd></div>
            </dl>
          </section>
          <p v-if="status.observations?.length" class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.observedShapeHint`) }}</p>
      </section>
      </div>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="loading" data-testid="codex-turn-state-refresh" @click="refresh">{{ t('common.refresh') }}</button>
      <button type="button" class="btn btn-secondary" @click="close">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCodexTurnState, type CodexTurnStateModelStatus, type CodexTurnStateStatus } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; account: { id: number; name: string } | null }>()
const emit = defineEmits<{ close: [] }>()
const { t, te } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const loading = ref(false)
const failed = ref(false)
const status = ref<CodexTurnStateStatus | null>(null)
const now = ref(Date.now())
let observedAt = now.value
let controller: AbortController | null = null
let timer: ReturnType<typeof setInterval> | null = null

function label(group: string, value?: string) {
  if (!value) return '—'
  const key = `${prefix}.${group}.${value}`
  return te(key) ? t(key) : value
}
function date(value?: string) {
  const parsed = value ? new Date(value) : null
  return parsed && Number.isFinite(parsed.getTime()) ? parsed.toLocaleString() : '—'
}
function remaining(model: CodexTurnStateModelStatus) {
  const expires = model.expires_at ? Date.parse(model.expires_at) : Number.NaN
  if (Number.isFinite(expires)) return Math.max(0, Math.ceil((expires - now.value) / 1000))
  return Math.max(0, model.remaining_seconds - Math.max(0, Math.floor((now.value - observedAt) / 1000)))
}
function cacheAvailable(model: CodexTurnStateModelStatus) {
  const available = model.cache_available ?? (model.state === 'ready' || model.state === 'paused')
  return status.value?.enabled && available && model.model_allowed !== false && model.shape === 'target' && remaining(model) > 0 &&
    model.token_length === status.value.expected_length && [292, 332].includes(model.token_length) &&
    model.cipher_blocks === (model.token_length === 292 ? 10 : 12)
}
function stop() {
  if (timer !== null) clearInterval(timer)
  timer = null
  controller?.abort()
  controller = null
  loading.value = false
}
function close() {
  stop()
  emit('close')
}
async function refresh() {
  if (!props.show || !props.account || controller) return
  const accountId = props.account.id
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const result = await getCodexTurnState(accountId, current.signal)
    if (controller === current && !current.signal.aborted && props.show && props.account?.id === accountId) {
      status.value = result
      now.value = observedAt = Date.now()
      failed.value = false
    }
  } catch {
    if (controller === current && !current.signal.aborted) failed.value = true
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
}
watch(() => [props.show, props.account?.id], () => {
  stop()
  status.value = null
  failed.value = false
  if (!props.show || !props.account) return
  void refresh()
  timer = setInterval(() => {
    now.value = Date.now()
    void refresh()
  }, 5000)
}, { immediate: true })
onUnmounted(stop)
</script>
