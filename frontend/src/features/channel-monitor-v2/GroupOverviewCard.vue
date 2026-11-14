<template>
  <article class="card min-w-0 rounded-3xl p-5 ring-1 ring-gray-900/5 dark:ring-white/10 sm:p-6" :data-group-id="item.group_id">
    <header class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 gap-3">
        <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl" :class="platformBadgeLightClass(item.platform)" data-testid="platform-card-icon">
          <PlatformIcon :platform="item.platform" size="lg" />
        </span>
        <div class="min-w-0">
          <h3 class="break-words font-semibold text-gray-900 dark:text-gray-100">{{ groupName }}</h3>
          <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            <span class="rounded-md px-1.5 py-0.5" :class="platformBadgeLightClass(item.platform)" data-testid="platform-label">{{ platformName }}</span>
            <span>{{ t('channelMonitorV2.overview.realRequests') }}</span>
            <span class="text-primary-700 dark:text-primary-300">{{ t('channelMonitorV2.overview.multiplier', { value: multiplier }) }}</span>
          </p>
        </div>
      </div>
      <span class="shrink-0 rounded-full px-2 py-1 text-xs font-medium" :class="badgeClass(item.state)" data-testid="group-state">
        {{ stateText(item.state) }}
      </span>
    </header>

    <dl class="mt-5 grid grid-cols-3 gap-2">
      <div class="metric-box" :title="t('channelMonitorV2.overview.cacheHint')">
        <dt>{{ t('channelMonitorV2.metrics.cacheRate') }}</dt>
        <dd>{{ percent(item.metrics.cache_rate) }}</dd>
      </div>
      <div class="metric-box" :title="t('channelMonitorV2.overview.availabilityHint')">
        <dt>{{ t('channelMonitorV2.overview.availability') }}</dt>
        <dd :class="metricClass(item.state)">{{ percent(item.metrics.availability_rate) }}</dd>
      </div>
      <div class="metric-box" :title="t('channelMonitorV2.overview.ttftHint')">
        <dt>{{ t('channelMonitorV2.metrics.ttftP50') }}</dt>
        <dd>{{ latency(item.metrics.ttft_p50_ms) }}</dd>
      </div>
    </dl>

    <div class="mt-5">
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.overview.historyLabel', { count: item.buckets.length }) }}</p>
      <div class="mt-2 flex h-7 items-end gap-1" :aria-label="t('channelMonitorV2.overview.requestHistory')">
        <button
          v-for="bucket in item.buckets"
          :key="bucket.bucket_start"
          type="button"
          class="history-cell"
          :class="barClass(bucket.state)"
          :title="bucketTooltip(bucket)"
          :aria-label="bucketTooltip(bucket)"
          :data-state="bucket.state"
        ><span class="sr-only">{{ bucketTooltip(bucket) }}</span></button>
      </div>
      <div class="mt-1 flex justify-between text-[10px] uppercase text-gray-400 dark:text-gray-500">
        <span>{{ t('channelMonitorV2.overview.past', { range }) }}</span><span>{{ t('channelMonitorV2.overview.now') }}</span>
      </div>
    </div>

    <div class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h4 class="text-xs font-semibold text-gray-700 dark:text-gray-200">{{ t('channelMonitorV2.overview.attribution') }}</h4>
        <span class="text-xs" :class="attributionTextClass(attributionState)" :data-state="attributionState" data-testid="attribution-state">
          {{ attributionSummaryText }}
        </span>
      </div>
      <p v-if="item.attribution?.probe_model" class="mt-1 break-words text-[11px] text-gray-500 dark:text-gray-400" data-testid="attribution-probe-model">
        {{ t('channelMonitorV2.overview.probeModel', { model: item.attribution.probe_model }) }}
      </p>
      <div class="mt-3 flex h-6 items-end gap-1" :aria-label="t('channelMonitorV2.overview.attributionHistory')">
        <button
          v-for="(bucket, index) in attributionHistory"
          :key="index"
          type="button"
          class="history-cell attribution-cell"
          :class="attributionBarClass(bucket.state)"
          :title="attributionTooltip(bucket)"
          :aria-label="attributionTooltip(bucket)"
          :data-result="bucket.state"
        ><span class="sr-only">{{ attributionTooltip(bucket) }}</span></button>
      </div>
      <div class="mt-1 flex justify-between text-[10px] text-gray-400 dark:text-gray-500">
        <span>{{ t('channelMonitorV2.overview.attributionPast') }}</span><span>{{ t('channelMonitorV2.overview.now') }}</span>
      </div>
      <p v-if="item.attribution?.as_of" class="mt-2 text-right text-[10px] text-gray-400 dark:text-gray-500" data-testid="attribution-through">{{ t('channelMonitorV2.updatedTo', { time: formatTime(item.attribution.as_of) }) }}</p>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformBadgeLightClass, platformLabel } from '@/utils/platformColors'
import type { GroupAttributionHistoryBucket, GroupAttributionSummaryState, GroupOverviewCard, GroupOverviewState, MonitorRange } from '@/api/channelMonitorV2'

const props = defineProps<{ item: GroupOverviewCard; range: MonitorRange; bucketSeconds: number }>()
const { t, locale } = useI18n()
const groupName = computed(() => props.item.group_i18n?.[locale.value]?.name || props.item.group_name)
const platformName = computed(() => platformLabel(props.item.platform))
const numberFormatter = computed(() => new Intl.NumberFormat(locale.value || undefined, { maximumFractionDigits: 4 }))
const percentFormatter = computed(() => new Intl.NumberFormat(locale.value || undefined, { style: 'percent', minimumFractionDigits: 1, maximumFractionDigits: 1 }))
const dateFormatter = computed(() => new Intl.DateTimeFormat(locale.value || undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }))
const multiplier = computed(() => Number.isFinite(props.item.rate_multiplier) ? numberFormatter.value.format(props.item.rate_multiplier) : '—')
const attributionState = computed(() => normalizeAttributionState(props.item.attribution?.summary_state))
const attributionSummaryText = computed(() => attributionState.value === 'no_data'
  ? t(`channelMonitorV2.overview.${props.item.attribution?.enabled === false ? 'attributionDisabled' : 'noAttribution'}`)
  : attributionText(attributionState.value))
const attributionHistory = computed<GroupAttributionHistoryBucket[]>(() => {
  const attribution = props.item.attribution
  const buckets = Array.isArray(attribution?.history) ? attribution.history.slice(-20) : []
  const asOf = new Date(attribution?.as_of || '').getTime()
  // Only pad missing intervals. Account selection and aggregation belong to the server.
  return Array.from({ length: 20 }, (_, index) => {
    const bucket = buckets[index - (20 - buckets.length)]
    const end = asOf - (19 - index) * 10 * 60_000
    return {
      bucket_start: bucket?.bucket_start || (Number.isFinite(end) ? new Date(end - 10 * 60_000).toISOString() : ''),
      bucket_end: bucket?.bucket_end || (Number.isFinite(end) ? new Date(end).toISOString() : ''),
      state: index === 19 ? attributionState.value : normalizeAttributionState(bucket?.state),
    }
  })
})
const knownStates = new Set(['unmonitored', 'rebuilding', 'no_data', 'insufficient', 'healthy', 'warning', 'critical'])
const stateText = (state: GroupOverviewState) => t(`channelMonitorV2.overview.states.${knownStates.has(state) ? state : 'no_data'}`)
const attributionText = (state: GroupAttributionSummaryState) => t(`channelMonitorV2.overview.results.${state}`)
function normalizeAttributionState(state: unknown): GroupAttributionSummaryState {
  return state === 'passed' || state === 'mismatch' || state === 'failed' ? state : 'no_data'
}
function percent(value: number | null) { return value != null && Number.isFinite(value) && value >= 0 && value <= 1 ? percentFormatter.value.format(value) : '—' }
function latency(value: number | null) { return value != null && Number.isFinite(value) && value >= 0 ? `${(value / 1000).toFixed(2)}s` : '—' }
function formatTime(value: string) {
  const time = new Date(value)
  return Number.isFinite(time.getTime()) ? dateFormatter.value.format(time) : '—'
}
function bucketTooltip(bucket: GroupOverviewCard['buckets'][number]) {
  const start = new Date(bucket.bucket_start).getTime()
  const end = Number.isFinite(start) && props.bucketSeconds > 0 ? ` – ${formatTime(new Date(start + props.bucketSeconds * 1000).toISOString())}` : ''
  return `${formatTime(bucket.bucket_start)}${end}\n${stateText(bucket.state)} · ${t('channelMonitorV2.overview.availability')} ${percent(bucket.availability_rate)}`
}
function attributionTooltip(bucket: GroupAttributionHistoryBucket) {
  return `${formatTime(bucket.bucket_start)} – ${formatTime(bucket.bucket_end)}\n${attributionText(bucket.state)}`
}
function metricClass(state: GroupOverviewState) {
  if (state === 'healthy') return 'text-emerald-700 dark:text-emerald-400'
  if (state === 'warning') return 'text-amber-700 dark:text-amber-300'
  if (state === 'critical') return 'text-rose-600 dark:text-rose-400'
  return 'text-gray-500 dark:text-gray-400'
}
function badgeClass(state: GroupOverviewState) {
  if (state === 'healthy') return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-300'
  if (state === 'warning') return 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300'
  if (state === 'critical') return 'bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
}
function barClass(state: GroupOverviewState) {
  if (state === 'healthy') return 'h-6 bg-emerald-500 dark:bg-emerald-500'
  if (state === 'warning') return 'h-3 bg-amber-400 dark:bg-amber-400'
  if (state === 'critical') return 'h-2 bg-rose-500 dark:bg-rose-500'
  if (state === 'insufficient') return 'h-3 bg-gray-300 dark:bg-dark-500'
  if (state === 'rebuilding') return 'h-3 border border-dashed border-gray-400 bg-gray-100 dark:border-dark-400 dark:bg-dark-700'
  return 'h-1 bg-gray-300 dark:bg-dark-500'
}
function attributionBarClass(status: GroupAttributionSummaryState) {
  if (status === 'passed') return 'h-5 bg-emerald-500'
  if (status === 'mismatch') return 'h-3 bg-amber-400'
  if (status === 'failed') return 'h-2 bg-rose-500'
  return 'h-1 bg-gray-300 dark:bg-dark-500'
}
function attributionTextClass(status: GroupAttributionSummaryState) {
  if (status === 'passed') return 'text-emerald-700 dark:text-emerald-400'
  if (status === 'mismatch') return 'text-amber-700 dark:text-amber-300'
  if (status === 'failed') return 'text-rose-600 dark:text-rose-400'
  return 'text-gray-500 dark:text-gray-400'
}
</script>

<style scoped>
.metric-box { @apply min-w-0 rounded-2xl border border-gray-100 bg-gray-50/70 px-2.5 py-3 dark:border-dark-600 dark:bg-dark-700/40; }
.metric-box dt { @apply text-[10px] leading-4 text-gray-500 dark:text-gray-400; }
.metric-box dd { @apply mt-1 text-base font-bold tabular-nums sm:text-lg; }
.history-cell { @apply min-w-0 flex-1 rounded-[3px] transition-opacity hover:opacity-75 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500; }
</style>
