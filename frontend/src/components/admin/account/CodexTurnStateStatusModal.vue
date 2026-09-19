<template>
  <BaseDialog :show="show" :title="t(`${prefix}.statusTitle`)" @close="$emit('close')">
    <p class="mb-3 break-all font-medium text-gray-800 dark:text-gray-200">{{ account?.name }}</p>
    <p class="mb-4 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.experimental`) }}</p>
    <p v-if="loading" role="status">{{ t(`${prefix}.loading`) }}</p>
    <p v-else-if="failed" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ t(`${prefix}.loadFailed`) }}</p>
    <div v-else-if="status" class="space-y-4 text-sm" data-testid="codex-turn-state-status">
      <p v-if="status.inherited" class="text-blue-600 dark:text-blue-400">{{ t(`${prefix}.inherited`, { id: status.owner_account_id }) }}</p>
      <p v-if="!status.enabled">{{ t(`${prefix}.disabled`) }}</p>
      <p v-else-if="!status.expected_length" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.unresolved`) }}</p>
      <p v-else>{{ t(`${prefix}.expectedLength`) }}: {{ t(`${prefix}.characters`, { count: status.expected_length }) }}</p>
      <p v-if="status.reason" class="break-words text-gray-500 dark:text-gray-400">{{ t(`${prefix}.reason`) }}: {{ label('reasons', status.reason) }}</p>
      <section v-if="status.enabled" class="space-y-3" data-testid="codex-turn-state-cache-section">
      <h3 class="font-semibold text-gray-900 dark:text-gray-100">{{ t(`${prefix}.cacheSection`) }}</h3>
      <p v-if="!status.collector_proxy_id" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.noCollectorProxy`) }}</p>
      <p v-if="!status.models?.length" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.cacheEmpty`) }}</p>
      <section v-for="model in status.models" :key="model.model" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
        <h3 class="break-all font-mono font-semibold text-gray-900 dark:text-gray-100">{{ model.model }}</h3>
        <dl class="grid gap-3 sm:grid-cols-2">
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.status`) }}</dt><dd>{{ label('states', model.state) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cacheShape`) }}</dt><dd>{{ label('shapes', model.shape || 'unknown') }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.length`) }}</dt><dd>{{ model.token_length || '—' }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.source`) }}</dt><dd>{{ label('sources', model.source) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.expiresAt`) }}</dt><dd>{{ date(model.expires_at) }}</dd><dd v-if="model.remaining_seconds > 0" class="text-xs text-gray-500">{{ t(`${prefix}.remaining`, { seconds: model.remaining_seconds }) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.lastBusiness`) }}</dt><dd>{{ date(model.last_business_at) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.lastCollected`) }}</dt><dd>{{ date(model.last_collected_at) }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.nextCollect`) }}</dt><dd>{{ date(model.next_collect_at) }}</dd></div>
        </dl>
        <p v-if="model.collector_paused" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.states.paused`) }}</p>
        <p v-if="model.refresh_reason" class="break-words text-xs text-gray-500">{{ t(`${prefix}.refreshReason`) }}: {{ label('reasons', model.refresh_reason) }}</p>
        <p v-if="model.last_error" class="break-words text-xs text-red-600 dark:text-red-400">{{ t(`${prefix}.lastError`) }}: {{ label('reasons', model.last_error) }}</p>
      </section>
      </section>
      <section v-if="typeof status.observation_enabled === 'boolean'" class="space-y-3" data-testid="codex-turn-state-observations-section">
        <h3 class="font-semibold text-gray-900 dark:text-gray-100">{{ t(`${prefix}.observationsTitle`) }}</h3>
        <p v-if="!status.observation_enabled" class="text-gray-500 dark:text-gray-400" data-testid="codex-turn-state-observation-disabled">{{ t(`${prefix}.observationDisabled`) }}</p>
        <template v-else>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.observationScopeHint`) }}</p>
          <p v-if="!status.observations?.length" class="text-gray-500 dark:text-gray-400" data-testid="codex-turn-state-observation-empty">{{ t(`${prefix}.observationEmpty`) }}</p>
          <section v-for="observation in status.observations" :key="observation.model" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`codex-turn-state-observation-${observation.model}`">
            <h4 class="break-all font-mono font-semibold text-gray-900 dark:text-gray-100">{{ observation.model }}</h4>
            <dl class="grid gap-3 sm:grid-cols-2">
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.observedAt`) }}</dt><dd>{{ date(observation.observed_at) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseLength`) }}</dt><dd>{{ observation.response_length > 0 ? t(`${prefix}.characters`, { count: observation.response_length }) : t(`${prefix}.responseStateMissing`) }}</dd></div>
              <div v-if="observation.response_observed_shape"><dt class="text-xs text-gray-500">{{ t(`${prefix}.observedShape`) }}</dt><dd>{{ label('observedShapes', observation.response_observed_shape) }}</dd></div>
              <div v-if="typeof observation.response_cipher_blocks === 'number'"><dt class="text-xs text-gray-500">{{ t(`${prefix}.cipherBlocks`) }}</dt><dd>{{ observation.response_cipher_blocks }}</dd></div>
              <div v-if="observation.response_length > 0"><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseEligibility`) }}</dt><dd>{{ label('shapes', observation.response_shape) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.responseSource`) }}</dt><dd>{{ label('sources', observation.response_source ? `response_${observation.response_source}` : undefined) }}</dd></div>
              <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.outboundObservedLength`) }}</dt><dd>{{ observation.outbound_length }}</dd></div>
              <div v-if="observation.response_validation_reason"><dt class="text-xs text-gray-500">{{ t(`${prefix}.validationReason`) }}</dt><dd>{{ label('validationReasons', observation.response_validation_reason) }}</dd></div>
            </dl>
          </section>
          <p v-if="status.observations?.length" class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.observedShapeHint`) }}</p>
        </template>
      </section>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button>
      <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCodexTurnState, type CodexTurnStateStatus } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; account: { id: number; name: string } | null }>()
defineEmits<{ close: [] }>()
const { t, te } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const loading = ref(false)
const failed = ref(false)
const status = ref<CodexTurnStateStatus | null>(null)
let controller: AbortController | null = null

function label(group: string, value?: string) {
  if (!value) return '—'
  const key = `${prefix}.${group}.${value}`
  return te(key) ? t(key) : value
}
function date(value?: string) {
  return value ? new Date(value).toLocaleString() : '—'
}
async function refresh() {
  controller?.abort()
  status.value = null
  failed.value = false
  loading.value = false
  if (!props.show || !props.account) return
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const result = await getCodexTurnState(props.account.id, current.signal)
    if (!current.signal.aborted) status.value = result
  } catch {
    if (!current.signal.aborted) failed.value = true
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
watch(() => [props.show, props.account?.id], refresh, { immediate: true })
onUnmounted(() => controller?.abort())
</script>
