<template>
  <section :aria-label="t(`${prefix}.title`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="guardian-request-details">
    <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.title`) }}</h3>
    <p v-if="!observation" class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.notRecorded`) }}</p>
    <template v-else>
      <p class="mt-1 text-gray-500 dark:text-gray-400">{{ sourceLabel }}</p>
      <dl class="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div>
          <dt class="text-gray-500 dark:text-gray-400">x-codex-guardian</dt>
          <dd class="mt-1 text-gray-800 dark:text-gray-200" data-testid="guardian-request-kind">{{ markerLabel }}</dd>
        </div>
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.contract`) }}</dt>
          <dd class="mt-1 text-gray-800 dark:text-gray-200">{{ t(`${prefix}.${observation.applied ? 'applied' : 'notApplied'}`) }}</dd>
          <dd class="mt-1 text-gray-500 dark:text-gray-400" data-testid="guardian-request-reason">{{ reasonLabel }}</dd>
        </div>
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.serviceTier`) }}</dt>
          <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200" data-testid="guardian-service-tier">{{ serviceTier }}</dd>
        </div>
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.routingHint`) }}</dt>
          <dd class="mt-1 text-gray-800 dark:text-gray-200">{{ t(`${prefix}.${observation.routing_hint_present ? 'present' : 'notSent'}`) }}</dd>
        </div>
        <div v-for="field in fields" :key="field.key" class="min-w-0" :data-testid="`guardian-${field.key}`">
          <dt class="break-all text-gray-500 dark:text-gray-400">{{ field.label }}</dt>
          <dd class="mt-1 text-gray-800 dark:text-gray-200">{{ field.status }}</dd>
          <dd v-if="field.type" class="mt-1 text-gray-500 dark:text-gray-400">{{ field.type }}</dd>
          <dd v-if="field.value !== undefined" class="mt-1 font-mono text-gray-800 dark:text-gray-200">{{ field.value }}</dd>
        </div>
      </dl>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexGuardianRequestObservation } from '@/api/admin/fingerprintObservations'

const props = defineProps<{ observation?: CodexGuardianRequestObservation }>()
const { t, te } = useI18n()
const prefix = 'admin.fingerprintObservation.request.guardianRequest'
const metadataPrefix = 'admin.fingerprintObservation.request.outboundMetadata'
const markerLabel = computed(() => {
  const entry = props.observation
  if (!entry) return t(`${prefix}.notRecorded`)
  if (entry.status === 'absent') return t(`${prefix}.absent`)
  if (entry.status === 'valid' && (entry.kind === 'reviewer' || entry.kind === 'classifier')) return entry.kind
  return t(`${prefix}.invalid`)
})
const sourceLabel = computed(() => {
  const source = props.observation?.source
  return source === 'http_request' || source === 'ws_handshake'
    ? t(`${prefix}.sources.${source}`) : t(`${prefix}.notRecorded`)
})
const reasonLabel = computed(() => {
  const key = `${prefix}.reasons.${props.observation?.reason}`
  return te(key) ? t(key) : t(`${prefix}.notRecorded`)
})
const serviceTier = computed(() => {
  const tier = props.observation?.service_tier
  return typeof tier === 'string' && tier ? tier.slice(0, 64) : t(`${prefix}.notSent`)
})
const fields = computed(() => {
  const entry = props.observation
  if (!entry) return []
  return [
    { key: 'credits', label: 'client_metadata.guardian_credits_requested', evidence: entry.credits },
    { key: 'parent-response', label: 'client_metadata.parent_response_id', evidence: entry.parent_response },
  ].map(({ key, label, evidence }) => {
    const status = !evidence ? 'notRecorded'
      : evidence.status === 'missing' && !evidence.present ? 'absent'
      : evidence.status === 'valid' && evidence.present ? 'valid' : 'invalid'
    const typeKey = `${metadataPrefix}.jsonTypes.${evidence?.type}`
    // Render a fixed field set. Parent IDs and malformed values never enter the UI.
    const value = key === 'credits' && status === 'valid'
      && (entry.credits.value === 'true' || entry.credits.value === 'false')
      ? JSON.stringify(entry.credits.value) : undefined
    return { key, label, status: t(`${prefix}.${status}`), value,
      type: evidence?.present && te(typeKey) ? t(typeKey) : undefined }
  })
})
</script>
