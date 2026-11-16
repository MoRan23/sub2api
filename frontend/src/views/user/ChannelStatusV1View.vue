<template>
  <AppLayout>
    <MonitorHero
      :overall-status="overallStatus"
      :interval-seconds="DEFAULT_INTERVAL_SECONDS"
      :window="currentWindow"
      :loading="loading"
      :auto-refresh="autoRefresh"
      @update:window="handleWindowChange"
      @refresh="refresh()"
    />

    <MonitorCardGrid
      :items="items"
      :window="currentWindow"
      :countdown-seconds="countdown"
      :loading="loading"
      :detail-cache="detailCache"
      @card-click="openDetail"
    />

    <MonitorDetailDialog
      :show="showDetail"
      :monitor-id="detailTarget?.id ?? null"
      :title="detailTitle"
      @close="closeDetail"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  list as listChannelMonitorViews,
  status as fetchChannelMonitorDetail,
  type UserMonitorView,
  type UserMonitorDetail,
} from '@/api/channelMonitor'
import AppLayout from '@/components/layout/AppLayout.vue'
import MonitorHero, {
  type MonitorWindow,
  type OverallStatus,
} from '@/components/user/monitor/MonitorHero.vue'
import MonitorCardGrid from '@/components/user/monitor/MonitorCardGrid.vue'
import MonitorDetailDialog from '@/components/user/MonitorDetailDialog.vue'
import { DEFAULT_INTERVAL_SECONDS, STATUS_OPERATIONAL } from '@/constants/channelMonitor'
import { useAutoRefresh } from '@/composables/useAutoRefresh'

const { t } = useI18n()
const appStore = useAppStore()

// ── State ──
const items = ref<UserMonitorView[]>([])
const loading = ref(false)
const currentWindow = ref<MonitorWindow>('7d')
const detailCache = reactive<Record<number, UserMonitorDetail>>({})
const showDetail = ref(false)
const detailTarget = ref<UserMonitorView | null>(null)

let abortController: AbortController | null = null
let inFlight: { window: MonitorWindow; promise: Promise<void> } | null = null

const autoRefresh = useAutoRefresh({
  storageKey: 'channel-status-auto-refresh',
  intervals: [30, 60, 120] as const,
  defaultInterval: DEFAULT_INTERVAL_SECONDS,
  defaultEnabled: true,
  onRefresh: () => refresh(true),
  shouldPause: () => document.hidden || loading.value,
})
const countdown = autoRefresh.countdown

// ── Computed ──
const overallStatus = computed<OverallStatus>(() => {
  if (items.value.length === 0) return 'operational'
  for (const it of items.value) {
    if (it.primary_status === 'failed' || it.primary_status === 'error') return 'degraded'
    if (it.primary_status !== STATUS_OPERATIONAL) return 'degraded'
  }
  return 'operational'
})

const detailTitle = computed(() => {
  return detailTarget.value?.name || t('channelStatus.detailTitle')
})

// ── Loaders ──
function reload(silent = false): Promise<void> {
  const selectedWindow = currentWindow.value
  if (inFlight?.window === selectedWindow) return inFlight.promise
  const promise = loadWindow(selectedWindow, silent).finally(() => {
    if (inFlight?.promise === promise) inFlight = null
  })
  inFlight = { window: selectedWindow, promise }
  return promise
}

async function loadWindow(selectedWindow: MonitorWindow, silent: boolean) {
  if (abortController) abortController.abort()
  const ctrl = new AbortController()
  abortController = ctrl
  if (!silent) loading.value = true
  try {
    const res = await listChannelMonitorViews({ signal: ctrl.signal, window: selectedWindow })
    if (ctrl.signal.aborted || abortController !== ctrl) return
    items.value = res.items || []
    await loadLegacyDetails(items.value, selectedWindow, ctrl)
  } catch (err: unknown) {
    if (ctrl.signal.aborted || abortController !== ctrl) return
    const e = err as { name?: string; code?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    appStore.showError(extractApiErrorMessage(err, t('channelStatus.loadError')))
  } finally {
    if (abortController === ctrl) {
      if (!silent) loading.value = false
      autoRefresh.resetCountdown()
      abortController = null
    }
  }
}

async function refresh(silent = false) {
  await reload(silent)
}

// Compatibility with an older server: at most four detail requests in flight.
// The current server returns the selected window in the list response.
async function loadLegacyDetails(rows: UserMonitorView[], window: MonitorWindow, ctrl: AbortController) {
  if (window === '7d') return
  const field = window === '15d' ? 'availability_15d' : 'availability_30d'
  const ids = [...new Set(rows.filter(row => row[field] === undefined).map(row => row.id))]
  let next = 0
  let failed = false
  const worker = async () => {
    while (!ctrl.signal.aborted && abortController === ctrl && next < ids.length) {
      const id = ids[next++]
      try {
        const detail = await fetchChannelMonitorDetail(id, { signal: ctrl.signal })
        if (!ctrl.signal.aborted && abortController === ctrl) detailCache[id] = detail
      } catch (err: unknown) {
        if (ctrl.signal.aborted || abortController !== ctrl || failed) continue
        failed = true
        appStore.showError(extractApiErrorMessage(err, t('channelStatus.detailLoadError')))
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(4, ids.length) }, worker))
}

// ── Handlers ──
async function handleWindowChange(value: MonitorWindow) {
  currentWindow.value = value
  await reload()
}

function openDetail(row: UserMonitorView) {
  detailTarget.value = row
  showDetail.value = true
}

function closeDetail() {
  showDetail.value = false
  detailTarget.value = null
}

watch(
  () => appStore.cachedPublicSettings?.channel_monitor_enabled,
  (enabled) => {
    if (enabled === false) autoRefresh.stop()
    else if (autoRefresh.enabled.value) autoRefresh.start()
  },
)

onMounted(() => {
  void reload(false)
  if (appStore.cachedPublicSettings?.channel_monitor_enabled !== false) {
    autoRefresh.setEnabled(autoRefresh.enabled.value)
  }
})

onBeforeUnmount(() => {
  if (abortController) abortController.abort()
})
</script>
