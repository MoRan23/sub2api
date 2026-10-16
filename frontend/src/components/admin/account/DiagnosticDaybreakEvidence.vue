<template>
  <p class="break-all text-xs">
    <span class="text-gray-500">{{ t('diagnosticDaybreak.field') }}: </span>
    <span v-if="!evidence">{{ t('diagnosticDaybreak.notRecorded') }}</span>
    <template v-else>
      <code v-if="evidence.cyber">{{ evidence.cyber }}</code>
      <span v-else>{{ t('diagnosticDaybreak.notAdded') }}</span>
      <span v-if="evidence.reason" class="text-gray-500"> · {{ reason }}</span>
    </template>
  </p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DiagnosticDaybreakEvidence } from '@/types/diagnosticDaybreak'

const props = defineProps<{ evidence?: DiagnosticDaybreakEvidence | null }>()
const { t, te } = useI18n()
const reason = computed(() => {
  const code = props.evidence?.reason || ''
  const key = `diagnosticDaybreak.reasons.${code}`
  return te(key) ? t(key) : code
})
</script>
