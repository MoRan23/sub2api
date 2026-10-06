<template>
  <article class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-testid="attribution-result">
    <div class="flex items-center justify-between gap-3"><h3 class="font-medium">{{ job.account_name }} · #{{ job.id }}</h3><span class="font-medium">{{ t(`attribution.status.${job.status}`) }}</span></div>
    <p v-if="job.reason" class="text-sm text-amber-700 dark:text-amber-300">{{ reason(job.reason) }}</p>
    <dl class="grid grid-cols-2 gap-3 text-sm">
      <div><dt class="text-gray-500">{{ t('attribution.model') }}</dt><dd class="break-all">{{ job.snapshot.policy.model }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.version') }} / {{ t('attribution.group') }}</dt><dd>{{ job.snapshot.config_version }} / {{ job.snapshot.group_id || t('attribution.allGroups') }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.time') }}</dt><dd>{{ formatDateTime(job.finished_at || job.started_at || job.created_at) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.duration') }}</dt><dd>{{ (job.result.duration_ms / 1000).toFixed(1) }} s</dd></div>
      <div v-if="job.result.retries"><dt class="text-gray-500">{{ t('attribution.retries') }}</dt><dd>{{ job.result.retries }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.action') }}</dt><dd>{{ t(`attribution.actions.${job.result.action || 'none'}`) }}</dd></div>
      <div v-if="job.result.pass_streak !== undefined"><dt class="text-gray-500">{{ t('attribution.passStreak') }}</dt><dd data-testid="attribution-pass-streak">{{ job.result.pass_streak }} / 2</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.source') }}</dt><dd>{{ t(`attribution.${job.source}`) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('attribution.detectorSource') }}</dt><dd>{{ job.snapshot.detector ? 'LM Fingerpoint Detector' : 'ModelTrace' }}</dd></div>
      <div v-if="job.snapshot.detector"><dt class="text-gray-500">{{ t('attribution.detectorVersion') }}</dt><dd class="break-all">{{ job.snapshot.detector.revision.slice(0, 12) }} · {{ job.snapshot.detector.bank_built_at }}</dd></div>
    </dl>
    <div v-if="job.result.analysis" class="space-y-2">
      <p v-if="job.snapshot.detector" class="text-xs text-gray-500">{{ t('attribution.confidenceHelp') }}</p>
      <p v-if="job.result.analysis.probability_status" class="text-xs">{{ t('attribution.calibration') }}: {{ t(`attribution.calibrationStatus.${job.result.analysis.probability_status}`) }}</p>
      <p class="text-sm font-medium">{{ t('attribution.top') }}: {{ job.result.analysis.prediction }} · {{ percent(job.result.analysis.probability) }}</p>
      <div v-for="candidate in job.result.analysis.results" :key="candidate.model" class="flex items-center gap-3 text-xs">
        <span class="w-48 shrink-0 truncate" :title="candidate.model">{{ candidate.model }}</span>
        <div class="h-2 flex-1 overflow-hidden rounded bg-gray-100 dark:bg-dark-600"><div v-if="candidate.probability != null" class="h-full bg-primary-500" :style="{ width: percent(candidate.probability) }" /></div><span class="w-16 text-right tabular-nums">{{ percent(candidate.probability) }}</span><span v-if="candidate.score !== undefined" class="w-24 text-right tabular-nums" :title="t('attribution.rankingScore')">{{ candidate.score.toFixed(4) }}</span>
      </div>
    </div>
    <details class="text-sm"><summary class="cursor-pointer">{{ t('attribution.high') }} / {{ t('attribution.low') }}</summary><p class="mt-2 break-all">{{ t('attribution.high') }}: {{ job.snapshot.policy.high_models?.join(', ') }}</p><p class="break-all">{{ t('attribution.low') }}: {{ job.snapshot.policy.low_models?.join(', ') }}</p></details>
    <details v-if="job.result.before || job.result.after" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.changes') }}</summary><div class="mt-2 grid gap-3 sm:grid-cols-2"><div><p>{{ t('attribution.before') }}</p><pre class="overflow-auto rounded bg-gray-100 p-2 text-xs dark:bg-dark-900">{{ JSON.stringify(job.result.before || {}, null, 2) }}</pre></div><div><p>{{ t('attribution.after') }}</p><pre class="overflow-auto rounded bg-gray-100 p-2 text-xs dark:bg-dark-900">{{ JSON.stringify(job.result.after || {}, null, 2) }}</pre></div></div></details>
    <details v-if="job.result.usage?.length" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.usage') }} · {{ t('attribution.evidence') }}</summary><p v-for="(usage, i) in job.result.usage" :key="i" class="mt-2 break-all">#{{ i + 1 }}: {{ usage?.input_tokens ?? '—' }} / {{ usage?.output_tokens ?? '—' }} · {{ job.result.actual_models?.[i] || '—' }} / {{ job.result.upstream_models?.[i] || '—' }}</p></details>
    <details v-if="job.result.analysis?.diagnostics?.length" class="text-sm"><summary class="cursor-pointer">{{ t('attribution.diagnostics') }}</summary><p v-for="d in job.result.analysis.diagnostics" :key="d.index">#{{ d.index + 1 }}: {{ d.parsed_numbers }} / {{ d.minimum_numbers }} · {{ d.accepted ? '✓' : '×' }}</p></details>
  </article>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import type { AttributionJob } from '@/api/admin/modelAttribution'
defineProps<{ job: AttributionJob }>()
const { t, te } = useI18n()
const reason = (code: string) => /^upstream_http_\d{3}$/.test(code) ? t('candyTests.upstreamHttpError', { status: code.slice(-3) }) : te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : te(`candyTests.failureReasons.${code}`) ? t(`candyTests.failureReasons.${code}`) : code
const percent = (p: number | null) => p == null ? t('attribution.unavailable') : `${(p * 100).toFixed(2)}%`
</script>
