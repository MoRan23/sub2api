<template>
  <dl class="grid gap-3 sm:grid-cols-2" data-testid="openai-header-diagnostics">
    <div v-for="field in fields" :key="field" class="min-w-0">
      <dt class="text-xs text-gray-500">{{ t(`${prefix}.headerDiagnostics.${field}`) }}</dt>
      <dd class="break-all font-mono" :data-testid="`openai-diagnostic-${field}`">{{ value(field) }}</dd>
    </div>
  </dl>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OpenAIHeaderDiagnostics } from '@/api/admin/accounts'

const props = defineProps<{ diagnostics?: OpenAIHeaderDiagnostics }>()
const { t } = useI18n()
const prefix = 'admin.accounts.responseEvidence'
const fields = ['imagegen_request_id', 'authorization_error', 'identity_error_code'] as const
function value(field: typeof fields[number]): string {
  const diagnostic = props.diagnostics?.[field]
  return typeof diagnostic === 'string' && diagnostic.trim() ? diagnostic : t(`${prefix}.notRecorded`)
}
</script>
