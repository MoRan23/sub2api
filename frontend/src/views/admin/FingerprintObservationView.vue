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
                  {{
                    observationEnabled
                      ? t('admin.fingerprintObservation.statusOn')
                      : t('admin.fingerprintObservation.statusOff')
                  }}
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
                class="flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 disabled:opacity-50 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                :disabled="toggling"
                :aria-label="t('common.refresh')"
                :title="t('common.refresh')"
                @click="manualRefresh"
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
            class="mt-4 flex items-start gap-2 rounded-lg bg-amber-50 p-3 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          >
            <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" />
            <span>{{ t('admin.fingerprintObservation.disabledHint') }}</span>
          </div>
        </div>
      </template>

      <template #table>
        <div class="flex h-full min-h-[320px] flex-col">
          <div
            class="grid shrink-0 grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 border-b border-gray-200 bg-gray-50/80 px-4 py-3 text-xs font-semibold text-gray-500 dark:border-dark-700 dark:bg-dark-800/80 dark:text-gray-400"
          >
            <span></span>
            <span>{{ t('admin.fingerprintObservation.columns.actor') }}</span>
            <span>{{ t('admin.fingerprintObservation.columns.session') }}</span>
            <span>{{ t('admin.fingerprintObservation.columns.activity') }}</span>
          </div>

          <div class="min-h-0 flex-1 overflow-auto">
            <div v-if="loading && sessions.length === 0" class="flex h-full min-h-72 items-center justify-center">
              <div class="flex flex-col items-center gap-3 text-sm text-gray-500 dark:text-gray-400">
                <Icon name="refresh" size="lg" class="animate-spin" />
                <span>{{ t('common.loading') }}</span>
              </div>
            </div>

            <ul v-else-if="userGroups.length > 0" class="divide-y divide-gray-200 dark:divide-dark-700">
              <li v-for="(user, userIndex) in userGroups" :key="user.key" class="bg-white dark:bg-dark-800">
                <div
                  class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 px-4 py-3.5"
                >
                  <button
                    type="button"
                    class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                    :aria-expanded="isUserExpanded(user)"
                    :aria-controls="userPanelId(userIndex)"
                    :aria-label="userToggleLabel(user)"
                    :title="userToggleLabel(user)"
                    @click="toggleUser(user)"
                  >
                    <Icon :name="isUserExpanded(user) ? 'chevronDown' : 'chevronRight'" size="sm" />
                  </button>

                  <div class="min-w-0 space-y-1">
                    <div class="flex min-w-0 items-center gap-1.5 text-sm font-semibold text-gray-900 dark:text-white">
                      <Icon name="user" size="xs" class="shrink-0 text-gray-400" />
                      <span class="truncate" :title="user.email || user.username">
                        {{ userGroupLabel(user) }}
                      </span>
                      <span v-if="user.userID > 0" class="shrink-0 text-xs font-normal text-gray-400">
                        #{{ user.userID }}
                      </span>
                    </div>
                    <div
                      v-if="user.email && user.email !== userGroupLabel(user)"
                      class="truncate pl-5 text-xs text-gray-500 dark:text-gray-400"
                      :title="user.email"
                    >
                      {{ user.email }}
                    </div>
                  </div>

                  <div class="flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 text-xs text-gray-500 dark:text-gray-400">
                    <span>{{ t('admin.fingerprintObservation.apiKeyCount', { count: user.apiKeyCount }) }}</span>
                    <span>{{ t('admin.fingerprintObservation.sessionCount', { count: user.sessionCount }) }}</span>
                  </div>

                  <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
                    <div class="flex flex-wrap gap-x-3 gap-y-0.5">
                      <span>{{ t('admin.fingerprintObservation.threadCount', { count: user.threadCount }) }}</span>
                      <span>{{ t('admin.fingerprintObservation.observationCount', { count: user.observationCount }) }}</span>
                    </div>
                    <div class="mt-1 truncate" :title="formatTime(user.lastObservedAt)">
                      {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(user.lastObservedAt) }}
                    </div>
                  </div>
                </div>

                <div
                  v-if="isUserExpanded(user)"
                  :id="userPanelId(userIndex)"
                  class="border-t border-gray-100 bg-gray-50/65 dark:border-dark-700 dark:bg-dark-900/25"
                >
                  <div
                    v-for="(apiKey, apiKeyIndex) in user.apiKeys"
                    :key="apiKey.key"
                    class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                  >
                    <div
                      class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 py-3 pl-9 pr-4"
                    >
                      <button
                        type="button"
                        class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-200/70 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                        :aria-expanded="isAPIKeyExpanded(apiKey)"
                        :aria-controls="apiKeyPanelId(userIndex, apiKeyIndex)"
                        :aria-label="apiKeyToggleLabel(apiKey)"
                        :title="apiKeyToggleLabel(apiKey)"
                        @click="toggleAPIKey(apiKey)"
                      >
                        <Icon :name="isAPIKeyExpanded(apiKey) ? 'chevronDown' : 'chevronRight'" size="sm" />
                      </button>

                      <div class="flex min-w-0 items-center gap-1.5 text-sm font-medium text-gray-800 dark:text-gray-100">
                        <Icon name="key" size="xs" class="shrink-0 text-gray-400" />
                        <span class="truncate" :title="apiKey.apiKeyName">{{ apiKeyGroupLabel(apiKey) }}</span>
                        <span v-if="apiKey.apiKeyID > 0" class="shrink-0 text-xs font-normal text-gray-400">
                          #{{ apiKey.apiKeyID }}
                        </span>
                      </div>

                      <div class="text-xs text-gray-500 dark:text-gray-400">
                        {{ t('admin.fingerprintObservation.sessionCount', { count: apiKey.sessionCount }) }}
                      </div>

                      <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
                        <div class="flex flex-wrap gap-x-3 gap-y-0.5">
                          <span>{{ t('admin.fingerprintObservation.threadCount', { count: apiKey.threadCount }) }}</span>
                          <span>{{ t('admin.fingerprintObservation.observationCount', { count: apiKey.observationCount }) }}</span>
                        </div>
                        <div class="mt-1 truncate" :title="formatTime(apiKey.lastObservedAt)">
                          {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(apiKey.lastObservedAt) }}
                        </div>
                      </div>
                    </div>

                    <div
                      v-if="isAPIKeyExpanded(apiKey)"
                      :id="apiKeyPanelId(userIndex, apiKeyIndex)"
                      class="border-t border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-800"
                    >
                      <div
                        v-for="(session, sessionIndex) in apiKey.sessions"
                        :key="sessionKey(session)"
                        class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                      >
                        <div
                          class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 py-3 pl-14 pr-4"
                        >
                          <button
                            type="button"
                            class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                            :aria-expanded="isSessionExpanded(session)"
                            :aria-controls="sessionPanelId(userIndex, apiKeyIndex, sessionIndex)"
                            :aria-label="sessionToggleLabel(session)"
                            :title="sessionToggleLabel(session)"
                            @click="toggleSession(session)"
                          >
                            <Icon :name="isSessionExpanded(session) ? 'chevronDown' : 'chevronRight'" size="sm" />
                          </button>

                          <div class="min-w-0">
                            <span class="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-[11px] font-semibold text-green-700 dark:bg-green-900/30 dark:text-green-300">
                              {{ t('admin.fingerprintObservation.rootSession') }}
                            </span>
                            <div class="mt-1 truncate text-[11px] text-gray-400" :title="formatTime(session.first_observed_at)">
                              {{ formatTime(session.first_observed_at) }}
                            </div>
                          </div>

                          <div class="min-w-0">
                            <div class="text-[11px] font-semibold text-gray-400">
                              {{ t('admin.fingerprintObservation.sessionId') }}
                            </div>
                            <span
                              class="block max-w-full truncate font-mono text-xs text-gray-700 dark:text-gray-300"
                              :title="session.session_id"
                            >
                              {{ session.session_id || '—' }}
                            </span>
                          </div>

                          <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
                            <div class="flex flex-wrap gap-x-3 gap-y-0.5">
                              <span>{{ t('admin.fingerprintObservation.threadCount', { count: sessionThreadCount(session) }) }}</span>
                              <span>{{ t('admin.fingerprintObservation.observationCount', { count: session.observation_count }) }}</span>
                            </div>
                            <div class="mt-1 truncate" :title="formatTime(session.last_observed_at)">
                              {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(session.last_observed_at) }}
                            </div>
                          </div>
                        </div>

                        <div
                          v-if="isSessionExpanded(session)"
                          :id="sessionPanelId(userIndex, apiKeyIndex, sessionIndex)"
                          class="border-t border-gray-100 bg-gray-50/60 dark:border-dark-700 dark:bg-dark-900/35"
                        >
                          <div
                            v-for="(thread, threadIndex) in displayThreads(session)"
                            :key="threadKey(session, thread)"
                            class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                          >
                            <div
                              class="grid grid-cols-[2rem_minmax(0,1fr)_minmax(0,1.35fr)_auto] items-center gap-3 py-3 pl-20 pr-4"
                            >
                              <button
                                type="button"
                                class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-200/70 hover:text-gray-700 disabled:cursor-default disabled:opacity-40 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                                :disabled="thread.observations.length === 0"
                                :aria-expanded="isThreadExpanded(session, thread)"
                                :aria-controls="threadPanelId(userIndex, apiKeyIndex, sessionIndex, threadIndex)"
                                :aria-label="threadToggleLabel(session, thread)"
                                :title="threadToggleLabel(session, thread)"
                                @click="toggleThread(session, thread)"
                              >
                                <Icon :name="isThreadExpanded(session, thread) ? 'chevronDown' : 'chevronRight'" size="sm" />
                              </button>

                              <div class="min-w-0">
                                <div class="flex flex-wrap items-center gap-2">
                                  <span :class="threadRelationClass(thread)">
                                    {{ threadRelationLabel(thread) }}
                                  </span>
                                  <span class="text-xs text-gray-500 dark:text-gray-400">
                                    {{ t('admin.fingerprintObservation.observationCount', { count: thread.observation_count }) }}
                                  </span>
                                </div>
                                <span
                                  v-if="!isUnthreaded(thread)"
                                  class="mt-1 block max-w-full truncate font-mono text-xs text-gray-700 dark:text-gray-300"
                                  :title="thread.thread_id"
                                >
                                  {{ thread.thread_id }}
                                </span>
                              </div>

                              <div class="min-w-0 space-y-1 text-xs text-gray-500 dark:text-gray-400">
                                <div v-if="thread.parent_thread_id" class="flex min-w-0 gap-2">
                                  <span class="shrink-0">{{ t('admin.fingerprintObservation.parentThread') }}</span>
                                  <span class="truncate font-mono" :title="thread.parent_thread_id">
                                    {{ thread.parent_thread_id }}
                                  </span>
                                </div>
                                <div v-if="thread.forked_from_thread_id" class="flex min-w-0 gap-2">
                                  <span class="shrink-0">{{ t('admin.fingerprintObservation.forkedFrom') }}</span>
                                  <span class="truncate font-mono" :title="thread.forked_from_thread_id">
                                    {{ thread.forked_from_thread_id }}
                                  </span>
                                </div>
                                <span v-if="!thread.parent_thread_id && !thread.forked_from_thread_id">—</span>
                              </div>

                              <div class="whitespace-nowrap text-right text-xs text-gray-400">
                                {{ formatTime(thread.last_observed_at) }}
                              </div>
                            </div>

                            <div
                              v-if="isThreadExpanded(session, thread)"
                              :id="threadPanelId(userIndex, apiKeyIndex, sessionIndex, threadIndex)"
                              class="overflow-x-auto border-t border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-800"
                            >
                              <table class="w-full min-w-[1120px] table-fixed">
                                <thead>
                                  <tr class="bg-gray-50/70 dark:bg-dark-800/80">
                                    <th class="w-40">{{ t('admin.fingerprintObservation.columns.time') }}</th>
                                    <th class="w-44">{{ t('admin.fingerprintObservation.columns.account') }}</th>
                                    <th class="w-28">{{ t('admin.fingerprintObservation.columns.mode') }}</th>
                                    <th class="w-60">{{ t('admin.fingerprintObservation.columns.installation') }}</th>
                                    <th class="w-64">{{ t('admin.fingerprintObservation.columns.wireIdentity') }}</th>
                                    <th class="w-72">{{ t('admin.fingerprintObservation.columns.client') }}</th>
                                    <th class="w-52">{{ t('admin.fingerprintObservation.columns.endpoint') }}</th>
                                  </tr>
                                </thead>
                                <tbody>
                                  <tr
                                    v-for="observation in thread.observations"
                                    :key="observation.sequence_id"
                                    class="align-top"
                                  >
                                    <td class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
                                      {{ formatTime(observation.timestamp) }}
                                    </td>
                                    <td>
                                      <div class="truncate text-sm font-medium text-gray-900 dark:text-white" :title="observation.account_name">
                                        {{ observation.account_name || '—' }}
                                      </div>
                                      <div class="mt-0.5 text-xs text-gray-400">#{{ observation.account_id }}</div>
                                    </td>
                                    <td>
                                      <span :class="pinBadgeClass(observation.pinned)">
                                        {{
                                          observation.pinned
                                            ? t('admin.fingerprintObservation.modePinned')
                                            : t('admin.fingerprintObservation.modePassthrough')
                                        }}
                                      </span>
                                    </td>
                                    <td>
                                      <div class="min-w-0 space-y-1">
                                        <div class="truncate font-mono text-xs text-gray-700 dark:text-gray-300" :title="observation.outbound_installation_id">
                                          <span class="text-gray-400">{{ t('admin.fingerprintObservation.outboundShort') }}:</span>
                                          {{ observation.outbound_installation_id || '—' }}
                                        </div>
                                        <div
                                          class="truncate font-mono text-xs"
                                          :class="installationDiffClass(observation)"
                                          :title="observation.client_reported_installation_id"
                                        >
                                          <span class="text-gray-400">{{ t('admin.fingerprintObservation.clientShort') }}:</span>
                                          {{ observation.client_reported_installation_id || '—' }}
                                        </div>
                                      </div>
                                    </td>
                                    <td>
                                      <div class="min-w-0 space-y-1 text-xs">
                                        <div class="flex min-w-0 gap-1.5">
                                          <span class="shrink-0 text-gray-400">S</span>
                                          <span class="truncate font-mono text-gray-700 dark:text-gray-300" :title="observation.session_id">
                                            {{ observation.session_id || '—' }}
                                          </span>
                                        </div>
                                        <div class="flex min-w-0 gap-1.5">
                                          <span class="shrink-0 text-gray-400">T</span>
                                          <span class="truncate font-mono text-gray-700 dark:text-gray-300" :title="observation.thread_id">
                                            {{ observation.thread_id || '—' }}
                                          </span>
                                        </div>
                                      </div>
                                    </td>
                                    <td>
                                      <div class="min-w-0 space-y-1 text-xs">
                                        <div class="truncate font-mono text-gray-700 dark:text-gray-300" :title="observation.user_agent">
                                          {{ observation.user_agent || '—' }}
                                        </div>
                                        <div class="truncate text-gray-400">
                                          <span class="font-mono">{{ observation.originator || '—' }}</span>
                                          <span v-if="observation.version"> · v{{ observation.version }}</span>
                                          <span v-if="observation.openai_beta"> · {{ observation.openai_beta }}</span>
                                        </div>
                                      </div>
                                    </td>
                                    <td>
                                      <span class="block truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="observation.inbound_endpoint">
                                        {{ observation.inbound_endpoint || '—' }}
                                      </span>
                                    </td>
                                  </tr>
                                </tbody>
                              </table>
                            </div>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </li>
            </ul>

            <div v-else class="flex h-full min-h-72 flex-col items-center justify-center px-4 text-center">
              <Icon name="eye" size="xl" class="mb-4 h-12 w-12 text-gray-300 dark:text-dark-600" />
              <p class="text-sm font-medium text-gray-500 dark:text-gray-400">
                {{
                  observationEnabled
                    ? t('admin.fingerprintObservation.emptyOn')
                    : t('admin.fingerprintObservation.emptyOff')
                }}
              </p>
            </div>
          </div>
        </div>
      </template>

      <template #pagination>
        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          @update:page="handlePageChange"
          @update:page-size="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  adminAPI,
  type FingerprintObservationEntry,
  type FingerprintObservationSessionNode,
  type FingerprintObservationThreadNode,
} from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import Pagination from '@/components/common/Pagination.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'

type DisplayThreadNode = FingerprintObservationThreadNode & {
  displayKind: 'thread' | 'unthreaded'
}

interface LoadSnapshotOptions {
  targetPage: number
  targetPageSize: number
  requestedSnapshotSeq: number
  silent?: boolean
  resetExpansion?: boolean
}

interface FingerprintAPIKeyGroup {
  key: string
  apiKeyID: number
  apiKeyName: string
  sessions: FingerprintObservationSessionNode[]
  sessionCount: number
  threadCount: number
  observationCount: number
  firstObservedAt: string
  lastObservedAt: string
}

interface FingerprintUserGroup {
  key: string
  userID: number
  username: string
  email: string
  apiKeys: FingerprintAPIKeyGroup[]
  apiKeyCount: number
  sessionCount: number
  threadCount: number
  observationCount: number
  firstObservedAt: string
  lastObservedAt: string
}

interface VisibleState {
  enabled: boolean
  sessions: FingerprintObservationSessionNode[]
  total: number
  page: number
  pageSize: number
  pages: number
  snapshotSeq: number
  expandedUsers: Set<string>
  expandedAPIKeys: Set<string>
  expandedSessions: Set<string>
  expandedThreads: Set<string>
}

const defaultPageSize = 20
const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const toggling = ref(false)
const observationEnabled = ref(false)
const sessions = ref<FingerprintObservationSessionNode[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(defaultPageSize)
const pages = ref(1)
const snapshotSeq = ref(0)
const expandedUsers = ref<Set<string>>(new Set())
const expandedAPIKeys = ref<Set<string>>(new Set())
const expandedSessions = ref<Set<string>>(new Set())
const expandedThreads = ref<Set<string>>(new Set())

let activeRequestController: AbortController | null = null
let activeRequestID = 0
let toggleGeneration = 0
let disposed = false

const autoRefresh = useAutoRefresh({
  storageKey: 'admin-fingerprint-observation-auto-refresh',
  intervals: [5, 10, 15, 30] as const,
  defaultInterval: 5,
  onRefresh: pollFirstPage,
  shouldPause: () =>
    (typeof document !== 'undefined' && document.hidden) ||
    page.value !== 1 ||
    loading.value ||
    toggling.value,
})

const statusBadgeClass = computed(() => {
  const base = 'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold '
  return observationEnabled.value
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
})

const statusDotClass = computed(() => (observationEnabled.value ? 'bg-green-500' : 'bg-gray-400'))
const userGroups = computed<FingerprintUserGroup[]>(() => groupSessionsByUser(sessions.value))

function isCanceledRequest(error: unknown, signal: AbortSignal): boolean {
  if (signal.aborted) return true
  if (!error || typeof error !== 'object') return false
  const candidate = error as { name?: string; code?: string }
  return candidate.name === 'AbortError' || candidate.code === 'ERR_CANCELED'
}

function clearVisibleData(clearExpansion = true): void {
  sessions.value = []
  total.value = 0
  page.value = 1
  pages.value = 1
  snapshotSeq.value = 0
  if (clearExpansion) {
    expandedUsers.value = new Set()
    expandedAPIKeys.value = new Set()
    expandedSessions.value = new Set()
    expandedThreads.value = new Set()
  }
}

function cancelActiveRequest(): void {
  activeRequestID += 1
  activeRequestController?.abort()
  activeRequestController = null
  loading.value = false
}

function sessionKey(session: FingerprintObservationSessionNode): string {
  const fallbackSequence =
    session.root_thread?.observations[0]?.sequence_id ??
    session.child_threads[0]?.observations[0]?.sequence_id ??
    session.unthreaded_observations[0]?.sequence_id ??
    session.first_observed_at
  return `${session.user_id}:${session.api_key_id}:${session.session_id || fallbackSequence}`
}

function userGroupKey(session: FingerprintObservationSessionNode): string {
  if (session.user_id > 0) return `user-id:${session.user_id}`
  const email = session.email.trim().toLowerCase()
  if (email) return `user-email:${email}`
  const username = session.username.trim().toLowerCase()
  return username ? `user-name:${username}` : 'user-unknown'
}

function apiKeyGroupKey(userKey: string, session: FingerprintObservationSessionNode): string {
  if (session.api_key_id > 0) return `${userKey}:api-key-id:${session.api_key_id}`
  const name = session.api_key_name.trim().toLowerCase()
  return name ? `${userKey}:api-key-name:${name}` : `${userKey}:api-key-unknown`
}

function earlierTimestamp(left: string, right: string): string {
  const leftTime = Date.parse(left)
  const rightTime = Date.parse(right)
  if (Number.isNaN(leftTime)) return right
  if (Number.isNaN(rightTime)) return left
  return leftTime <= rightTime ? left : right
}

function laterTimestamp(left: string, right: string): string {
  const leftTime = Date.parse(left)
  const rightTime = Date.parse(right)
  if (Number.isNaN(leftTime)) return right
  if (Number.isNaN(rightTime)) return left
  return leftTime >= rightTime ? left : right
}

function groupSessionsByUser(nextSessions: FingerprintObservationSessionNode[]): FingerprintUserGroup[] {
  const groups: FingerprintUserGroup[] = []
  const usersByKey = new Map<string, FingerprintUserGroup>()
  const apiKeysByUser = new Map<string, Map<string, FingerprintAPIKeyGroup>>()

  for (const session of nextSessions) {
    const userKey = userGroupKey(session)
    let user = usersByKey.get(userKey)
    if (!user) {
      user = {
        key: userKey,
        userID: session.user_id,
        username: session.username,
        email: session.email,
        apiKeys: [],
        apiKeyCount: 0,
        sessionCount: 0,
        threadCount: 0,
        observationCount: 0,
        firstObservedAt: session.first_observed_at,
        lastObservedAt: session.last_observed_at,
      }
      usersByKey.set(userKey, user)
      apiKeysByUser.set(userKey, new Map())
      groups.push(user)
    }

    const apiKey = apiKeyGroupKey(userKey, session)
    const userAPIKeys = apiKeysByUser.get(userKey)!
    let keyGroup = userAPIKeys.get(apiKey)
    if (!keyGroup) {
      keyGroup = {
        key: apiKey,
        apiKeyID: session.api_key_id,
        apiKeyName: session.api_key_name,
        sessions: [],
        sessionCount: 0,
        threadCount: 0,
        observationCount: 0,
        firstObservedAt: session.first_observed_at,
        lastObservedAt: session.last_observed_at,
      }
      userAPIKeys.set(apiKey, keyGroup)
      user.apiKeys.push(keyGroup)
      user.apiKeyCount += 1
    }

    const threadCount = sessionThreadCount(session)
    keyGroup.sessions.push(session)
    keyGroup.sessionCount += 1
    keyGroup.threadCount += threadCount
    keyGroup.observationCount += session.observation_count
    keyGroup.firstObservedAt = earlierTimestamp(keyGroup.firstObservedAt, session.first_observed_at)
    keyGroup.lastObservedAt = laterTimestamp(keyGroup.lastObservedAt, session.last_observed_at)

    user.sessionCount += 1
    user.threadCount += threadCount
    user.observationCount += session.observation_count
    user.firstObservedAt = earlierTimestamp(user.firstObservedAt, session.first_observed_at)
    user.lastObservedAt = laterTimestamp(user.lastObservedAt, session.last_observed_at)
  }

  return groups
}

function threadKey(session: FingerprintObservationSessionNode, thread: DisplayThreadNode): string {
  return `${sessionKey(session)}:${thread.displayKind}:${thread.thread_id || 'unthreaded'}`
}

function pruneExpansionState(nextSessions: FingerprintObservationSessionNode[]): void {
  const nextUserGroups = groupSessionsByUser(nextSessions)
  const validUsers = new Set(nextUserGroups.map((group) => group.key))
  const validAPIKeys = new Set(nextUserGroups.flatMap((group) => group.apiKeys.map((apiKey) => apiKey.key)))
  const validSessions = new Set(nextSessions.map(sessionKey))
  const validThreads = new Set<string>()
  for (const session of nextSessions) {
    for (const thread of displayThreads(session)) {
      validThreads.add(threadKey(session, thread))
    }
  }
  expandedUsers.value = new Set([...expandedUsers.value].filter((key) => validUsers.has(key)))
  expandedAPIKeys.value = new Set([...expandedAPIKeys.value].filter((key) => validAPIKeys.has(key)))
  expandedSessions.value = new Set([...expandedSessions.value].filter((key) => validSessions.has(key)))
  expandedThreads.value = new Set([...expandedThreads.value].filter((key) => validThreads.has(key)))
}

async function loadSnapshot(options: LoadSnapshotOptions): Promise<void> {
  const requestID = activeRequestID + 1
  activeRequestID = requestID
  activeRequestController?.abort()
  const requestController = new AbortController()
  activeRequestController = requestController
  const generation = toggleGeneration

  if (!options.silent) loading.value = true
  try {
    const response = await adminAPI.fingerprintObservations.list(
      {
        page: options.targetPage,
        page_size: options.targetPageSize,
        snapshot_seq: options.requestedSnapshotSeq,
      },
      { signal: requestController.signal }
    )

    if (
      disposed ||
      requestController.signal.aborted ||
      requestID !== activeRequestID ||
      generation !== toggleGeneration
    ) {
      return
    }

    observationEnabled.value = response.enabled
    if (!response.enabled) {
      clearVisibleData()
      return
    }

    const nextSessions = response.items ?? []
    sessions.value = nextSessions
    total.value = response.total
    page.value = response.page
    pageSize.value = response.page_size
    pages.value = response.pages
    snapshotSeq.value = response.snapshot_seq
    if (options.resetExpansion) {
      expandedUsers.value = new Set()
      expandedAPIKeys.value = new Set()
      expandedSessions.value = new Set()
      expandedThreads.value = new Set()
    } else {
      pruneExpansionState(nextSessions)
    }
  } catch (error: unknown) {
    if (
      disposed ||
      requestID !== activeRequestID ||
      generation !== toggleGeneration ||
      isCanceledRequest(error, requestController.signal)
    ) {
      return
    }
    appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.loadFailed')))
  } finally {
    if (requestID === activeRequestID) {
      activeRequestController = null
      if (!options.silent) loading.value = false
      autoRefresh.resetCountdown()
    }
  }
}

async function manualRefresh(): Promise<void> {
  if (toggling.value) return
  await loadSnapshot({
    targetPage: 1,
    targetPageSize: pageSize.value,
    requestedSnapshotSeq: 0,
    resetExpansion: true,
  })
}

async function pollFirstPage(): Promise<void> {
  if (page.value !== 1 || loading.value || toggling.value) return
  await loadSnapshot({
    targetPage: 1,
    targetPageSize: pageSize.value,
    requestedSnapshotSeq: 0,
    silent: true,
  })
}

function handlePageChange(nextPage: number): void {
  if (toggling.value || nextPage === page.value || nextPage < 1 || nextPage > pages.value) return
  void loadSnapshot({
    targetPage: nextPage,
    targetPageSize: pageSize.value,
    requestedSnapshotSeq: snapshotSeq.value,
    resetExpansion: true,
  })
}

function handlePageSizeChange(nextPageSize: number): void {
  if (toggling.value || nextPageSize <= 0) return
  void loadSnapshot({
    targetPage: 1,
    targetPageSize: nextPageSize,
    requestedSnapshotSeq: 0,
    resetExpansion: true,
  })
}

function snapshotVisibleState(): VisibleState {
  return {
    enabled: observationEnabled.value,
    sessions: sessions.value,
    total: total.value,
    page: page.value,
    pageSize: pageSize.value,
    pages: pages.value,
    snapshotSeq: snapshotSeq.value,
    expandedUsers: new Set(expandedUsers.value),
    expandedAPIKeys: new Set(expandedAPIKeys.value),
    expandedSessions: new Set(expandedSessions.value),
    expandedThreads: new Set(expandedThreads.value),
  }
}

function restoreVisibleState(state: VisibleState): void {
  observationEnabled.value = state.enabled
  sessions.value = state.sessions
  total.value = state.total
  page.value = state.page
  pageSize.value = state.pageSize
  pages.value = state.pages
  snapshotSeq.value = state.snapshotSeq
  expandedUsers.value = state.expandedUsers
  expandedAPIKeys.value = state.expandedAPIKeys
  expandedSessions.value = state.expandedSessions
  expandedThreads.value = state.expandedThreads
}

async function setObservationEnabled(value: boolean): Promise<void> {
  if (toggling.value) return

  const previous = snapshotVisibleState()
  cancelActiveRequest()
  const mutationGeneration = toggleGeneration + 1
  toggleGeneration = mutationGeneration
  toggling.value = true
  observationEnabled.value = value
  if (!value) clearVisibleData()

  try {
    const settings = await adminAPI.settings.updateSettings({
      installation_observation_enabled: value,
    })
    if (disposed || mutationGeneration !== toggleGeneration) return

    const enabled = settings?.installation_observation_enabled ?? value
    observationEnabled.value = enabled
    if (enabled) {
      await loadSnapshot({
        targetPage: 1,
        targetPageSize: pageSize.value,
        requestedSnapshotSeq: 0,
        resetExpansion: true,
      })
    } else {
      clearVisibleData()
    }

    if (disposed || mutationGeneration !== toggleGeneration) return
    appStore.showSuccess(
      observationEnabled.value
        ? t('admin.fingerprintObservation.enabledSuccess')
        : t('admin.fingerprintObservation.disabledSuccess')
    )
  } catch (error: unknown) {
    if (disposed || mutationGeneration !== toggleGeneration) return
    restoreVisibleState(previous)
    appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.toggleFailed')))
  } finally {
    if (mutationGeneration === toggleGeneration) toggling.value = false
  }
}

function toggleSetValue(target: typeof expandedSessions, key: string): void {
  const next = new Set(target.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  target.value = next
}

function toggleUser(group: FingerprintUserGroup): void {
  toggleSetValue(expandedUsers, group.key)
}

function isUserExpanded(group: FingerprintUserGroup): boolean {
  return expandedUsers.value.has(group.key)
}

function toggleAPIKey(group: FingerprintAPIKeyGroup): void {
  toggleSetValue(expandedAPIKeys, group.key)
}

function isAPIKeyExpanded(group: FingerprintAPIKeyGroup): boolean {
  return expandedAPIKeys.value.has(group.key)
}

function toggleSession(session: FingerprintObservationSessionNode): void {
  toggleSetValue(expandedSessions, sessionKey(session))
}

function isSessionExpanded(session: FingerprintObservationSessionNode): boolean {
  return expandedSessions.value.has(sessionKey(session))
}

function toggleThread(session: FingerprintObservationSessionNode, thread: DisplayThreadNode): void {
  if (thread.observations.length === 0) return
  toggleSetValue(expandedThreads, threadKey(session, thread))
}

function isThreadExpanded(session: FingerprintObservationSessionNode, thread: DisplayThreadNode): boolean {
  return expandedThreads.value.has(threadKey(session, thread))
}

function displayThreads(session: FingerprintObservationSessionNode): DisplayThreadNode[] {
  const result: DisplayThreadNode[] = []
  if (session.root_thread) result.push({ ...session.root_thread, displayKind: 'thread' })
  for (const thread of session.child_threads ?? []) {
    result.push({ ...thread, displayKind: 'thread' })
  }
  if (session.unthreaded_observations?.length) {
    const observations = session.unthreaded_observations
    result.push({
      displayKind: 'unthreaded',
      thread_id: '',
      parent_thread_id: '',
      forked_from_thread_id: '',
      relation: 'descendant',
      first_observed_at: observations[observations.length - 1]?.timestamp ?? session.first_observed_at,
      last_observed_at: observations[0]?.timestamp ?? session.last_observed_at,
      observation_count: observations.length,
      observations,
    })
  }
  return result
}

function isUnthreaded(thread: DisplayThreadNode): boolean {
  return thread.displayKind === 'unthreaded'
}

function sessionThreadCount(session: FingerprintObservationSessionNode): number {
  return (session.root_thread ? 1 : 0) + (session.child_threads?.length ?? 0)
}

function userPanelId(userIndex: number): string {
  return `fingerprint-user-${userIndex}`
}

function apiKeyPanelId(userIndex: number, apiKeyIndex: number): string {
  return `fingerprint-user-${userIndex}-api-key-${apiKeyIndex}`
}

function sessionPanelId(userIndex: number, apiKeyIndex: number, sessionIndex: number): string {
  return `fingerprint-user-${userIndex}-api-key-${apiKeyIndex}-session-${sessionIndex}`
}

function threadPanelId(
  userIndex: number,
  apiKeyIndex: number,
  sessionIndex: number,
  threadIndex: number
): string {
  return `${sessionPanelId(userIndex, apiKeyIndex, sessionIndex)}-thread-${threadIndex}`
}

function userToggleLabel(group: FingerprintUserGroup): string {
  return t(
    isUserExpanded(group)
      ? 'admin.fingerprintObservation.collapseUser'
      : 'admin.fingerprintObservation.expandUser',
    { name: userGroupLabel(group) }
  )
}

function apiKeyToggleLabel(group: FingerprintAPIKeyGroup): string {
  return t(
    isAPIKeyExpanded(group)
      ? 'admin.fingerprintObservation.collapseApiKey'
      : 'admin.fingerprintObservation.expandApiKey',
    { name: apiKeyGroupLabel(group) }
  )
}

function sessionToggleLabel(session: FingerprintObservationSessionNode): string {
  return t(
    isSessionExpanded(session)
      ? 'admin.fingerprintObservation.collapseSession'
      : 'admin.fingerprintObservation.expandSession',
    { id: session.session_id || '—' }
  )
}

function threadToggleLabel(
  session: FingerprintObservationSessionNode,
  thread: DisplayThreadNode
): string {
  const expanded = isThreadExpanded(session, thread)
  const key = isUnthreaded(thread)
    ? expanded
      ? 'admin.fingerprintObservation.collapseUnthreaded'
      : 'admin.fingerprintObservation.expandUnthreaded'
    : expanded
      ? 'admin.fingerprintObservation.collapseThread'
      : 'admin.fingerprintObservation.expandThread'
  return t(key, { id: thread.thread_id || '—' })
}

function userGroupLabel(group: FingerprintUserGroup): string {
  return group.username || group.email || t('admin.fingerprintObservation.unknownUser')
}

function apiKeyGroupLabel(group: FingerprintAPIKeyGroup): string {
  return group.apiKeyName || t('admin.fingerprintObservation.unknownApiKey')
}

function threadRelationLabel(thread: DisplayThreadNode): string {
  if (isUnthreaded(thread)) return t('admin.fingerprintObservation.unthreaded')
  return thread.relation === 'root'
    ? t('admin.fingerprintObservation.rootThread')
    : t('admin.fingerprintObservation.childThread')
}

function threadRelationClass(thread: DisplayThreadNode): string {
  const base = 'inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold '
  if (isUnthreaded(thread)) {
    return base + 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
  return thread.relation === 'root'
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}

function pinBadgeClass(pinned: boolean): string {
  const base = 'inline-flex w-fit items-center rounded-full px-2 py-0.5 text-xs font-semibold '
  return pinned
    ? base + 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : base + 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

function installationDiffClass(observation: FingerprintObservationEntry): string {
  if (!observation.pinned) return 'text-gray-500 dark:text-gray-400'
  const differs =
    !!observation.client_reported_installation_id &&
    observation.client_reported_installation_id !== observation.outbound_installation_id
  return differs ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'
}

function formatTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString()
}

onMounted(async () => {
  await manualRefresh()
  if (!disposed && autoRefresh.enabled.value) {
    autoRefresh.resetCountdown()
    autoRefresh.start()
  }
})

onBeforeUnmount(() => {
  disposed = true
  toggleGeneration += 1
  cancelActiveRequest()
})
</script>
