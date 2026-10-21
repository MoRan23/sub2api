<template>
  <article class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-testid="attribution-result">
    <div class="flex flex-wrap items-center justify-between gap-3"><h3 class="font-medium">{{ job.account_name }} · #{{ job.id }}</h3><div class="flex items-center gap-3"><span class="font-medium">{{ t(`attribution.status.${job.status}`) }}</span><button v-if="isAttributionActive(job.status)" class="btn btn-secondary btn-sm" :disabled="cancelling" @click="emit('cancel', job.id)">{{ t(cancelling ? 'attribution.cancelling' : 'attribution.cancel') }}</button></div></div>
    <p v-if="job.reason" class="text-sm text-amber-700 dark:text-amber-300">{{ reason(job.reason) }}</p>
    <dl class="grid grid-cols-2 gap-3 text-sm">
      <div><dt class="text-gray-500">{{ t('attribution.model') }}</dt><dd class="break-all">{{ job.snapshot.policy.model }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.expectedModels') }}</dt><dd class="break-all" data-testid="result-expected-models">{{ (job.snapshot.policy.expected_models?.length ? job.snapshot.policy.expected_models : [job.snapshot.policy.model]).join(', ') }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.version') }} / {{ t('attribution.group') }}</dt><dd>{{ job.snapshot.config_version }} / {{ job.snapshot.group_id || t('attribution.allGroups') }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.time') }}</dt><dd>{{ formatDateTime(job.finished_at || job.started_at || job.created_at) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.duration') }}</dt><dd>{{ (job.result.duration_ms / 1000).toFixed(1) }} s</dd></div>
      <div v-if="(job.snapshot.version ?? 0) >= 2 && job.result.completed_probes !== undefined"><dt class="text-gray-500">{{ t('attribution.completedProbes') }}</dt><dd data-testid="attribution-completed-probes">{{ job.result.completed_probes }} / 3</dd></div>
      <div v-if="job.result.retries !== undefined"><dt class="text-gray-500">{{ t('attribution.retries') }}</dt><dd>{{ job.result.retries }}</dd></div>
      <div v-if="job.status === 'retry_wait' && job.next_attempt_at"><dt class="text-gray-500">{{ t('attribution.nextAttempt') }}</dt><dd>{{ formatDateTime(job.next_attempt_at) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.resultMode') }}</dt><dd data-testid="attribution-result-mode">{{ t(scheduleMode ? 'attribution.resultModes.schedule' : 'attribution.resultModes.replace') }}</dd></div>
      <div><dt class="text-gray-500">{{ t(scheduleMode ? 'attribution.schedulingAction' : 'attribution.action') }}</dt><dd data-testid="attribution-result-action">{{ actionText }}</dd></div>
      <div v-if="scheduleMode && (job.result.scheduling_before != null || job.result.scheduling_after != null)"><dt class="text-gray-500">{{ t('attribution.schedulingChange') }}</dt><dd data-testid="attribution-scheduling-change">{{ schedulingText(job.result.scheduling_before) }} → {{ schedulingText(job.result.scheduling_after) }}</dd></div>
      <div v-if="job.snapshot.daybreak"><dt class="text-gray-500">{{ t('diagnosticDaybreak.preferences') }}</dt><dd>Blue: {{ t(job.snapshot.daybreak.blue ? 'diagnosticDaybreak.enabled' : 'diagnosticDaybreak.disabled') }} / Red: {{ t(job.snapshot.daybreak.red ? 'diagnosticDaybreak.enabled' : 'diagnosticDaybreak.disabled') }}</dd></div>
      <div v-if="job.result.pass_streak !== undefined"><dt class="text-gray-500">{{ t('attribution.passStreak') }}</dt><dd data-testid="attribution-pass-streak">{{ job.result.pass_streak }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.source') }}</dt><dd>{{ t(`attribution.${job.source}`) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.detectorSource') }}</dt><dd>{{ job.snapshot.detector ? 'LM Fingerpoint Detector' : 'ModelTrace' }}</dd></div>
      <div v-if="job.snapshot.detector"><dt class="text-gray-500">{{ t('attribution.detectorVersion') }}</dt><dd class="break-all">{{ job.snapshot.detector.revision.slice(0, 12) }} · {{ job.snapshot.detector.bank_built_at }}</dd></div>
    </dl>
    <section class="space-y-2" :aria-label="t('diagnosticDaybreak.title')">
      <h4 class="text-sm font-medium">{{ t('diagnosticDaybreak.title') }}</h4>
      <template v-if="job.result.daybreak?.length">
        <div v-for="(evidence, i) in job.result.daybreak" :key="i" class="flex items-baseline gap-2"><span class="text-xs">#{{ i + 1 }}</span><DiagnosticDaybreakEvidence :evidence="evidence" /></div>
      </template>
      <DiagnosticDaybreakEvidence v-else />
    </section>
    <div v-if="job.result.analysis" class="space-y-2">
      <p v-if="job.snapshot.detector" class="text-xs text-gray-500">{{ t('attribution.confidenceHelp') }}</p>
      <p v-if="job.result.analysis.probability_status" class="text-xs">{{ t('attribution.calibration') }}: {{ t(`attribution.calibrationStatus.${job.result.analysis.probability_status}`) }}</p>
      <p class="text-sm font-medium">{{ t('attribution.top') }}: {{ job.result.analysis.prediction }} · {{ percent(job.result.analysis.probability) }}</p>
      <div v-for="candidate in job.result.analysis.results" :key="candidate.model" class="flex items-center gap-3 text-xs">
        <span class="w-48 shrink-0 truncate" :title="candidate.model">{{ candidate.model }}</span>
        <div class="h-2 flex-1 overflow-hidden rounded bg-gray-100 dark:bg-dark-600"><div v-if="candidate.probability != null" class="h-full bg-primary-500" :style="{ width: percent(candidate.probability) }" /></div><span class="w-16 text-right tabular-nums">{{ percent(candidate.probability) }}</span><span v-if="candidate.score !== undefined" class="w-24 text-right tabular-nums" :title="t('attribution.rankingScore')">{{ candidate.score.toFixed(4) }}</span>
      </div>
    </div>
    <details v-if="!scheduleMode" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.high') }} / {{ t('attribution.low') }}</summary><p class="mt-2 break-all">{{ t('attribution.high') }}: {{ job.snapshot.policy.high_models?.join(', ') }}</p><p class="break-all">{{ t('attribution.low') }}: {{ job.snapshot.policy.low_models?.join(', ') }}</p></details>
    <details v-if="job.result.before || job.result.after" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.changes') }}</summary><div class="mt-2 grid gap-3 sm:grid-cols-2"><div><p>{{ t('attribution.before') }}</p><pre class="overflow-auto rounded bg-gray-100 p-2 text-xs dark:bg-dark-900">{{ JSON.stringify(job.result.before || {}, null, 2) }}</pre></div><div><p>{{ t('attribution.after') }}</p><pre class="overflow-auto rounded bg-gray-100 p-2 text-xs dark:bg-dark-900">{{ JSON.stringify(job.result.after || {}, null, 2) }}</pre></div></div></details>
    <details v-if="job.result.usage?.length" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.usage') }} · {{ t('attribution.evidence') }}</summary><p v-for="(usage, i) in job.result.usage" :key="i" class="mt-2 break-all">#{{ i + 1 }}: {{ usage?.input_tokens ?? '—' }} / {{ usage?.output_tokens ?? '—' }} · {{ job.result.actual_models?.[i] || '—' }} / {{ job.result.upstream_models?.[i] || '—' }}</p></details>
    <details v-if="job.result.analysis?.diagnostics?.length" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.diagnostics') }}</summary><p v-for="d in job.result.analysis.diagnostics" :key="d.index">#{{ d.index + 1 }}: {{ d.parsed_numbers }} / {{ d.minimum_numbers }} · {{ d.accepted ? '✓' : '×' }}</p></details>
  </article>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import type { AttributionJob } from '@/api/admin/modelAttribution'
import { isAttributionActive } from '@/utils/modelAttribution'
import DiagnosticDaybreakEvidence from './DiagnosticDaybreakEvidence.vue'
const props = defineProps<{ job: AttributionJob; cancelling?: boolean }>()
const emit = defineEmits<{ cancel: [id: number] }>()
const { t, te } = useI18n()
// Older jobs predate scheduling mode; never infer their mode from the current account.
const scheduleMode = computed(() => (props.job.snapshot.version ?? 0) >= 4 && props.job.snapshot.result_mode === 'schedule')
const actionText = computed(() => {
  const action = props.job.result.action || 'none'
  if (scheduleMode.value && ['awaiting_confirmation', 'stale_policy'].includes(action)) return t(`attribution.schedulingActions.${action}`)
  return t(`attribution.actions.${action}`)
})
const schedulingText = (value: boolean | null | undefined) => t(value == null ? 'attribution.notRecorded' : value ? 'attribution.schedulingEnabled' : 'attribution.schedulingPaused')
const reason = (code: string) => /^upstream_http_\d{3}$/.test(code) ? t('candyTests.upstreamHttpError', { status: code.slice(-3) }) : te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : te(`candyTests.failureReasons.${code}`) ? t(`candyTests.failureReasons.${code}`) : code
const percent = (p: number | null) => p == null ? t('attribution.unavailable') : `${(p * 100).toFixed(2)}%`
</script>
