<template>
  <div v-if="account.platform === 'openai' && (account.type === 'oauth' || account.type === 'apikey')" class="w-44 space-y-1 text-xs" data-testid="attribution-cell">
    <span v-if="summary?.active" class="inline-flex rounded bg-blue-700 px-1.5 py-0.5 text-white" data-testid="attribution-active">{{ t(`attribution.status.${summary.active.status}`) }}</span>
    <p :class="color" data-testid="attribution-latest">{{ latest ? t(`attribution.status.${latest.status}`) : t('attribution.never') }}<span v-if="account.type === 'apikey'" class="ml-1 text-gray-500 dark:text-gray-400">· {{ t('attribution.manualOnly') }}</span></p>
    <p v-if="latest?.result.analysis" class="truncate" :title="latest.result.analysis.prediction">{{ latest.result.analysis.prediction }} · {{ (latest.result.analysis.probability * 100).toFixed(1) }}%</p>
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
const summary = computed(() => props.account.model_attribution)
const latest = computed(() => summary.value?.latest)
const color = computed(() => latest.value?.status === 'passed' ? 'text-emerald-700 dark:text-emerald-300' : latest.value?.status === 'mismatch' ? 'text-red-700 dark:text-red-300' : 'text-gray-500 dark:text-gray-400')
const reason = (code: string) => te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : code
</script>
