<template>
  <section class="card rounded-3xl p-5 ring-1 ring-gray-900/5 dark:ring-white/10" data-testid="official-status">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{ t('channelMonitorV2.officialStatus.title') }}</h2>
      <div class="flex items-center gap-3 text-xs">
        <a :href="data?.source_url || 'https://status.openai.com/'" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:underline dark:text-primary-400">{{ t('channelMonitorV2.officialStatus.source') }}</a>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" :aria-label="t('common.refresh')" data-testid="official-status-refresh" @click="load">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
        </button>
      </div>
    </div>
    <div class="mt-3 flex flex-wrap gap-x-6 gap-y-2" role="status">
      <div v-for="group in groups" :key="group.id" class="flex items-center gap-2 text-sm" :data-official-group="group.id" :title="group.fetched_at ? t('channelMonitorV2.updatedTo', { time: formatTime(group.fetched_at) }) : undefined">
        <span class="h-2 w-2 rounded-full" :class="statusClasses(group.stale || failed ? 'unknown' : group.status).dot" aria-hidden="true" />
        <span class="font-medium text-gray-800 dark:text-gray-200">{{ group.name }}</span>
        <span :class="statusClasses(group.stale || failed ? 'unknown' : group.status).text">{{ statusLabel(group.status) }}</span>
      </div>
    </div>
    <p v-if="loading && !data" class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.updating') }}</p>
    <p v-else-if="!hasSnapshot" class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.officialStatus.unavailable') }}</p>
    <p v-else-if="stale" class="mt-3 text-xs text-amber-700 dark:text-amber-300" data-testid="official-status-stale">{{ t('channelMonitorV2.officialStatus.delayed') }}</p>
    <button type="button" class="mt-3 text-xs text-gray-600 hover:text-primary-600 dark:text-gray-400 dark:hover:text-primary-400" :aria-expanded="expanded" aria-controls="official-status-incidents" data-testid="official-status-toggle" @click="expanded = !expanded">
      {{ t(expanded ? 'channelMonitorV2.officialStatus.hideEvents' : 'channelMonitorV2.officialStatus.showEvents') }}
    </button>
    <div v-if="expanded" id="official-status-incidents" class="mt-3 border-t border-gray-100 pt-3 dark:border-dark-700">
      <p v-if="!data?.incidents.length" class="text-sm text-gray-500 dark:text-gray-400">{{ t(data?.incidents_fetched_at ? 'channelMonitorV2.officialStatus.noEvents' : 'channelMonitorV2.officialStatus.unavailable') }}</p>
      <ul class="divide-y divide-gray-100 dark:divide-dark-700">
        <li v-for="incident in incidents" :key="incident.id" class="py-3 first:pt-0" data-testid="official-incident">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <a :href="incident.url" target="_blank" rel="noopener noreferrer" class="min-w-0 break-words text-sm font-medium text-gray-900 hover:underline dark:text-gray-100">{{ incident.title }}</a>
            <span class="shrink-0 text-xs" :class="statusClasses(data?.incidents_stale || failed ? 'unknown' : incident.status).text">{{ statusLabel(incident.status) }}</span>
          </div>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ formatTime(incident.updated_at) }}<span v-if="incident.components.length"> · {{ incident.components.join(' · ') }}</span></p>
          <p class="mt-2 whitespace-pre-line break-words text-sm leading-relaxed text-gray-600 dark:text-gray-300">{{ incident.summary }}</p>
        </li>
      </ul>
      <button v-if="(data?.incidents.length || 0) > 5" type="button" class="mt-2 text-xs text-primary-600 dark:text-primary-400" data-testid="official-status-more" @click="showMore = !showMore">{{ t(showMore ? 'channelMonitorV2.officialStatus.showLess' : 'channelMonitorV2.officialStatus.showMore') }}</button>
      <p v-if="data?.incidents_fetched_at" class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.updatedTo', { time: formatTime(data.incidents_fetched_at) }) }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getOfficialStatus, type OfficialStatusGroup, type OfficialStatusResponse } from '@/api/officialStatus'

const { t, locale } = useI18n()
const data = shallowRef<OfficialStatusResponse | null>(null)
const loading = ref(false)
const failed = ref(false)
const expanded = ref(false)
const showMore = ref(false)
const emptyGroups: OfficialStatusGroup[] = [
  { id: 'apis', name: 'APIs', status: 'unknown', fetched_at: null, stale: true },
  { id: 'chatgpt', name: 'ChatGPT', status: 'unknown', fetched_at: null, stale: true },
  { id: 'codex', name: 'Codex', status: 'unknown', fetched_at: null, stale: true },
]
const groups = computed(() => data.value?.groups || emptyGroups)
const incidents = computed(() => data.value?.incidents.slice(0, showMore.value ? 20 : 5) || [])
const hasSnapshot = computed(() => groups.value.some(group => group.fetched_at) || Boolean(data.value?.incidents_fetched_at))
const stale = computed(() => failed.value || data.value?.incidents_stale || groups.value.some(group => group.stale))
const dateFormatter = computed(() => new Intl.DateTimeFormat(locale.value || undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }))
const knownStatuses = new Set(['operational', 'degraded_performance', 'partial_outage', 'major_outage', 'under_maintenance', 'investigating', 'identified', 'monitoring', 'resolved', 'postmortem', 'scheduled', 'in_progress', 'verifying', 'completed', 'unknown'])
let timer: number | undefined
let sequence = 0
let disposed = false
let activeSignature = ''
let pending: { controller: AbortController; promise: Promise<void> } | undefined

function statusLabel(status: string) {
  return t(`channelMonitorV2.officialStatus.states.${knownStatuses.has(status) ? status : 'unknown'}`)
}
function statusClasses(status: string) {
  if (['operational', 'resolved', 'postmortem', 'completed'].includes(status)) return { dot: 'bg-emerald-500', text: 'text-emerald-700 dark:text-emerald-400' }
  if (['major_outage', 'partial_outage'].includes(status)) return { dot: 'bg-rose-500', text: 'text-rose-600 dark:text-rose-400' }
  if (knownStatuses.has(status) && status !== 'unknown') return { dot: 'bg-amber-400', text: 'text-amber-700 dark:text-amber-300' }
  return { dot: 'bg-gray-400', text: 'text-gray-500 dark:text-gray-400' }
}
function formatTime(value: string) {
  const time = new Date(value)
  return Number.isFinite(time.getTime()) ? dateFormatter.value.format(time) : '—'
}
function load(): Promise<void> {
  if (disposed || document.hidden) return Promise.resolve()
  if (pending) return pending.promise
  const controller = new AbortController()
  const id = ++sequence
  loading.value = true
  const promise = (async () => {
    try {
      const response = await getOfficialStatus(controller.signal)
      if (disposed || id !== sequence) return
      data.value = response
      failed.value = false
      const signature = [
        ...response.incidents.filter(incident => !response.incidents_stale && ['investigating', 'identified', 'monitoring', 'in_progress', 'verifying'].includes(incident.status)).map(incident => incident.id),
        ...response.groups.filter(group => !group.stale && ['degraded_performance', 'partial_outage', 'major_outage', 'under_maintenance'].includes(group.status)).map(group => `${group.id}:${group.status}`),
      ].sort().join(',')
      if (signature && signature !== activeSignature) expanded.value = true
      activeSignature = signature
    } catch {
      if (disposed || id !== sequence || controller.signal.aborted) return
      failed.value = true
    } finally {
      if (!disposed && id === sequence) {
        pending = undefined
        loading.value = false
      }
    }
  })()
  pending = { controller, promise }
  return promise
}
function visibilityChanged() {
  if (timer !== undefined) window.clearInterval(timer)
  timer = undefined
  if (document.hidden) {
    ++sequence
    pending?.controller.abort()
    pending = undefined
    loading.value = false
    return
  }
  void load()
  timer = window.setInterval(() => { void load() }, 120000)
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
