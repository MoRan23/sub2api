<template>
  <section class="space-y-3 border-t border-gray-100 pt-3 dark:border-dark-600" data-testid="codex-response-evidence">
    <dl class="grid gap-3 sm:grid-cols-2">
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.upstreamResponseModel`) }}</dt><dd class="break-all font-mono" data-testid="codex-response-model">{{ evidence.upstream_response_model || t(`${prefix}.modelNotReported`) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelRelation`) }}</dt><dd data-testid="codex-response-model-relation">{{ relation }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelEvidenceSource`) }}</dt><dd>{{ t(`${prefix}.modelEvidenceSources.${evidence.model_evidence_source === 'response.model' ? 'response' : evidence.model_evidence_source === 'model' ? 'top_level' : 'unknown'}`) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelConflict`) }}</dt><dd>{{ flag(evidence.model_conflict) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.safetyBufferingEnabled`) }}</dt><dd data-testid="codex-safety-buffering-enabled">{{ flag(evidence.safety_buffering_enabled) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.safetyBufferingFasterModel`) }}</dt><dd class="break-all font-mono" data-testid="codex-safety-buffering-faster-model">{{ evidence.safety_buffering_faster_model || t(`${prefix}.modelNotReported`) }}</dd></div>
      <div v-if="evidence.header_evidence_scope === 'response' || evidence.header_evidence_scope === 'connection'"><dt class="text-xs text-gray-500">{{ t(`${prefix}.headerEvidenceScope`) }}</dt><dd data-testid="codex-header-evidence-scope">{{ t(`${prefix}.headerEvidenceScopes.${evidence.header_evidence_scope}`) }}</dd></div>
    </dl>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.modelEvidenceHint`) }}</p>
    <section v-if="evidence.cookie_diagnostic" class="space-y-2" data-testid="codex-cookie-diagnostic">
      <h5 class="text-xs font-semibold">{{ t(`${prefix}.cookieDiagnostic`) }}</h5>
      <dl class="grid gap-3 sm:grid-cols-2">
        <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cookieSent`) }}</dt><dd>{{ flag(evidence.cookie_diagnostic.sent) }}</dd></div>
        <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cookieSource`) }}</dt><dd>{{ cookieSource }}</dd></div>
        <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.cookieNames`) }}</dt><dd class="break-all font-mono">{{ cookieNames.join(', ') || '—' }}</dd></div>
        <div v-if="cookieExpiries.length"><dt class="text-xs text-gray-500">{{ t(`${prefix}.cookieExpiresAt`) }}</dt><dd v-for="(cookie, index) in cookieExpiries" :key="`${cookie.name}:${index}`" class="break-words"><span class="font-mono">{{ cookie.name }}</span>: {{ cookie.expiry }}</dd></div>
      </dl>
      <p v-if="evidence.cookie_diagnostic.reason" class="text-xs text-gray-500 dark:text-gray-400">{{ cookieReason }}</p>
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexResponseEvidence } from '@/api/admin/accounts'

const props = defineProps<{ evidence: CodexResponseEvidence }>()
const { t } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const relations = new Set(['not_reported', 'exact', 'known_alias', 'different', 'conflicting'])
const cookieSources = new Set(['none', 'persistent', 'memory', 'mixed'])
const cookieReasons = new Set(['cookie_scope_missing', 'cookie_invalid_scope', 'cookie_host_not_allowed', 'cookie_store_unavailable', 'cookie_store_corrupt', 'cookie_stale_scope', 'cookie_empty', 'cookie_sent', 'cookie_updated'])
const relation = computed(() => t(`${prefix}.modelRelations.${props.evidence.model_conflict ? 'conflicting' : relations.has(props.evidence.model_relation || '') ? props.evidence.model_relation : 'not_reported'}`))
const cookieSource = computed(() => t(`${prefix}.cookieSources.${cookieSources.has(props.evidence.cookie_diagnostic?.source || '') ? props.evidence.cookie_diagnostic?.source : 'unknown'}`))
const cookieReason = computed(() => t(`${prefix}.cookieReasons.${cookieReasons.has(props.evidence.cookie_diagnostic?.reason || '') ? props.evidence.cookie_diagnostic?.reason : 'unknown'}`))
const cookieNames = computed(() => [...new Set(props.evidence.cookie_diagnostic?.names || [])])
const cookieExpiries = computed(() => (props.evidence.cookie_diagnostic?.cookies || []).map(cookie => {
  const time = cookie.expires_at ? Date.parse(cookie.expires_at) : Number.NaN
  return { name: cookie.name, expiry: Number.isFinite(time) ? new Date(time).toLocaleString() : t(`${prefix}.${cookie.expires_at ? 'cookieUnknownExpiry' : 'cookieSession'}`) }
}))
function flag(value?: boolean) {
  return t(`${prefix}.evidenceFlags.${typeof value === 'boolean' ? value ? 'yes' : 'no' : 'unknown'}`)
}
</script>
