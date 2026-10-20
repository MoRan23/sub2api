<template>
  <div v-if="account.platform === 'openai' && (account.type === 'oauth' || account.type === 'apikey')" class="w-44 space-y-1 text-xs" data-testid="attribution-cell">
    <span v-if="summary?.active" class="inline-flex rounded bg-blue-700 px-1.5 py-0.5 text-white" data-testid="attribution-active">{{ t(`attribution.status.${summary.active.status}`) }}</span>
    <p v-if="summary?.active?.status === 'retry_wait' && (summary.active.snapshot.version ?? 0) >= 2" class="text-gray-500 dark:text-gray-400">{{ t('attribution.probeProgress', { count: summary.active.result.completed_probes ?? 0 }) }} · {{ t('attribution.retryCount', { count: summary.active.result.retries ?? 0 }) }}</p>
    <p v-if="summary?.active?.status === 'retry_wait' && summary.active.next_attempt_at" class="text-gray-500 dark:text-gray-400">{{ t('attribution.nextAttempt') }}: {{ formatDateTime(summary.active.next_attempt_at) }}</p>
    <p :class="color" data-testid="attribution-latest">{{ latest ? t(`attribution.status.${latest.status}`) : t('attribution.never') }}<span v-if="manualOnly" class="ml-1 text-gray-500 dark:text-gray-400">· {{ t('attribution.manualOnly') }}</span></p>
    <p v-if="account.schedulable === false && summary?.scheduling_paused_by_attribution" class="text-amber-700 dark:text-amber-300" data-testid="attribution-scheduling-paused">{{ t('attribution.pausedByAttribution') }}</p>
    <p v-if="latest?.result.analysis" class="truncate" :title="t('attribution.probability')">{{ latest.result.analysis.prediction }} · {{ latest.result.analysis.probability == null ? t('attribution.unavailable') : (latest.result.analysis.probability * 100).toFixed(1) + '%' }}</p>
    <p v-if="latest" class="text-gray-500 dark:text-gray-400">{{ formatDateTime(latest.finished_at || latest.created_at) }}</p>
    <p v-if="summary?.skip_reason" class="truncate text-gray-500" :title="reason(summary.skip_reason)">{{ reason(summary.skip_reason) }}</p>
    <button class="text-primary-600 hover:underline dark:text-primary-400" @click="emit('open')">{{ t('attribution.column') }} / {{ t('attribution.history') }}</button>
  </div><span v-else class="text-xs text-gray-400">—</span>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountListItem } from '@/types'
import { formatDateTime } from '@/utils/format'
const props = defineProps<{ account: AccountListItem }>()
const emit = defineEmits<{ open: [] }>()
const { t, te } = useI18n()
const manualOnly = computed(() => props.account.platform === 'openai' && props.account.type === 'apikey' && props.account.extra?.openai_api_key_auto_attribution_enabled !== true)
const summary = computed(() => props.account.model_attribution)
const latest = computed(() => summary.value?.latest)
const color = computed(() => latest.value?.status === 'passed' ? 'text-emerald-700 dark:text-emerald-300' : latest.value?.status === 'mismatch' ? 'text-red-700 dark:text-red-300' : 'text-gray-500 dark:text-gray-400')
const reason = (code: string) => te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : code
</script>
