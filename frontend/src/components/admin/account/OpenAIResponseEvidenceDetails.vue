<template>
  <section class="space-y-3 border-t border-gray-100 pt-3 dark:border-dark-600" data-testid="openai-response-evidence">
    <dl class="grid gap-3 sm:grid-cols-2">
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.upstreamResponseModel`) }}</dt><dd class="break-all font-mono" data-testid="openai-response-model">{{ evidence.upstream_response_model || t(`${prefix}.modelNotReported`) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelRelation`) }}</dt><dd data-testid="openai-response-model-relation">{{ relation }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelEvidenceSource`) }}</dt><dd>{{ t(`${prefix}.modelEvidenceSources.${evidence.model_evidence_source === 'response.model' ? 'response' : evidence.model_evidence_source === 'model' ? 'top_level' : 'unknown'}`) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.modelConflict`) }}</dt><dd>{{ flag(evidence.model_conflict) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.safetyBufferingEnabled`) }}</dt><dd data-testid="openai-safety-buffering-enabled">{{ flag(evidence.safety_buffering_enabled) }}</dd></div>
      <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.safetyBufferingFasterModel`) }}</dt><dd class="break-all font-mono" data-testid="openai-safety-buffering-faster-model">{{ evidence.safety_buffering_faster_model || t(`${prefix}.modelNotReported`) }}</dd></div>
      <div v-if="evidence.header_evidence_scope === 'response' || evidence.header_evidence_scope === 'connection'"><dt class="text-xs text-gray-500">{{ t(`${prefix}.headerEvidenceScope`) }}</dt><dd data-testid="openai-header-evidence-scope">{{ t(`${prefix}.headerEvidenceScopes.${evidence.header_evidence_scope}`) }}</dd></div>
    </dl>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.modelEvidenceHint`) }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAIResponseEvidence } from '@/api/admin/accounts'

const props = defineProps<{ evidence: OpenAIResponseEvidence }>()
const { t } = useI18n()
const prefix = 'admin.accounts.responseEvidence'
const relations = new Set(['not_reported', 'exact', 'known_alias', 'different', 'conflicting'])
const relation = computed(() => t(`${prefix}.modelRelations.${props.evidence.model_conflict ? 'conflicting' : relations.has(props.evidence.model_relation || '') ? props.evidence.model_relation : 'not_reported'}`))
function flag(value?: boolean) {
  return t(`${prefix}.evidenceFlags.${typeof value === 'boolean' ? value ? 'yes' : 'no' : 'unknown'}`)
}
</script>
