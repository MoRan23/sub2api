<template>
  <AppLayout>
    <TablePageLayout>
      <!-- Header / status -->
      <template #filters>
        <div class="card p-4 sm:p-6">
          <div class="flex flex-wrap items-center justify-between gap-4">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.installationObservation.title') }}
                </h2>
                <span :class="statusBadgeClass">
                  <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass"></span>
                  {{ enabled ? t('admin.installationObservation.statusOn') : t('admin.installationObservation.statusOff') }}
                </span>
              </div>
              <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.installationObservation.subtitle') }}
              </p>
            </div>

            <div class="flex flex-wrap items-center gap-3">
              <label class="flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400">
                <input v-model="autoRefresh" type="checkbox" class="h-4 w-4 rounded border-gray-300" />
                {{ t('admin.installationObservation.autoRefresh') }}
              </label>
              <button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">
                <Icon name="refresh" size="sm" class="mr-1.5" />
                {{ t('common.refresh') }}
              </button>
            </div>
          </div>

          <!-- Disabled hint -->
          <div
            v-if="!enabled"
            class="mt-4 flex items-start gap-2 rounded-xl bg-amber-50 p-3 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          >
            <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" />
            <span>{{ t('admin.installationObservation.disabledHint') }}</span>
          </div>
        </div>
      </template>

      <!-- Table -->
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
            <div class="flex flex-col gap-1">
              <span :class="pinBadgeClass(row.pinned)">
                {{ row.pinned ? t('admin.installationObservation.modePinned') : t('admin.installationObservation.modePassthrough') }}
              </span>
            </div>
          </template>

          <template #cell-installation="{ row }">
            <div class="min-w-0 max-w-[280px] space-y-1">
              <div class="truncate font-mono text-xs text-gray-800 dark:text-gray-200" :title="row.outbound_installation_id">
                <span class="text-gray-400">{{ t('admin.installationObservation.outboundShort') }}:</span>
                {{ row.outbound_installation_id || '—' }}
              </div>
              <div
                class="truncate font-mono text-xs"
                :class="installationDiffClass(row)"
                :title="row.client_reported_installation_id"
              >
                <span class="text-gray-400">{{ t('admin.installationObservation.clientShort') }}:</span>
                {{ row.client_reported_installation_id || '—' }}
              </div>
            </div>
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
                {{ enabled ? t('admin.installationObservation.emptyOn') : t('admin.installationObservation.emptyOff') }}
              </p>
            </div>
          </template>
        </DataTable>
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI, type InstallationObservationEntry } from '@/api/admin'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

// The ring buffer has no stable per-entry id, so we derive a row key from the
// entry position + timestamp for DataTable's :row-key.
type ObservationRow = InstallationObservationEntry & { rowKey: string }

const loading = ref(false)
const enabled = ref(false)
const entries = ref<ObservationRow[]>([])
const autoRefresh = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const columns = computed<Column[]>(() => [
  { key: 'timestamp', label: t('admin.installationObservation.columns.time') },
  { key: 'account', label: t('admin.installationObservation.columns.account') },
  { key: 'mode', label: t('admin.installationObservation.columns.mode') },
  { key: 'installation', label: t('admin.installationObservation.columns.installation') },
  { key: 'identity', label: t('admin.installationObservation.columns.identity') },
  { key: 'inbound_endpoint', label: t('admin.installationObservation.columns.endpoint') }
])

const statusBadgeClass = computed(() => {
  const base = 'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold '
  return enabled.value
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
})

const statusDotClass = computed(() => (enabled.value ? 'bg-green-500' : 'bg-gray-400'))

function pinBadgeClass(pinned: boolean): string {
  const base = 'inline-flex w-fit items-center rounded-full px-2 py-0.5 text-xs font-semibold '
  return pinned
    ? base + 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : base + 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

// Highlight the client-reported id in amber when it differs from the outbound
// (pinned) value — that's exactly the leak the pin suppresses.
function installationDiffClass(row: ObservationRow): string {
  if (!row.pinned) return 'text-gray-500 dark:text-gray-400'
  const differs = !!row.client_reported_installation_id &&
    row.client_reported_installation_id !== row.outbound_installation_id
  return differs ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

async function refresh() {
  loading.value = true
  try {
    const res = await adminAPI.installationObservations.list()
    enabled.value = res.enabled
    entries.value = (res.entries || []).map((e, i) => ({
      ...e,
      rowKey: `${e.timestamp}-${e.account_id}-${i}`
    }))
  } finally {
    loading.value = false
  }
}

watch(autoRefresh, (on) => {
  if (on) {
    timer = setInterval(refresh, 5000)
  } else if (timer) {
    clearInterval(timer)
    timer = null
  }
})

onMounted(refresh)
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>
