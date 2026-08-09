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
                <Icon name="refresh" size="md" :class="rootRequestPending ? 'animate-spin' : ''" />
              </button>

              <AutoRefreshButton
                :enabled="autoRefresh.enabled.value"
                :paused="autoRefreshPaused"
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
            <div v-if="rootLoading && users.length === 0" class="flex h-full min-h-72 items-center justify-center">
              <div class="flex flex-col items-center gap-3 text-sm text-gray-500 dark:text-gray-400">
                <Icon name="refresh" size="lg" class="animate-spin" />
                <span>{{ t('common.loading') }}</span>
              </div>
            </div>

            <ul v-else-if="users.length > 0" class="divide-y divide-gray-200 dark:divide-dark-700">
              <li v-for="(user, userIndex) in users" :key="user.node_id" class="bg-white dark:bg-dark-800">
                <div
                  class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 px-4 py-3.5"
                >
                  <button
                    type="button"
                    class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                    :disabled="rootRequestPending"
                    :aria-expanded="isExpanded(expandedUsers, user.node_id)"
                    :aria-controls="userPanelId(userIndex)"
                    :aria-label="userToggleLabel(user)"
                    :title="userToggleLabel(user)"
                    @click="toggleUser(user)"
                  >
                    <Icon
                      :name="isExpanded(expandedUsers, user.node_id) ? 'chevronDown' : 'chevronRight'"
                      size="sm"
                    />
                  </button>

                  <div class="min-w-0 space-y-1">
                    <div class="flex min-w-0 items-center gap-1.5 text-sm font-semibold text-gray-900 dark:text-white">
                      <Icon name="user" size="xs" class="shrink-0 text-gray-400" />
                      <span class="truncate" :title="user.email || user.username">{{ userLabel(user) }}</span>
                      <span v-if="user.user_id > 0" class="shrink-0 text-xs font-normal text-gray-400">
                        #{{ user.user_id }}
                      </span>
                    </div>
                    <div
                      v-if="user.email && user.email !== userLabel(user)"
                      class="truncate pl-5 text-xs text-gray-500 dark:text-gray-400"
                      :title="user.email"
                    >
                      {{ user.email }}
                    </div>
                  </div>

                  <div class="flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 text-xs text-gray-500 dark:text-gray-400">
                    <span>{{ t('admin.fingerprintObservation.apiKeyCount', { count: user.api_key_count }) }}</span>
                    <span>{{ t('admin.fingerprintObservation.sessionCount', { count: user.session_count }) }}</span>
                  </div>

                  <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
                    <div class="flex flex-wrap gap-x-3 gap-y-0.5">
                      <span>{{ t('admin.fingerprintObservation.threadCount', { count: user.thread_count }) }}</span>
                      <span>{{ t('admin.fingerprintObservation.observationCount', { count: user.observation_count }) }}</span>
                      <span v-if="user.unattributed_observation_count">
                        {{ t('admin.fingerprintObservation.unattributedObservationCount', { count: user.unattributed_observation_count }) }}
                      </span>
                    </div>
                    <div class="mt-1 truncate" :title="formatTime(user.last_observed_at)">
                      {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(user.last_observed_at) }}
                    </div>
                  </div>
                </div>

                <div
                  v-if="isExpanded(expandedUsers, user.node_id)"
                  :id="userPanelId(userIndex)"
                  class="border-t border-gray-100 bg-gray-50/65 dark:border-dark-700 dark:bg-dark-900/25"
                >
                  <div
                    v-for="(apiKey, apiKeyIndex) in apiKeyState(user.node_id).items"
                    :key="apiKey.node_id"
                    class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                  >
                    <div
                      class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 py-3 pl-9 pr-4"
                    >
                      <button
                        type="button"
                        class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-200/70 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                        :disabled="rootRequestPending"
                        :aria-expanded="isExpanded(expandedAPIKeys, apiKey.node_id)"
                        :aria-controls="apiKeyPanelId(userIndex, apiKeyIndex)"
                        :aria-label="apiKeyToggleLabel(apiKey)"
                        :title="apiKeyToggleLabel(apiKey)"
                        @click="toggleAPIKey(apiKey)"
                      >
                        <Icon
                          :name="isExpanded(expandedAPIKeys, apiKey.node_id) ? 'chevronDown' : 'chevronRight'"
                          size="sm"
                        />
                      </button>

                      <div class="flex min-w-0 items-center gap-1.5 text-sm font-medium text-gray-800 dark:text-gray-100">
                        <Icon name="key" size="xs" class="shrink-0 text-gray-400" />
                        <span class="truncate" :title="apiKey.api_key_name">{{ apiKeyLabel(apiKey) }}</span>
                        <span v-if="apiKey.api_key_id > 0" class="shrink-0 text-xs font-normal text-gray-400">
                          #{{ apiKey.api_key_id }}
                        </span>
                      </div>

                      <div class="text-xs text-gray-500 dark:text-gray-400">
                        {{ t('admin.fingerprintObservation.sessionCount', { count: apiKey.session_count }) }}
                      </div>

                      <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
                        <div class="flex flex-wrap gap-x-3 gap-y-0.5">
                          <span>{{ t('admin.fingerprintObservation.threadCount', { count: apiKey.thread_count }) }}</span>
                          <span>{{ t('admin.fingerprintObservation.observationCount', { count: apiKey.observation_count }) }}</span>
                          <span v-if="apiKey.unattributed_observation_count">
                            {{ t('admin.fingerprintObservation.unattributedObservationCount', { count: apiKey.unattributed_observation_count }) }}
                          </span>
                        </div>
                        <div class="mt-1 truncate" :title="formatTime(apiKey.last_observed_at)">
                          {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(apiKey.last_observed_at) }}
                        </div>
                      </div>
                    </div>

                    <div
                      v-if="isExpanded(expandedAPIKeys, apiKey.node_id)"
                      :id="apiKeyPanelId(userIndex, apiKeyIndex)"
                      class="border-t border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-800"
                    >
                      <div
                        v-for="(session, sessionIndex) in sessionState(apiKey.node_id).items"
                        :key="session.node_id"
                        class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                      >
                        <div
                          class="grid grid-cols-[2rem_minmax(0,1.25fr)_minmax(0,1.4fr)_minmax(10rem,0.8fr)] items-center gap-3 py-3 pl-14 pr-4"
                        >
                          <button
                            type="button"
                            class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                            :disabled="rootRequestPending"
                            :aria-expanded="isExpanded(expandedSessions, session.node_id)"
                            :aria-controls="sessionPanelId(userIndex, apiKeyIndex, sessionIndex)"
                            :aria-label="sessionToggleLabel(session)"
                            :title="sessionToggleLabel(session)"
                            @click="toggleSession(session)"
                          >
                            <Icon
                              :name="isExpanded(expandedSessions, session.node_id) ? 'chevronDown' : 'chevronRight'"
                              size="sm"
                            />
                          </button>

                          <div class="min-w-0">
                            <span class="inline-flex rounded-full bg-green-100 px-2 py-0.5 text-[11px] font-semibold text-green-700 dark:bg-green-900/30 dark:text-green-300">
                                  {{
                                    session.unattributed
                                      ? t('admin.fingerprintObservation.unattributedSession')
                                      : t('admin.fingerprintObservation.rootSession')
                                  }}
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
                              <span>{{ t('admin.fingerprintObservation.threadCount', { count: session.thread_count }) }}</span>
                              <span>{{ t('admin.fingerprintObservation.observationCount', { count: session.observation_count }) }}</span>
                              <span v-if="session.unthreaded_observation_count">
                                {{ t('admin.fingerprintObservation.unthreadedObservationCount', { count: session.unthreaded_observation_count }) }}
                              </span>
                            </div>
                            <div class="mt-1 truncate" :title="formatTime(session.last_observed_at)">
                              {{ t('admin.fingerprintObservation.lastSeen') }} {{ formatTime(session.last_observed_at) }}
                            </div>
                          </div>
                        </div>

                        <div
                          v-if="isExpanded(expandedSessions, session.node_id)"
                          :id="sessionPanelId(userIndex, apiKeyIndex, sessionIndex)"
                          class="border-t border-gray-100 bg-gray-50/60 dark:border-dark-700 dark:bg-dark-900/35"
                        >
                          <div
                            v-for="(thread, threadIndex) in threadState(session.node_id).items"
                            :key="thread.node_id"
                            class="border-b border-gray-100 last:border-b-0 dark:border-dark-700"
                          >
                            <div
                              class="grid grid-cols-[2rem_minmax(0,1fr)_minmax(0,1.35fr)_auto] items-center gap-3 py-3 pl-20 pr-4"
                            >
                              <button
                                type="button"
                                class="flex h-7 w-7 items-center justify-center rounded-md text-gray-400 transition-colors hover:bg-gray-200/70 hover:text-gray-700 disabled:cursor-default disabled:opacity-40 dark:hover:bg-dark-700 dark:hover:text-gray-200"
                                :disabled="rootRequestPending || thread.observation_count === 0"
                                :aria-expanded="isExpanded(expandedThreads, thread.node_id)"
                                :aria-controls="threadPanelId(userIndex, apiKeyIndex, sessionIndex, threadIndex)"
                                :aria-label="threadToggleLabel(thread)"
                                :title="threadToggleLabel(thread)"
                                @click="toggleThread(thread)"
                              >
                                <Icon
                                  :name="isExpanded(expandedThreads, thread.node_id) ? 'chevronDown' : 'chevronRight'"
                                  size="sm"
                                />
                              </button>

                              <div class="min-w-0">
                                <div class="flex flex-wrap items-center gap-2">
                                  <span :class="threadRelationClass(thread)">{{ threadRelationLabel(thread) }}</span>
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
                                  <span class="truncate font-mono" :title="thread.parent_thread_id">{{ thread.parent_thread_id }}</span>
                                </div>
                                <div v-if="thread.forked_from_thread_id" class="flex min-w-0 gap-2">
                                  <span class="shrink-0">{{ t('admin.fingerprintObservation.forkedFrom') }}</span>
                                  <span class="truncate font-mono" :title="thread.forked_from_thread_id">{{ thread.forked_from_thread_id }}</span>
                                </div>
                                <span v-if="!thread.parent_thread_id && !thread.forked_from_thread_id">—</span>
                              </div>

                              <div class="whitespace-nowrap text-right text-xs text-gray-400">
                                {{ formatTime(thread.last_observed_at) }}
                              </div>
                            </div>

                            <div
                              v-if="isExpanded(expandedThreads, thread.node_id)"
                              :id="threadPanelId(userIndex, apiKeyIndex, sessionIndex, threadIndex)"
                              class="border-t border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-800"
                            >
                              <div v-if="entryState(thread.node_id).items.length > 0" class="overflow-x-auto">
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
                                      v-for="observation in entryState(thread.node_id).items"
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
                              <LazyStateFooter
                                :state="entryState(thread.node_id)"
                                @retry="retryEntries(thread.node_id)"
                                @load-more="loadMoreEntries(thread.node_id)"
                              />
                            </div>
                          </div>

                          <LazyStateFooter
                            :state="threadState(session.node_id)"
                            @retry="retryThreads(session.node_id)"
                            @load-more="loadMoreThreads(session.node_id)"
                          />
                        </div>
                      </div>

                      <LazyStateFooter
                        :state="sessionState(apiKey.node_id)"
                        @retry="retrySessions(apiKey.node_id)"
                        @load-more="loadMoreSessions(apiKey.node_id)"
                      />
                    </div>
                  </div>

                  <LazyStateFooter
                    :state="apiKeyState(user.node_id)"
                    @retry="retryAPIKeys(user.node_id)"
                    @load-more="loadMoreAPIKeys(user.node_id)"
                  />
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
          :page-size-options="[20, 50, 100]"
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
  type FingerprintObservationAPIKeySummary,
  type FingerprintObservationChildrenParams,
  type FingerprintObservationChildrenResponse,
  type FingerprintObservationEntry,
  type FingerprintObservationSessionSummary,
  type FingerprintObservationThreadSummary,
  type FingerprintObservationUserSummary,
} from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import Pagination from '@/components/common/Pagination.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import LazyStateFooter from './components/FingerprintObservationLazyFooter.vue'

interface LazyCollection<T> {
  items: T[]
  total: number
  nextCursor: string
  loaded: boolean
  loading: boolean
  error: string
}

type LazyCache<T> = Record<string, LazyCollection<T>>
type ChildFetcher<T> = (
  params: FingerprintObservationChildrenParams,
  options: { signal: AbortSignal }
) => Promise<FingerprintObservationChildrenResponse<T>>

const defaultPageSize = 20
const allowedPageSizes = [20, 50, 100] as const
const childPageSize = 20
const { t } = useI18n()
const appStore = useAppStore()

const observationEnabled = ref(false)
const toggling = ref(false)
const rootLoading = ref(false)
const rootRequestPending = ref(false)
const users = ref<FingerprintObservationUserSummary[]>([])
const total = ref(0)
const page = ref(1)
const persistedPageSize = getPersistedPageSize(defaultPageSize)
const pageSize = ref<number>(
  allowedPageSizes.includes(persistedPageSize as (typeof allowedPageSizes)[number])
    ? persistedPageSize
    : defaultPageSize
)
const pages = ref(1)
const snapshotToken = ref('')

const expandedUsers = ref(new Set<string>())
const expandedAPIKeys = ref(new Set<string>())
const expandedSessions = ref(new Set<string>())
const expandedThreads = ref(new Set<string>())

const apiKeyCache = ref<LazyCache<FingerprintObservationAPIKeySummary>>({})
const sessionCache = ref<LazyCache<FingerprintObservationSessionSummary>>({})
const threadCache = ref<LazyCache<FingerprintObservationThreadSummary>>({})
const entryCache = ref<LazyCache<FingerprintObservationEntry>>({})

let disposed = false
let requestGeneration = 0
let toggleGeneration = 0
let rootController: AbortController | null = null
let snapshotRecoveryPromise: Promise<void> | null = null
const childControllers = new Map<string, AbortController>()

const hasExpandedNodes = computed(
  () =>
    expandedUsers.value.size > 0 ||
    expandedAPIKeys.value.size > 0 ||
    expandedSessions.value.size > 0 ||
    expandedThreads.value.size > 0
)

const childRequestPending = computed(() =>
  [apiKeyCache.value, sessionCache.value, threadCache.value, entryCache.value].some((cache) =>
    Object.values(cache).some((state) => state.loading)
  )
)

const autoRefresh = useAutoRefresh({
  storageKey: 'admin-fingerprint-observation-auto-refresh',
  intervals: [5, 10, 15, 30] as const,
  defaultInterval: 5,
  onRefresh: pollFirstPage,
  shouldPause: () =>
    (typeof document !== 'undefined' && document.hidden) ||
    !observationEnabled.value ||
    page.value !== 1 ||
    hasExpandedNodes.value ||
    rootRequestPending.value ||
    childRequestPending.value ||
    toggling.value,
})

const autoRefreshPaused = computed(
  () =>
    autoRefresh.enabled.value &&
    (!observationEnabled.value ||
      page.value !== 1 ||
      hasExpandedNodes.value ||
      rootRequestPending.value ||
      childRequestPending.value ||
      toggling.value)
)

const statusBadgeClass = computed(() => {
  const base = 'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold '
  return observationEnabled.value
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
})

const statusDotClass = computed(() => (observationEnabled.value ? 'bg-green-500' : 'bg-gray-400'))

function createLazyCollection<T>(): LazyCollection<T> {
  return { items: [], total: 0, nextCursor: '', loaded: false, loading: false, error: '' }
}

function ensureState<T>(cache: LazyCache<T>, parentNodeID: string): LazyCollection<T> {
  return (cache[parentNodeID] ??= createLazyCollection<T>())
}

function apiKeyState(parentNodeID: string): LazyCollection<FingerprintObservationAPIKeySummary> {
  return ensureState(apiKeyCache.value, parentNodeID)
}

function sessionState(parentNodeID: string): LazyCollection<FingerprintObservationSessionSummary> {
  return ensureState(sessionCache.value, parentNodeID)
}

function threadState(parentNodeID: string): LazyCollection<FingerprintObservationThreadSummary> {
  return ensureState(threadCache.value, parentNodeID)
}

function entryState(parentNodeID: string): LazyCollection<FingerprintObservationEntry> {
  return ensureState(entryCache.value, parentNodeID)
}

function isCanceledRequest(error: unknown, signal: AbortSignal): boolean {
  if (signal.aborted) return true
  if (!error || typeof error !== 'object') return false
  const candidate = error as { name?: string; code?: string }
  return candidate.name === 'AbortError' || candidate.code === 'ERR_CANCELED'
}

function isSnapshotExpired(error: unknown): boolean {
  return extractApiErrorCode(error) === 'fingerprint_snapshot_expired'
}

function isExpanded(target: Set<string>, nodeID: string): boolean {
  return target.has(nodeID)
}

function addExpanded(target: typeof expandedUsers, nodeID: string): void {
  target.value = new Set(target.value).add(nodeID)
}

function removeExpanded(target: typeof expandedUsers, nodeID: string): void {
  const next = new Set(target.value)
  next.delete(nodeID)
  target.value = next
}

function clearHierarchy(): void {
  expandedUsers.value = new Set()
  expandedAPIKeys.value = new Set()
  expandedSessions.value = new Set()
  expandedThreads.value = new Set()
  apiKeyCache.value = {}
  sessionCache.value = {}
  threadCache.value = {}
  entryCache.value = {}
}

function clearVisibleData(): void {
  users.value = []
  total.value = 0
  page.value = 1
  pages.value = 1
  snapshotToken.value = ''
  clearHierarchy()
}

function markAllChildrenIdle(): void {
  for (const cache of [apiKeyCache.value, sessionCache.value, threadCache.value, entryCache.value]) {
    for (const state of Object.values(cache)) state.loading = false
  }
}

function abortAllChildRequests(): void {
  for (const controller of childControllers.values()) controller.abort()
  childControllers.clear()
  markAllChildrenIdle()
}

function abortAllRequests(): void {
  rootController?.abort()
  rootController = null
  abortAllChildRequests()
  rootRequestPending.value = false
  rootLoading.value = false
}

function invalidateAllRequests(): number {
  requestGeneration += 1
  abortAllRequests()
  return requestGeneration
}

function abortChildRequest(requestKey: string, state?: LazyCollection<unknown>): void {
  childControllers.get(requestKey)?.abort()
  childControllers.delete(requestKey)
  if (state) state.loading = false
}

function mergeUnique<T>(existing: T[], incoming: T[], identity: (item: T) => string | number): T[] {
  const merged = [...existing]
  const seen = new Set(existing.map(identity))
  for (const item of incoming) {
    const key = identity(item)
    if (!seen.has(key)) {
      seen.add(key)
      merged.push(item)
    }
  }
  return merged
}

function recoverExpiredSnapshot(): Promise<void> {
  if (disposed) return Promise.resolve()
  if (snapshotRecoveryPromise) return snapshotRecoveryPromise

  appStore.showError(t('admin.fingerprintObservation.snapshotExpired'))
  invalidateAllRequests()
  clearVisibleData()

  const recovery = loadUsers({ targetPage: 1, targetPageSize: pageSize.value })
  const tracked = recovery.finally(() => {
    if (snapshotRecoveryPromise === tracked) snapshotRecoveryPromise = null
  })
  snapshotRecoveryPromise = tracked
  return tracked
}

async function loadChildren<T>(options: {
  requestKey: string
  parentNodeID: string
  state: LazyCollection<T>
  fetcher: ChildFetcher<T>
  append: boolean
  identity: (item: T) => string | number
}): Promise<void> {
  if (options.state.loading || disposed) return
  const cursor = options.append ? options.state.nextCursor : ''
  if (options.append && !cursor) return

  abortChildRequest(options.requestKey, options.state)
  const controller = new AbortController()
  childControllers.set(options.requestKey, controller)
  const generation = requestGeneration
  options.state.loading = true
  options.state.error = ''

  try {
    const params: FingerprintObservationChildrenParams = {
      snapshot_token: snapshotToken.value,
      parent_node_id: options.parentNodeID,
      limit: childPageSize,
    }
    if (cursor) params.cursor = cursor

    const response = await options.fetcher(params, { signal: controller.signal })
    if (
      disposed ||
      controller.signal.aborted ||
      generation !== requestGeneration ||
      childControllers.get(options.requestKey) !== controller
    ) {
      return
    }

    options.state.items = options.append
      ? mergeUnique(options.state.items, response.items ?? [], options.identity)
      : (response.items ?? [])
    options.state.total = response.total
    options.state.nextCursor = response.next_cursor ?? ''
    options.state.loaded = true
  } catch (error: unknown) {
    if (
      disposed ||
      generation !== requestGeneration ||
      childControllers.get(options.requestKey) !== controller ||
      isCanceledRequest(error, controller.signal)
    ) {
      return
    }
    if (isSnapshotExpired(error)) {
      await recoverExpiredSnapshot()
      return
    }
    options.state.error = extractApiErrorMessage(error, t('admin.fingerprintObservation.loadFailed'))
  } finally {
    if (childControllers.get(options.requestKey) === controller) {
      childControllers.delete(options.requestKey)
      options.state.loading = false
      autoRefresh.resetCountdown()
    }
  }
}

function loadAPIKeys(parentNodeID: string, append = false): Promise<void> {
  return loadChildren({
    requestKey: `api-keys:${parentNodeID}`,
    parentNodeID,
    state: apiKeyState(parentNodeID),
    fetcher: adminAPI.fingerprintObservations.listAPIKeys,
    append,
    identity: (item) => item.node_id,
  })
}

function loadSessions(parentNodeID: string, append = false): Promise<void> {
  return loadChildren({
    requestKey: `sessions:${parentNodeID}`,
    parentNodeID,
    state: sessionState(parentNodeID),
    fetcher: adminAPI.fingerprintObservations.listSessions,
    append,
    identity: (item) => item.node_id,
  })
}

function loadThreads(parentNodeID: string, append = false): Promise<void> {
  return loadChildren({
    requestKey: `threads:${parentNodeID}`,
    parentNodeID,
    state: threadState(parentNodeID),
    fetcher: adminAPI.fingerprintObservations.listThreads,
    append,
    identity: (item) => item.node_id,
  })
}

function loadEntries(parentNodeID: string, append = false): Promise<void> {
  return loadChildren({
    requestKey: `entries:${parentNodeID}`,
    parentNodeID,
    state: entryState(parentNodeID),
    fetcher: adminAPI.fingerprintObservations.listEntries,
    append,
    identity: (item) => item.sequence_id,
  })
}

async function loadUsers(options: {
  targetPage: number
  targetPageSize: number
  requestedSnapshotToken?: string
  silent?: boolean
}): Promise<void> {
  const generation = invalidateAllRequests()
  clearHierarchy()
  const controller = new AbortController()
  rootController = controller
  rootRequestPending.value = true
  if (!options.silent) rootLoading.value = true

  try {
    const params: { page: number; page_size: number; snapshot_token?: string } = {
      page: options.targetPage,
      page_size: options.targetPageSize,
    }
    if (options.requestedSnapshotToken) params.snapshot_token = options.requestedSnapshotToken
    const response = await adminAPI.fingerprintObservations.list(params, { signal: controller.signal })
    if (
      disposed ||
      controller.signal.aborted ||
      generation !== requestGeneration ||
      rootController !== controller
    ) {
      return
    }

    abortAllChildRequests()
    clearHierarchy()
    observationEnabled.value = response.enabled
    if (!response.enabled) {
      clearVisibleData()
      return
    }
    users.value = response.items ?? []
    total.value = response.total
    page.value = response.page
    pageSize.value = response.page_size
    pages.value = response.pages
    snapshotToken.value = response.snapshot_token
  } catch (error: unknown) {
    if (
      disposed ||
      generation !== requestGeneration ||
      rootController !== controller ||
      isCanceledRequest(error, controller.signal)
    ) {
      return
    }
    if (options.requestedSnapshotToken && isSnapshotExpired(error)) {
      await recoverExpiredSnapshot()
      return
    }
    appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.loadFailed')))
  } finally {
    if (rootController === controller) {
      rootController = null
      rootRequestPending.value = false
      rootLoading.value = false
      autoRefresh.resetCountdown()
    }
  }
}

function manualRefresh(): Promise<void> {
  if (toggling.value) return Promise.resolve()
  return loadUsers({ targetPage: 1, targetPageSize: pageSize.value })
}

function pollFirstPage(): Promise<void> {
  if (
    page.value !== 1 ||
    hasExpandedNodes.value ||
    rootRequestPending.value ||
    childRequestPending.value ||
    toggling.value
  ) {
    return Promise.resolve()
  }
  return loadUsers({ targetPage: 1, targetPageSize: pageSize.value, silent: true })
}

function handlePageChange(nextPage: number): void {
  if (toggling.value || nextPage === page.value || nextPage < 1 || nextPage > pages.value) return
  const token = snapshotToken.value
  void loadUsers({
    targetPage: nextPage,
    targetPageSize: pageSize.value,
    requestedSnapshotToken: token,
  })
}

function handlePageSizeChange(nextPageSize: number): void {
  if (
    toggling.value ||
    !allowedPageSizes.includes(nextPageSize as (typeof allowedPageSizes)[number])
  ) {
    return
  }
  void loadUsers({ targetPage: 1, targetPageSize: nextPageSize })
}

async function setObservationEnabled(value: boolean): Promise<void> {
  if (toggling.value || value === observationEnabled.value) return
  const mutationGeneration = toggleGeneration + 1
  toggleGeneration = mutationGeneration
  toggling.value = true

  try {
    const settings = await adminAPI.settings.updateSettings({ installation_observation_enabled: value })
    if (disposed || mutationGeneration !== toggleGeneration) return
    const enabled = settings?.installation_observation_enabled ?? value
    observationEnabled.value = enabled
    if (enabled) {
      await loadUsers({ targetPage: 1, targetPageSize: pageSize.value })
    } else {
      invalidateAllRequests()
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
    appStore.showError(extractApiErrorMessage(error, t('admin.fingerprintObservation.toggleFailed')))
  } finally {
    if (mutationGeneration === toggleGeneration) toggling.value = false
  }
}

function collapseThread(nodeID: string): void {
  removeExpanded(expandedThreads, nodeID)
  abortChildRequest(`entries:${nodeID}`, entryCache.value[nodeID])
}

function collapseSession(nodeID: string): void {
  removeExpanded(expandedSessions, nodeID)
  abortChildRequest(`threads:${nodeID}`, threadCache.value[nodeID])
  for (const thread of threadCache.value[nodeID]?.items ?? []) collapseThread(thread.node_id)
}

function collapseAPIKey(nodeID: string): void {
  removeExpanded(expandedAPIKeys, nodeID)
  abortChildRequest(`sessions:${nodeID}`, sessionCache.value[nodeID])
  for (const session of sessionCache.value[nodeID]?.items ?? []) collapseSession(session.node_id)
}

function collapseUser(nodeID: string): void {
  removeExpanded(expandedUsers, nodeID)
  abortChildRequest(`api-keys:${nodeID}`, apiKeyCache.value[nodeID])
  for (const apiKey of apiKeyCache.value[nodeID]?.items ?? []) collapseAPIKey(apiKey.node_id)
}

function toggleUser(user: FingerprintObservationUserSummary): void {
  if (rootRequestPending.value) return
  if (expandedUsers.value.has(user.node_id)) {
    collapseUser(user.node_id)
    return
  }
  addExpanded(expandedUsers, user.node_id)
  const state = apiKeyState(user.node_id)
  if (!state.loaded) void loadAPIKeys(user.node_id)
}

function toggleAPIKey(apiKey: FingerprintObservationAPIKeySummary): void {
  if (rootRequestPending.value) return
  if (expandedAPIKeys.value.has(apiKey.node_id)) {
    collapseAPIKey(apiKey.node_id)
    return
  }
  addExpanded(expandedAPIKeys, apiKey.node_id)
  const state = sessionState(apiKey.node_id)
  if (!state.loaded) void loadSessions(apiKey.node_id)
}

function toggleSession(session: FingerprintObservationSessionSummary): void {
  if (rootRequestPending.value) return
  if (expandedSessions.value.has(session.node_id)) {
    collapseSession(session.node_id)
    return
  }
  addExpanded(expandedSessions, session.node_id)
  const state = threadState(session.node_id)
  if (!state.loaded) void loadThreads(session.node_id)
}

function toggleThread(thread: FingerprintObservationThreadSummary): void {
  if (rootRequestPending.value || thread.observation_count === 0) return
  if (expandedThreads.value.has(thread.node_id)) {
    collapseThread(thread.node_id)
    return
  }
  addExpanded(expandedThreads, thread.node_id)
  const state = entryState(thread.node_id)
  if (!state.loaded) void loadEntries(thread.node_id)
}

function retryAPIKeys(nodeID: string): void {
  void loadAPIKeys(nodeID, apiKeyState(nodeID).loaded)
}

function retrySessions(nodeID: string): void {
  void loadSessions(nodeID, sessionState(nodeID).loaded)
}

function retryThreads(nodeID: string): void {
  void loadThreads(nodeID, threadState(nodeID).loaded)
}

function retryEntries(nodeID: string): void {
  void loadEntries(nodeID, entryState(nodeID).loaded)
}

function loadMoreAPIKeys(nodeID: string): void {
  void loadAPIKeys(nodeID, true)
}

function loadMoreSessions(nodeID: string): void {
  void loadSessions(nodeID, true)
}

function loadMoreThreads(nodeID: string): void {
  void loadThreads(nodeID, true)
}

function loadMoreEntries(nodeID: string): void {
  void loadEntries(nodeID, true)
}

function userPanelId(userIndex: number): string {
  return `fingerprint-user-${userIndex}`
}

function apiKeyPanelId(userIndex: number, apiKeyIndex: number): string {
  return `${userPanelId(userIndex)}-api-key-${apiKeyIndex}`
}

function sessionPanelId(userIndex: number, apiKeyIndex: number, sessionIndex: number): string {
  return `${apiKeyPanelId(userIndex, apiKeyIndex)}-session-${sessionIndex}`
}

function threadPanelId(
  userIndex: number,
  apiKeyIndex: number,
  sessionIndex: number,
  threadIndex: number
): string {
  return `${sessionPanelId(userIndex, apiKeyIndex, sessionIndex)}-thread-${threadIndex}`
}

function userLabel(user: FingerprintObservationUserSummary): string {
  if (user.unattributed) return t('admin.fingerprintObservation.unattributedUser')
  return user.username || user.email || t('admin.fingerprintObservation.unknownUser')
}

function apiKeyLabel(apiKey: FingerprintObservationAPIKeySummary): string {
  if (apiKey.unattributed) return t('admin.fingerprintObservation.unattributedApiKey')
  return apiKey.api_key_name || t('admin.fingerprintObservation.unknownApiKey')
}

function userToggleLabel(user: FingerprintObservationUserSummary): string {
  return t(
    expandedUsers.value.has(user.node_id)
      ? 'admin.fingerprintObservation.collapseUser'
      : 'admin.fingerprintObservation.expandUser',
    { name: userLabel(user) }
  )
}

function apiKeyToggleLabel(apiKey: FingerprintObservationAPIKeySummary): string {
  return t(
    expandedAPIKeys.value.has(apiKey.node_id)
      ? 'admin.fingerprintObservation.collapseApiKey'
      : 'admin.fingerprintObservation.expandApiKey',
    { name: apiKeyLabel(apiKey) }
  )
}

function sessionToggleLabel(session: FingerprintObservationSessionSummary): string {
  return t(
    expandedSessions.value.has(session.node_id)
      ? 'admin.fingerprintObservation.collapseSession'
      : 'admin.fingerprintObservation.expandSession',
    { id: session.session_id || '—' }
  )
}

function threadToggleLabel(thread: FingerprintObservationThreadSummary): string {
  const expanded = expandedThreads.value.has(thread.node_id)
  const key =
    isUnthreaded(thread)
      ? expanded
        ? 'admin.fingerprintObservation.collapseUnthreaded'
        : 'admin.fingerprintObservation.expandUnthreaded'
      : expanded
        ? 'admin.fingerprintObservation.collapseThread'
        : 'admin.fingerprintObservation.expandThread'
  return t(key, { id: thread.thread_id || '—' })
}

function threadRelationLabel(thread: FingerprintObservationThreadSummary): string {
  if (isUnthreaded(thread)) return t('admin.fingerprintObservation.unthreaded')
  return thread.relation === 'root'
    ? t('admin.fingerprintObservation.rootThread')
    : t('admin.fingerprintObservation.childThread')
}

function threadRelationClass(thread: FingerprintObservationThreadSummary): string {
  const base = 'inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold '
  if (isUnthreaded(thread)) {
    return base + 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
  return thread.relation === 'root'
    ? base + 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : base + 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}

function isUnthreaded(thread: FingerprintObservationThreadSummary): boolean {
  return thread.unthreaded === true || thread.relation === 'unthreaded'
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
  invalidateAllRequests()
  clearHierarchy()
})
</script>
