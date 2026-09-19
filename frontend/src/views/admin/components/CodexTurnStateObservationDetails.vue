<template>
  <section class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="codex-turn-state-observation">
    <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.outboundTitle`) }}</h3>
    <dl class="mt-3 grid gap-x-4 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
      <div v-for="item in items" :key="item.label" class="min-w-0">
        <dt class="text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
        <dd class="mt-1 break-words text-gray-800 dark:text-gray-200">{{ item.value }}</dd>
      </div>
    </dl>
    <p v-if="state.response_observed_shape" class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.observedShapeHint`) }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FingerprintObservationEntry } from '@/api/admin/fingerprintObservations'
const props = defineProps<{ state: NonNullable<FingerprintObservationEntry['codex_turn_state']> }>()
const { t, te } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
function label(group: string, value?: string) {
  const key = `${prefix}.${group}.${value}`
  return value ? (te(key) ? t(key) : value) : '—'
}
const items = computed(() => [
  { label: t(`${prefix}.model`), value: props.state.model },
  ...(typeof props.state.account_enabled === 'boolean' ? [
    { label: t(`${prefix}.accountCache`), value: t(`${prefix}.${props.state.account_enabled ? 'cacheEnabled' : 'cacheDisabled'}`) }
  ] : []),
  { label: t(`${prefix}.cacheMode`), value: t(`${prefix}.${props.state.enabled ? 'cacheEnabled' : props.state.account_enabled ? 'maintenancePaused' : 'observationOnly'}`) },
  ...(props.state.maintenance_reason ? [
    { label: t(`${prefix}.maintenanceReason`), value: label('reasons', props.state.maintenance_reason) }
  ] : []),
  { label: t(`${prefix}.action`), value: label('actions', props.state.action) },
  { label: t(`${prefix}.source`), value: label('sources', props.state.source) },
  { label: t(`${prefix}.length`), value: props.state.outbound_length },
  ...(props.state.outbound_carrier ? [
    { label: t(`${prefix}.carrier`), value: label('carriers', props.state.outbound_carrier) },
    { label: t(`${prefix}.headerLength`), value: props.state.outbound_header_length ?? 0 },
    { label: t(`${prefix}.bodyLength`), value: props.state.outbound_body_length ?? 0 }
  ] : []),
  { label: t(`${prefix}.${props.state.response_observed_shape ? 'responseEligibility' : 'shape'}`), value: `${label('shapes', props.state.response_shape)}${props.state.response_length ? ` (${props.state.response_length})` : ''}` },
  ...(props.state.response_observed_shape ? [
    { label: t(`${prefix}.observedShape`), value: label('observedShapes', props.state.response_observed_shape) }
  ] : []),
  ...(typeof props.state.response_cipher_blocks === 'number' ? [
    { label: t(`${prefix}.cipherBlocks`), value: props.state.response_cipher_blocks }
  ] : []),
  ...(props.state.response_validation_reason ? [
    { label: t(`${prefix}.validationReason`), value: label('validationReasons', props.state.response_validation_reason) }
  ] : []),
  { label: t(`${prefix}.responseSource`), value: label('sources', props.state.response_source ? `response_${props.state.response_source}` : undefined) },
  { label: t(`${prefix}.expiresAt`), value: props.state.expires_at ? new Date(props.state.expires_at).toLocaleString() : '—' },
  { label: t(`${prefix}.refreshReason`), value: label('reasons', props.state.renewal_reason) }
])
</script>
