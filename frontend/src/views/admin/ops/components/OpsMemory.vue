<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useIntervalFn } from '@vueuse/core'
import { Chart as ChartJS, CategoryScale, Legend, LineElement, LinearScale, PointElement, Tooltip } from 'chart.js'
import { Line } from 'vue-chartjs'
import Select from '@/components/common/Select.vue'
import { opsAPI, type OpsMemoryResponse, type OpsMemorySample, type OpsMemoryHistogram } from '@/api/admin/ops'
import { formatBytes, formatDateTime } from '@/utils/format'

ChartJS.register(CategoryScale, Legend, LineElement, LinearScale, PointElement, Tooltip)
const { t } = useI18n()
const label = (key: string) => t(`admin.ops.memoryObservation.${key}`)
const selectedInstance = ref('')
const response = ref<OpsMemoryResponse | null>(null)
const loading = ref(true)
const error = ref('')
const profileError = ref('')
const profileBusy = ref(false)
const now = ref(Date.now())
const chartGroup = ref('memoryGroup')
let loadController: AbortController | null = null
let profileController: AbortController | null = null
let generation = 0
let mounted = true

const history = computed(() => response.value?.history ?? [])
const latest = computed(() => history.value.at(-1))
const currentInstance = computed(() => response.value?.current_instance_id ?? '')
const localSelected = computed(() => !error.value && !!currentInstance.value && selectedInstance.value === currentInstance.value)
const sampleState = computed(() => {
  if (error.value || !latest.value) return 'unavailable'
  const at = Date.parse(latest.value.at)
  const staleAfter = (response.value?.stale_after_seconds ?? 30) * 1000
  return !Number.isFinite(at) || now.value - at > staleAfter ? 'stale' : 'fresh'
})
const profileEnabled = computed(() => {
  const profile = response.value?.profile
  return !!profile?.enabled && !!profile.expires_at && Date.parse(profile.expires_at) > now.value
})
const profileInProgress = computed(() => profileBusy.value || !!response.value?.profile.in_progress)
const instanceOptions = computed(() => {
  const options = (response.value?.instances ?? []).map(instance => ({
    value: instance.instance_id,
    label: `${instance.instance_id}${instance.stale ? ` · ${label('stale')}` : ''}`
  }))
  if (selectedInstance.value && !options.some(item => item.value === selectedInstance.value)) {
    options.push({ value: selectedInstance.value, label: selectedInstance.value })
  }
  return options
})
const chartOptions = computed(() => ['memoryGroup', 'allocationGroup', 'runtimeGroup', 'cacheGroup', 'cacheBytesGroup', 'queueGroup', 'bufferGroup'].map(value => ({ value, label: label(value) })))
const bytes = (value?: number) => typeof value === 'number' && Number.isFinite(value) ? formatBytes(value) : '—'
const number = (value?: number) => typeof value === 'number' && Number.isFinite(value) ? value.toLocaleString() : '—'
const time = (value?: string | null) => value && Number.isFinite(Date.parse(value)) ? formatDateTime(value) : '—'

function allocationRate(sample: OpsMemorySample, index: number): number | null {
  const previous = history.value[index - 1]
  if (!previous) return null
  const seconds = (Date.parse(sample.at) - Date.parse(previous.at)) / 1000
  const allocated = sample.alloc_bytes_total - previous.alloc_bytes_total
  return seconds > 0 && allocated >= 0 ? allocated / seconds : null
}

const summary = computed(() => {
  const sample = latest.value
  const rate = sample ? allocationRate(sample, history.value.length - 1) : null
  return [
    { key: 'rss', value: sample?.rss_available ? bytes(sample.rss_bytes) : label('unavailable') },
    { key: 'heapAlloc', value: bytes(sample?.heap_alloc_bytes) },
    { key: 'heapReleased', value: bytes(sample?.heap_released_bytes) },
    { key: 'allocRate', value: rate === null ? '—' : `${bytes(rate)}/s` },
    { key: 'goroutines', value: number(sample?.goroutines) },
    { key: 'waiting', value: number(sample?.usage_queue.waiting) }
  ]
})
const runtimeRows = computed(() => {
  const sample = latest.value
  return [
    ['heapInuse', bytes(sample?.heap_inuse_bytes)], ['heapIdle', bytes(sample?.heap_idle_bytes)],
    ['sys', bytes(sample?.sys_bytes)], ['stack', bytes(sample?.stack_inuse_bytes)],
    ['allocTotal', bytes(sample?.alloc_bytes_total)], ['freeTotal', bytes(sample?.free_bytes_total)],
    ['heapObjects', number(sample?.heap_objects)], ['gcCycles', number(sample?.gc_cycles)]
  ]
})
const queueRows = computed(() => {
  const queue = latest.value?.usage_queue
  return [
    ['capacity', number(queue?.capacity)], ['waiting', number(queue?.waiting)], ['running', number(queue?.running)],
    ['submitted', number(queue?.submitted)], ['completed', number(queue?.completed)], ['failed', number(queue?.failed)],
    ['dropped', number(queue?.dropped)], ['syncFallback', number(queue?.sync_fallback)],
    ['settlementSucceeded', number(queue?.settlement_succeeded)], ['settlementFailed', number(queue?.settlement_failed)]
  ]
})
const activityRows = computed(() => {
  const activity = latest.value?.activity
  return [
    ['activeBodies', number(activity?.active_bodies)], ['activeBodyBytes', bytes(activity?.active_body_bytes)],
    ['largeBodies', number(activity?.active_large_bodies)], ['largeBodyBytes', bytes(activity?.active_large_body_bytes)],
    ['activeSse', number(activity?.active_sse)], ['sseBuffers', number(activity?.active_sse_buffers)],
    ['sseBufferBytes', bytes(activity?.active_sse_buffer_bytes)], ['sseBufferMax', bytes(activity?.sse_buffer_max_bytes)]
  ]
})
const histograms = computed(() => {
  const sample = latest.value
  if (!sample) return []
  return [
    { key: 'gcPause', value: sample.gc_pause_seconds, scale: 1000 },
    { key: 'queueWait', value: sample.usage_queue.wait_ms, scale: 1 },
    { key: 'queueExecution', value: sample.usage_queue.execution_ms, scale: 1 },
    { key: 'bodySize', value: sample.activity.body_size_bytes, scale: 1 },
    { key: 'sseBufferSize', value: sample.activity.sse_buffer_size_bytes, scale: 1 }
  ]
})
function histogramBuckets(histogram: OpsMemoryHistogram, scale: number) {
  return histogram.counts.map((count, index) => ({
    range: index === 0 ? `≤ ${number(histogram.bounds[0] * scale)}`
      : index >= histogram.bounds.length ? `> ${number(histogram.bounds[index - 1] * scale)}`
        : `${number(histogram.bounds[index - 1] * scale)} – ${number(histogram.bounds[index] * scale)}`,
    count
  }))
}

const chartData = computed(() => {
  const mib = 1024 * 1024
  const series: Array<{ name: string; get: (sample: OpsMemorySample, index: number) => number | null }> = []
  switch (chartGroup.value) {
    case 'allocationGroup':
      series.push({ name: label('allocRate'), get: (sample, index) => { const rate = allocationRate(sample, index); return rate === null ? null : rate / mib } })
      break
    case 'runtimeGroup':
      series.push({ name: label('gcCycles'), get: sample => sample.gc_cycles }, { name: label('goroutines'), get: sample => sample.goroutines })
      break
    case 'cacheBytesGroup':
    case 'cacheGroup':
      for (const name of new Set(history.value.flatMap(sample => (sample.caches ?? []).map(cache => cache.name)))) {
        series.push({ name, get: sample => {
          const cache = sample.caches?.find(cache => cache.name === name)
          return cache ? (chartGroup.value === 'cacheGroup' ? cache.entries : cache.estimated_bytes / mib) : null
        } })
      }
      break
    case 'bufferGroup':
      series.push({ name: label('activeBodyBytes'), get: sample => sample.activity.active_body_bytes / mib },
        { name: label('largeBodyBytes'), get: sample => sample.activity.active_large_body_bytes / mib },
        { name: label('sseBufferBytes'), get: sample => typeof sample.activity.active_sse_buffer_bytes === 'number' ? sample.activity.active_sse_buffer_bytes / mib : null })
      break
    case 'queueGroup':
      series.push({ name: label('waiting'), get: sample => sample.usage_queue.waiting }, { name: label('running'), get: sample => sample.usage_queue.running },
        { name: label('largeBodies'), get: sample => sample.activity.active_large_bodies }, { name: label('activeSse'), get: sample => sample.activity.active_sse })
      break
    default:
      series.push({ name: label('rss'), get: sample => sample.rss_available ? sample.rss_bytes / mib : null },
        { name: label('heapAlloc'), get: sample => sample.heap_alloc_bytes / mib }, { name: label('heapInuse'), get: sample => sample.heap_inuse_bytes / mib },
        { name: label('heapReleased'), get: sample => sample.heap_released_bytes / mib }, { name: label('sys'), get: sample => sample.sys_bytes / mib })
  }
  const colors = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ef4444', '#06b6d4', '#ec4899']
  return {
    labels: history.value.map(sample => new Date(sample.at).toLocaleTimeString()),
    datasets: series.map((item, index) => ({ label: item.name, data: history.value.map(item.get), borderColor: colors[index % colors.length], pointRadius: 0, pointHitRadius: 8, borderWidth: 2, tension: 0.1, spanGaps: false }))
  }
})
const lineOptions = {
  responsive: true, maintainAspectRatio: false, animation: false as const,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: { legend: { position: 'top' as const, labels: { color: '#9ca3af', usePointStyle: true, boxWidth: 6 } } },
  scales: { x: { grid: { display: false }, ticks: { maxTicksLimit: 8, color: '#9ca3af' } }, y: { beginAtZero: true, ticks: { color: '#9ca3af' }, grid: { color: '#9ca3af20' } } }
}

async function loadData() {
  loadController?.abort()
  const controller = new AbortController()
  loadController = controller
  const requestGeneration = ++generation
  const instance = selectedInstance.value
  loading.value = true
  error.value = ''
  try {
    const data = await opsAPI.getMemoryObservation(instance || undefined, { signal: controller.signal })
    if (!mounted || requestGeneration !== generation || controller.signal.aborted) return
    if (!data.selected_instance_id || (instance && data.selected_instance_id !== instance) || !Array.isArray(data.history)) {
      if (response.value) response.value = { ...response.value, history: [] }
      error.value = label('mismatch')
      return
    }
    // Pin the initial node explicitly; subsequent load-balanced requests must not silently select another process.
    if (!selectedInstance.value) selectedInstance.value = data.selected_instance_id
    response.value = data
    now.value = Date.now()
  } catch {
    if (!mounted || requestGeneration !== generation || controller.signal.aborted) return
    if (response.value) response.value = { ...response.value, history: [] }
    error.value = label('loadFailed')
  } finally {
    if (mounted && requestGeneration === generation) loading.value = false
  }
}

function selectInstance(value: string | number | boolean | null) {
  const next = String(value ?? '')
  if (!next || next === selectedInstance.value) return
  profileController?.abort()
  selectedInstance.value = next
  // Keep node discovery and profile identity, but clear all selected-node values immediately.
  if (response.value) response.value = { ...response.value, selected_instance_id: next, history: [] }
  profileError.value = ''
  void loadData()
}

async function changeProfile(enabled: boolean) {
  if (!localSelected.value || loading.value || profileInProgress.value) return
  await profileAction(async (instance, signal) => {
    const state = await opsAPI.setHeapProfileEnabled(instance, enabled, { signal })
    if (!signal.aborted && localSelected.value && selectedInstance.value === instance && response.value) response.value.profile = state
  })
}

async function downloadProfile() {
  if (!localSelected.value || !profileEnabled.value || loading.value || profileInProgress.value) return
  await profileAction(async (instance, signal) => {
    const blob = await opsAPI.downloadHeapProfile(instance, { signal })
    if (signal.aborted || !mounted || selectedInstance.value !== instance || currentInstance.value !== instance) return
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `heap-${instance.replace(/[^a-zA-Z0-9_.-]/g, '_')}.pprof`
    anchor.click()
    URL.revokeObjectURL(url)
  })
}

async function profileAction(action: (instance: string, signal: AbortSignal) => Promise<void>) {
  const controller = new AbortController()
  profileController = controller
  profileBusy.value = true
  profileError.value = ''
  try {
    await action(selectedInstance.value, controller.signal)
  } catch {
    if (mounted && !controller.signal.aborted) profileError.value = label('profileFailed')
  } finally {
    if (mounted && profileController === controller) profileBusy.value = false
  }
}

watch(currentInstance, (next, previous) => {
  if (previous && next !== previous) profileController?.abort()
})
useIntervalFn(() => { now.value = Date.now() }, 1000)
useIntervalFn(() => { if (!loading.value && !profileBusy.value) void loadData() }, 10000)
onMounted(() => { void loadData() })
onUnmounted(() => { mounted = false; generation++; loadController?.abort(); profileController?.abort() })
</script>

<template>
  <section class="space-y-5 rounded-3xl bg-white p-6 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700" :aria-label="label('title')">
    <div class="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ label('title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ label('description') }}</p>
        <p class="mt-1 break-all text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.memoryObservation.currentNode', { id: currentInstance || '—' }) }}</p>
      </div>
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <Select class="w-64 max-w-full" :model-value="selectedInstance" :options="instanceOptions" :aria-label="label('instance')" @update:model-value="selectInstance" />
        <button type="button" class="btn btn-secondary" :disabled="loading || profileBusy" @click="loadData">{{ label('refresh') }}</button>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-3 text-xs" aria-live="polite">
      <span class="rounded-full px-2 py-1 font-medium" :class="sampleState === 'fresh' ? 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400'">{{ loading ? t('admin.ops.loadingText') : label(sampleState) }}</span>
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.ops.memoryObservation.sampledAt', { time: time(latest?.at) }) }}</span>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="response && !response.redis_available" role="status" class="text-xs text-amber-700 dark:text-amber-400">{{ label('redisUnavailable') }}</p>
    <p v-if="response?.truncated" role="status" class="text-xs text-amber-700 dark:text-amber-400">{{ label('instancesTruncated') }}</p>

    <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <div v-for="metric in summary" :key="metric.key" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-900">
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ label(metric.key) }}</div>
        <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ metric.value }}</div>
      </div>
    </div>
    <div>
      <Select v-model="chartGroup" class="mb-3 w-64 max-w-full" :options="chartOptions" :aria-label="label('chart')" />
      <div v-if="history.length" class="h-64"><Line :data="chartData" :options="lineOptions" :aria-label="label(chartGroup)" /></div>
      <div v-else class="flex h-40 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ loading ? t('admin.ops.loadingText') : label('noSamples') }}</div>
    </div>

    <template v-if="latest">
      <div class="grid grid-cols-1 gap-5 lg:grid-cols-3">
        <div v-for="group in [{ key: 'runtime', rows: runtimeRows }, { key: 'queue', rows: queueRows }, { key: 'activity', rows: activityRows }]" :key="group.key" class="rounded-xl border border-gray-100 p-4 dark:border-dark-700">
          <h4 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ label(group.key) }}</h4>
          <dl class="space-y-2 text-xs">
            <div v-for="[key, value] in group.rows" :key="key" class="flex justify-between gap-3"><dt class="text-gray-500 dark:text-gray-400">{{ label(key) }}</dt><dd class="font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ value }}</dd></div>
          </dl>
        </div>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ label('queueHint') }}</p>
      <div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ label('caches') }}</h4>
        <p class="my-2 text-xs text-gray-500 dark:text-gray-400">{{ label('cacheHint') }}</p>
        <div class="overflow-x-auto">
          <table class="w-full whitespace-nowrap text-left text-xs">
            <thead class="text-gray-500 dark:text-gray-400"><tr><th v-for="key in ['cacheName', 'entries', 'estimatedBytes', 'hits', 'misses', 'evictions', 'maxEntries', 'maxBytes', 'ttl']" :key="key" class="px-2 py-2 font-medium">{{ label(key) }}</th></tr></thead>
            <tbody class="divide-y divide-gray-100 text-gray-900 dark:divide-dark-700 dark:text-gray-200"><tr v-for="cache in latest.caches" :key="cache.name"><td class="px-2 py-2 font-mono">{{ cache.name }}</td><td class="px-2 py-2">{{ number(cache.entries) }}</td><td class="px-2 py-2">{{ bytes(cache.estimated_bytes) }}</td><td class="px-2 py-2">{{ number(cache.hits) }}</td><td class="px-2 py-2">{{ number(cache.misses) }}</td><td class="px-2 py-2">{{ number(cache.evictions) }}</td><td class="px-2 py-2">{{ number(cache.max_entries) }}</td><td class="px-2 py-2">{{ bytes(cache.max_bytes) }}</td><td class="px-2 py-2">{{ number(cache.ttl_seconds) }}</td></tr></tbody>
          </table>
        </div>
      </div>
      <details class="rounded-xl border border-gray-100 p-4 dark:border-dark-700">
        <summary class="cursor-pointer text-sm font-semibold text-gray-900 dark:text-white">{{ label('histograms') }}</summary>
        <div class="mt-4 grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-5">
          <div v-for="histogram in histograms" :key="histogram.key">
            <h5 class="mb-2 text-xs font-medium text-gray-700 dark:text-gray-300">{{ label(histogram.key) }}</h5>
            <table class="w-full text-xs"><thead class="text-gray-500 dark:text-gray-400"><tr><th class="text-left font-normal">{{ label('bucket') }}</th><th class="text-right font-normal">{{ label('count') }}</th></tr></thead><tbody class="text-gray-700 dark:text-gray-300"><tr v-for="bucket in histogramBuckets(histogram.value, histogram.scale)" :key="bucket.range"><td class="py-1">{{ bucket.range }}</td><td class="text-right tabular-nums">{{ number(bucket.count) }}</td></tr></tbody></table>
          </div>
        </div>
      </details>
    </template>

    <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
      <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ label('profile') }}</h4>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ label('profileHint') }}</p>
      <p v-if="!localSelected" class="mt-2 text-xs text-amber-700 dark:text-amber-400">{{ t('admin.ops.memoryObservation.localOnly', { id: currentInstance || '—' }) }}</p>
      <p v-else class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ profileEnabled ? t('admin.ops.memoryObservation.profileExpires', { time: time(response?.profile.expires_at) }) : label('profileDisabled') }}</p>
      <p v-if="profileInProgress" role="status" class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ label('profileBusy') }}</p>
      <p v-if="profileError" role="alert" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ profileError }}</p>
      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary" :disabled="!localSelected || loading || profileInProgress" @click="changeProfile(!profileEnabled)">{{ label(profileEnabled ? 'disableProfile' : 'enableProfile') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="!localSelected || !profileEnabled || loading || profileInProgress" @click="downloadProfile">{{ label('downloadProfile') }}</button>
      </div>
    </div>
  </section>
</template>
