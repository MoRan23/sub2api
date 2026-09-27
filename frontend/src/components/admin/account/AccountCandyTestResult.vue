<template>
  <div class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-testid="candy-result">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <span class="font-medium">{{ item.account_name || `#${item.account_id}` }}</span>
      <AccountCandyTestStatus :status="item.status" />
    </div>
    <dl class="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
      <div v-for="field in fields" :key="field.label">
        <dt class="text-gray-500">{{ field.label }}</dt>
        <dd class="mt-1 break-words text-gray-800 dark:text-gray-200">{{ field.value }}</dd>
      </div>
    </dl>
    <p v-if="item.execution?.model_conflict" class="text-xs text-amber-600 dark:text-amber-400">{{ t('candyTests.modelConflict') }}</p>
    <p v-if="item.failure_code" role="status" class="break-words text-sm text-gray-600 dark:text-gray-300">
      {{ t('candyTests.error') }}: {{ failureReason }}
    </p>
    <table class="w-full text-left text-xs">
      <thead class="border-b border-gray-200 text-gray-500 dark:border-dark-600">
        <tr><th class="py-2">{{ t('candyTests.question') }}</th><th>{{ t('candyTests.answer') }}</th><th>{{ t('candyTests.expected') }}</th></tr>
      </thead>
      <tbody>
        <tr v-for="question in questions" :key="question.key">
          <td class="py-2">{{ t(question.label) }}</td>
          <td :class="answerClass(question.key, question.expected)">{{ item.answers?.[question.key] ?? '—' }}</td>
          <td>{{ question.expected }}</td>
        </tr>
      </tbody>
    </table>
    <details v-if="item.execution?.usage" class="text-xs">
      <summary class="cursor-pointer text-gray-500">{{ t('candyTests.usage') }}</summary>
      <div class="mt-2 flex flex-wrap gap-3">
        <span>{{ t('candyTests.inputTokens') }}: {{ item.execution.usage.input_tokens }}</span>
        <span>{{ t('candyTests.outputTokens') }}: {{ item.execution.usage.output_tokens }}</span>
        <span v-if="item.execution.usage.cache_read_input_tokens != null">{{ t('candyTests.cachedTokens') }}: {{ item.execution.usage.cache_read_input_tokens }}</span>
      </div>
    </details>
    <details :open="expanded">
      <summary class="cursor-pointer text-sm font-medium">{{ t('candyTests.rawAnswer') }}</summary>
      <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-900">{{ item.response_text || t('candyTests.noAnswer') }}</pre>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CandyTestAnswerKey, CandyTestItem } from '@/api/admin/candyTests'
import { formatDateTime } from '@/utils/format'
import AccountCandyTestStatus from './AccountCandyTestStatus.vue'

const props = withDefaults(defineProps<{ item: CandyTestItem; expanded?: boolean }>(), { expanded: false })
const { t, te } = useI18n()
const questions: { key: CandyTestAnswerKey; label: string; expected: number }[] = [
  { key: 'q1_fixed', label: 'candyTests.q1', expected: 32 },
  { key: 'q2_adaptive', label: 'candyTests.q2', expected: 29 },
  { key: 'q3_fixed', label: 'candyTests.q3Fixed', expected: 40 },
  { key: 'q3_adaptive', label: 'candyTests.q3Adaptive', expected: 38 },
]
const failureReason = computed(() => {
  const httpStatus = /^upstream_http_([1-5]\d\d)$/.exec(props.item.failure_code || '')?.[1]
  if (httpStatus) return t('candyTests.upstreamHttpError', { status: httpStatus })
  const key = `candyTests.failureReasons.${props.item.failure_code}`
  return te(key) ? t(key) : props.item.failure_code
})
const fields = computed(() => [
  { label: t('candyTests.requestedModel'), value: props.item.execution?.requested_model || props.item.model },
  { label: t('candyTests.effort'), value: props.item.reasoning_effort || t('candyTests.defaultEffort') },
  { label: t('candyTests.outboundModel'), value: props.item.execution?.actual_model || t('candyTests.missing') },
  { label: t('candyTests.upstreamModel'), value: props.item.execution?.upstream_model || t('candyTests.missing') },
  { label: t('candyTests.modelEvidenceSource'), value: props.item.execution?.model_evidence_source || '—' },
  { label: t('candyTests.startedAt'), value: formatDateTime(props.item.started_at) || '—' },
  { label: t('candyTests.completedAt'), value: formatDateTime(props.item.finished_at) || '—' },
  { label: t('candyTests.duration'), value: props.item.execution ? t('candyTests.seconds', { seconds: (props.item.execution.duration_ms / 1000).toFixed(1) }) : '—' },
])
function answerClass(key: CandyTestAnswerKey, expected: number): string {
  const answer = props.item.answers?.[key]
  if (answer == null) return 'text-gray-400'
  return answer === expected ? 'text-emerald-600 dark:text-emerald-400' : 'font-semibold text-red-600 dark:text-red-400'
}
</script>
