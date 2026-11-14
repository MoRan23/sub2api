<template>
  <div class="space-y-6 pb-8">
    <section class="card rounded-3xl ring-1 ring-gray-900/5 dark:ring-white/10">
      <div class="card-body space-y-4">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h1 class="page-title">{{ t('channelMonitorV2.title') }}</h1>
            <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.overview.description') }}</p>
            <p v-if="data?.coverage.data_through" class="mt-2 text-xs text-gray-500 dark:text-gray-400">
              {{ t('channelMonitorV2.updatedTo', { time: formatTime(data.coverage.data_through) }) }}
            </p>
          </div>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="refreshing" data-testid="overview-refresh" @click="load">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />
            {{ t('common.refresh') }}
          </button>
        </div>
        <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <div class="flex flex-wrap gap-1" role="group" :aria-label="t('channelMonitorV2.timeRange')">
            <button
              v-for="value in ranges"
              :key="value"
              type="button"
              class="tab"
              :class="{ 'tab-active': range === value }"
              :aria-pressed="range === value"
              :data-range="value"
              @click="setRange(value)"
            >{{ t(`channelMonitorV2.ranges.${value}`) }}</button>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400" role="status">
            <span v-if="data">{{ t('channelMonitorV2.overview.groupCount', { count: data.items.length }) }} · </span>
            {{ refreshing ? t('channelMonitorV2.updating') : t('channelMonitorV2.overview.refreshIn', { seconds: countdown }) }}
          </p>
        </div>
        <p v-if="data && (!data.coverage.availability_ready || !data.coverage.coverage_complete)" class="text-sm text-amber-700 dark:text-amber-300" data-testid="overview-coverage">
          {{ t('channelMonitorV2.overview.coveragePending') }}
        </p>
      </div>
    </section>

    <div v-if="error" class="card p-6 text-center" role="alert">
      <p class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <button type="button" class="btn btn-secondary mt-4" :disabled="refreshing" @click="load">{{ t('common.retry') }}</button>
    </div>
    <div v-else-if="!data" class="card flex justify-center p-12" role="status"><LoadingSpinner /></div>
    <div v-else-if="!data.items.length" class="card p-10 text-center text-gray-500 dark:text-gray-400" data-testid="overview-empty">
      {{ t('channelMonitorV2.overview.noGroups') }}
    </div>

    <section v-for="section in sections" :key="section.platform" class="space-y-3" :aria-label="section.label">
      <h2 class="flex items-center gap-2 text-base font-semibold text-gray-800 dark:text-gray-100">
        <span class="flex h-8 w-8 items-center justify-center rounded-xl" :class="platformBadgeLightClass(section.platform)" data-testid="platform-section-icon">
          <PlatformIcon :platform="section.platform" size="md" />
        </span>
        {{ section.label }}
        <span class="rounded-lg bg-gray-100 px-2 py-0.5 text-xs font-normal text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ section.items.length }}</span>
      </h2>
      <div class="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3 min-[2100px]:grid-cols-4">
        <GroupOverviewCard v-for="item in section.items" :key="item.group_id" :item="item" :range="range" :bucket-seconds="data?.coverage.bucket_seconds || 0" />
      </div>
    </section>

    <footer v-if="data?.items.length" class="space-y-2 text-xs text-gray-500 dark:text-gray-400">
      <div class="flex flex-wrap gap-4">
        <span v-for="state in ['healthy', 'warning', 'critical', 'no_data']" :key="state" class="flex items-center gap-1.5">
          <span class="h-2 w-2 rounded-full" :class="legendClasses[state]" />{{ t(`channelMonitorV2.overview.states.${state}`) }}
        </span>
      </div>
      <p>{{ t('channelMonitorV2.overview.metricsNote') }}</p>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { listPlatforms, getPlatformSpec } from '@/constants/platformCatalog'
import { getGroupOverview, type GroupOverviewCard as Card, type GroupOverviewResponse, type MonitorRange } from '@/api/channelMonitorV2'
import { extractApiErrorMessage } from '@/utils/apiError'
import { platformBadgeLightClass } from '@/utils/platformColors'
import GroupOverviewCard from './GroupOverviewCard.vue'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const ranges: MonitorRange[] = ['90m', '24h', '7d', '30d']
const parseRange = (value: unknown): MonitorRange => ranges.includes(value as MonitorRange) ? value as MonitorRange : '90m'
const range = ref(parseRange(route.query.range))
const data = shallowRef<GroupOverviewResponse | null>(null)
const error = ref('')
const refreshing = ref(false)
const countdown = ref(0)
let intervalSeconds = 300
let nextRefreshAt = 0
let timer: number | undefined
let sequence = 0
let disposed = false
let pending: { range: MonitorRange; controller: AbortController; promise: Promise<void> } | undefined

const sections = computed(() => {
  const grouped = new Map<string, Card[]>()
  for (const item of data.value?.items || []) {
    const items = grouped.get(item.platform) || []
    items.push(item)
    grouped.set(item.platform, items)
  }
  const order = [...listPlatforms().map(platform => platform.id), 'composite']
  return [...grouped.entries()].sort(([a], [b]) => {
    const ai = order.indexOf(a), bi = order.indexOf(b)
    return (ai < 0 ? order.length : ai) - (bi < 0 ? order.length : bi) || a.localeCompare(b)
  }).map(([platform, items]) => ({ platform, items, label: getPlatformSpec(platform)?.display_name || (platform === 'composite' ? 'Composite' : platform) }))
})
const legendClasses: Record<string, string> = { healthy: 'bg-emerald-500', warning: 'bg-amber-400', critical: 'bg-rose-500', no_data: 'bg-gray-400' }
const dateFormatter = computed(() => new Intl.DateTimeFormat(locale.value || undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }))
function formatTime(value: string) {
  const time = new Date(value)
  return Number.isFinite(time.getTime()) ? dateFormatter.value.format(time) : '—'
}
function load(): Promise<void> {
  if (disposed || document.hidden) return Promise.resolve()
  if (pending?.range === range.value) return pending.promise
  pending?.controller.abort()
  const controller = new AbortController()
  const id = ++sequence
  const requestedRange = range.value
  refreshing.value = true
  error.value = ''
  const promise = (async () => {
    try {
      const response = await getGroupOverview(requestedRange, controller.signal)
      if (disposed || id !== sequence) return
      if (response.range !== requestedRange || !Array.isArray(response.items) || !response.coverage) throw new Error(t('channelMonitorV2.loadFailed'))
      data.value = response
      intervalSeconds = response.refresh_interval_seconds === 60 ? 60 : 300
    } catch (cause) {
      if (disposed || id !== sequence || controller.signal.aborted) return
      // Never keep cards whose authorization could have been revoked since the last response.
      data.value = null
      error.value = extractApiErrorMessage(cause, t('channelMonitorV2.loadFailed'))
    } finally {
      if (!disposed && id === sequence) {
        pending = undefined
        refreshing.value = false
        nextRefreshAt = Date.now() + intervalSeconds * 1000
        countdown.value = intervalSeconds
      }
    }
  })()
  pending = { range: requestedRange, controller, promise }
  return promise
}
function setRange(value: MonitorRange) {
  if (range.value === value) return
  range.value = value
  void router.replace({ query: { ...route.query, range: value } })
}
watch(() => route.query.range, value => { range.value = parseRange(value) })
watch(range, () => {
  data.value = null
  error.value = ''
  void load()
})
function visibilityChanged() {
  if (timer !== undefined) window.clearInterval(timer)
  timer = undefined
  if (document.hidden) {
    ++sequence
    pending?.controller.abort()
    pending = undefined
    refreshing.value = false
    return
  }
  void load()
  timer = window.setInterval(() => {
    countdown.value = Math.max(0, Math.ceil((nextRefreshAt - Date.now()) / 1000))
    if (!refreshing.value && Date.now() >= nextRefreshAt) void load()
  }, 1000)
}
onMounted(() => {
  document.addEventListener('visibilitychange', visibilityChanged)
  visibilityChanged()
})
onBeforeUnmount(() => {
  disposed = true
  ++sequence
  pending?.controller.abort()
  if (timer !== undefined) window.clearInterval(timer)
  document.removeEventListener('visibilitychange', visibilityChanged)
})
</script>
