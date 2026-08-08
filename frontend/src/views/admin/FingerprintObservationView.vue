<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="card p-4 sm:p-6">
          <div class="flex flex-wrap items-center justify-between gap-4">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.fingerprintObservation.title') }}
                </h2>
                <span :class="statusBadgeClass">
                  <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass"></span>
                  {{ observationEnabled ? t('admin.fingerprintObservation.statusOn') : t('admin.fingerprintObservation.statusOff') }}
                </span>
              </div>
              <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.fingerprintObservation.subtitle') }}
              </p>
            </div>

            <div class="flex flex-wrap items-center gap-3">
              <div class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
                <span>{{ t('admin.fingerprintObservation.toggleLabel') }}</span>
                <Toggle
                  :model-value="observationEnabled"
                  :disabled="toggling"
                  :aria-label="t('admin.fingerprintObservation.toggleLabel')"
                  @update:model-value="setObservationEnabled"
                />
              </div>

              <button
                type="button"
                class="h-8 w-8 rounded-lg flex items-center justify-center text-gray-500 hover:text-gray-700 hover:bg-gray-100 dark:text-gray-400 dark:hover:text-gray-200 dark:hover:bg-dark-700 transition-colors disabled:opacity-50"
                :disabled="loading || toggling"
                :aria-label="t('common.refresh')"
                :title="t('common.refresh')"
                @click="refresh()"
              >
                <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
              </button>

              <AutoRefreshButton
                :enabled="autoRefresh.enabled.value"
                :interval-seconds="autoRefresh.intervalSeconds.value"
                :countdown="autoRefresh.countdown.value"
                :intervals="autoRefresh.intervals"
                @update:enabled="autoRefresh.setEnabled"
                @update:interval="autoRefresh.setInterval"
              />
            </div>
          </div>

          <div
            v-if="!observationEnabled"
            class="mt-4 flex items-start gap-2 rounded-xl bg-amber-50 p-3 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          >
            <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" />
            <span>{{ t('admin.fingerprintObservation.disabledHint') }}</span>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="entries" :loading="loading" row-key="rowKey">
          <template #cell-timestamp="{ value }">
            <span class="whitespace-nowrap text-gray-600 dark:text-gray-300">{{ formatTime(value) }}</span>
          </template>

          <template #cell-account="{ row }">
            <div class="min-w-0 max-w-[200px]">
              <div class="truncate font-medium text-gray-900 dark:text-white" :title="row.account_name">
                {{ row.account_name || '—' }}
              </div>
              <div class="mt-0.5 truncate text-xs text-gray-400">#{{ row.account_id }}</div>
            </div>
          </template>

          <template #cell-mode="{ row }">
            <span :class="pinBadgeClass(row.pinned)">
              {{ row.pinned ? t('admin.fingerprintObservation.modePinned') : t('admin.fingerprintObservation.modePassthrough') }}
            </span>
          </template>

          <template #cell-installation="{ row }">
            <div class="min-w-0 max-w-[280px] space-y-1">
              <div
                class="truncate font-mono text-xs text-gray-800 dark:text-gray-200"
                :title="row.outbound_installation_id"
              >
                <span class="text-gray-400">{{ t('admin.fingerprintObservation.outboundShort') }}:</span>
                {{ row.outbound_installation_id || '—' }}
              </div>
              <div
                class="truncate font-mono text-xs"
                :class="installationDiffClass(row)"
                :title="row.client_reported_installation_id"
              >
                <span class="text-gray-400">{{ t('admin.fingerprintObservation.clientShort') }}:</span>
                {{ row.client_reported_installation_id || '—' }}
              </div>
            </div>
          </template>

          <template #cell-session_id="{ value }">
            <span class="block max-w-[240px] truncate font-mono text-xs text-gray-600 dark:text-gray-300" :title="value">
              {{ value || '—' }}
            </span>
          </template>

          <template #cell-thread_id="{ value }">
            <span class="block max-w-[240px] truncate font-mono text-xs text-gray-600 dark:text-gray-300" :title="value">
              {{ value || '—' }}
            </span>
          </template>

          <template #cell-identity="{ row }">
            <div class="min-w-0 max-w-[320px] space-y-0.5 text-xs">
              <div class="truncate font-mono text-gray-700 dark:text-gray-300" :title="row.user_agent">
                {{ row.user_agent || '—' }}
              </div>
              <div class="truncate text-gray-400">
                <span class="font-mono">{{ row.originator || '—' }}</span>
                <span v-if="row.version"> · v{{ row.version }}</span>
                <span v-if="row.openai_beta"> · {{ row.openai_beta }}</span>
              </div>
            </div>
          </template>

          <template #cell-inbound_endpoint="{ value }">
            <span class="whitespace-nowrap font-mono text-xs text-gray-500 dark:text-gray-400">{{ value || '—' }}</span>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-8">
              <Icon name="eye" size="xl" class="mb-4 h-12 w-12 text-gray-300 dark:text-dark-600" />
              <p class="text-sm font-medium text-gray-500 dark:text-gray-400">
                {{ observationEnabled ? t('admin.fingerprintObservation.emptyOn') : t('admin.fingerprintObservation.emptyOff') }}
              </p>
            </div>
          </template>
        </DataTable>
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI, type FingerprintObservationEntry } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { Column } from '@/components/common/types'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

// The observer has no durable row id, so derive a stable key from its position
// and request timestamp for DataTable's row-key resolver.
type FingerprintRow = FingerprintObservationEntry & { rowKey: string }

const loading = ref(false)
const toggling = ref(false)
const observationEnabled = ref(false)
const entries = ref<FingerprintRow[]>([])
let refreshPromise: Promise<void> | null = null
// A settings toggle invalidates any poll that started before it. Without a
// generation guard, a late auto-refresh response can overwrite the optimistic
// toggle state (and repopulate rows after a successful disable).
let refreshGeneration = 0

const autoRefresh = useAutoRefresh({
  storageKey: 'admin-fingerprint-observation-auto-refresh',
  intervals: [5, 10, 15, 30] as const,
  defaultInterval: 5,
  onRefresh: () => refresh(true),
  shouldPause: () => typeof document !== 'undefined' && document.hidden,
})

const columns = computed<Column[]>(() => [
  { key: 'timestamp', label: t('admin.fingerprintObservation.columns.time') },
  { key: 'account', label: t('admin.fingerprintObservation.columns.account') },
  { key: 'mode', label: t('admin.fingerprintObservation.columns.mode') },
  { key: 'installation', label: t('admin.fingerprintObservation.columns.installation') },
  { key: 'session_id', label: t('admin.fingerprintObservation.columns.sessionId') },
  { key: 'thread_id', label: t('admin.fingerprintObservation.columns.threadId') },
  { key: 'identity', label: t('admin.fingerprintObservation.columns.identity') },
  { key: 'inbound_endpoint', label: t('admin.fingerprintObservation.columns.endpoint') },
])

const statusBadgeClass = computed(() => {
  const base = 'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold '
  return observationEnabled.value
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
})

const statusDotClass = computed(() => (observationEnabled.value ? 'bg-green-500' : 'bg-gray-400'))

function pinBadgeClass(pinned: boolean): string {
  const base = 'inline-flex w-fit items-center rounded-full px-2 py-0.5 text-xs font-semibold '
  return pinned
    ? base + 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : base + 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

function installationDiffClass(row: FingerprintRow): string {
  if (!row.pinned) return 'text-gray-500 dark:text-gray-400'
  const differs = !!row.client_reported_installation_id &&
    row.client_reported_installation_id !== row.outbound_installation_id
  return differs ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'
}

function formatTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString()
}

async function refresh(silent = false, allowDuringToggle = false): Promise<void> {
  if (refreshPromise) return refreshPromise

  const generation = refreshGeneration

  refreshPromise = (async () => {
    if (!silent) loading.value = true
    try {
      const response = await adminAPI.fingerprintObservations.list()
      // Ignore snapshots that raced a settings mutation. The mutation waits
      // for this promise and then performs a fresh read in its own generation.
      if (generation === refreshGeneration && (allowDuringToggle || !toggling.value)) {
        observationEnabled.value = response.enabled
        entries.value = response.enabled
          ? (response.entries || []).map((entry, index) => ({
              ...entry,
              rowKey: `${entry.timestamp}-${entry.account_id}-${index}`,
            }))
          : []
      }
    } catch (error: unknown) {
      if (generation === refreshGeneration && (allowDuringToggle || !toggling.value)) {
        appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.loadFailed')))
      }
    } finally {
      if (!silent) loading.value = false
      autoRefresh.resetCountdown()
      refreshPromise = null
    }
  })()

  return refreshPromise
}

async function setObservationEnabled(value: boolean): Promise<void> {
  if (toggling.value || value === observationEnabled.value) return

  const previous = observationEnabled.value
  const mutationGeneration = ++refreshGeneration
  observationEnabled.value = value
  toggling.value = true
  try {
    const settings = await adminAPI.settings.updateSettings({
      installation_observation_enabled: value,
    })
    observationEnabled.value = settings?.installation_observation_enabled ?? value
    if (!observationEnabled.value) entries.value = []

    // A poll may have been in flight when the toggle was clicked. Wait for it
    // to settle (its result is generation-invalidated) before issuing the
    // authoritative post-write snapshot.
    if (refreshPromise) await refreshPromise
    if (mutationGeneration !== refreshGeneration) return
    await refresh(false, true)
    appStore.showSuccess(
      observationEnabled.value
        ? t('admin.fingerprintObservation.enabledSuccess')
        : t('admin.fingerprintObservation.disabledSuccess')
    )
  } catch (error: unknown) {
    observationEnabled.value = previous
    appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.toggleFailed')))
  } finally {
    toggling.value = false
  }
}

onMounted(async () => {
  await refresh()
  // useAutoRefresh restores the user's preference from localStorage but does
  // not start a timer until explicitly requested.
  if (autoRefresh.enabled.value) {
    autoRefresh.resetCountdown()
    autoRefresh.start()
  }
})
</script>
