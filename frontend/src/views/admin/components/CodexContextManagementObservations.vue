<template>
  <section class="flex h-full min-h-[320px] flex-col" :aria-label="t('admin.fingerprintObservation.contextManagement.title')">
    <div class="shrink-0 border-b border-gray-200 p-4 dark:border-dark-700">
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.fingerprintObservation.contextManagement.hint') }}</p>
      <dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-5">
        <div v-for="metric in metrics" :key="metric.key" class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/60">
          <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t(`admin.fingerprintObservation.contextManagement.metrics.${metric.key}`) }}</dt>
          <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ metric.value }}</dd>
        </div>
      </dl>
    </div>

    <div v-if="error" role="alert" class="flex items-center justify-between gap-3 border-b border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">
      <span>{{ error }}</span>
      <button type="button" class="shrink-0 font-medium underline" :disabled="loading" @click="emit('retry')">{{ t('admin.fingerprintObservation.retry') }}</button>
    </div>

    <div class="min-h-0 flex-1 overflow-auto" :aria-busy="loading">
      <div v-if="loading && !items.length" class="flex min-h-64 items-center justify-center gap-2 text-sm text-gray-500 dark:text-gray-400">
        <Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}
      </div>
      <ul v-else-if="items.length" class="divide-y divide-gray-200 dark:divide-dark-700">
        <li v-for="event in items" :key="event.sequence_id" class="px-4 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex flex-wrap items-center gap-2 text-xs">
              <span class="rounded-full bg-indigo-100 px-2 py-0.5 font-semibold text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300">{{ event.kind }}</span>
              <span :class="statusClass(event.status)">{{ statusLabel(event.status) }}</span>
              <span v-if="event.http_status" class="font-mono text-gray-500 dark:text-gray-400">HTTP {{ event.http_status }}</span>
              <span v-if="event.upstream_sent && event.upstream_http_status" class="font-mono text-gray-500 dark:text-gray-400">{{ t('admin.fingerprintObservation.contextManagement.upstreamStatus', { status: event.upstream_http_status }) }}</span>
              <span class="text-gray-500 dark:text-gray-400">{{ event.duration_ms }} ms</span>
              <span v-if="event.fallback" class="text-amber-600 dark:text-amber-400">{{ t('admin.fingerprintObservation.contextManagement.fallback') }}</span>
            </div>
            <time class="text-xs text-gray-400" :datetime="event.timestamp">{{ formatTime(event.timestamp) }}</time>
          </div>
          <div class="mt-2 break-all font-mono text-xs text-gray-700 dark:text-gray-300">{{ event.path }}</div>
          <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t('admin.fingerprintObservation.columns.actor') }}: {{ event.username || event.email || `#${event.user_id}` }} / {{ event.api_key_name || `#${event.api_key_id}` }}</span>
            <span v-if="event.account_id">{{ t('admin.fingerprintObservation.columns.account') }}: {{ event.account_name || `#${event.account_id}` }}</span>
            <span v-if="event.upstream_sent">{{ event.sticky_hit ? t('admin.fingerprintObservation.contextManagement.stickyHit', { source: event.sticky_source }) : t('admin.fingerprintObservation.contextManagement.stickyMiss') }}</span>
            <span v-if="event.upstream_sent && event.attempt">{{ t('admin.fingerprintObservation.contextManagement.attempt', { count: event.attempt }) }}</span>
            <span>{{ t('admin.fingerprintObservation.contextManagement.deliveredBytes', { count: event.delivered_bytes }) }}</span>
          </div>
          <p v-if="event.error_kind" class="mt-2 break-all text-xs text-red-600 dark:text-red-400">{{ t('admin.fingerprintObservation.contextManagement.errorKind') }}: {{ event.error_kind }}</p>

          <details class="mt-3 rounded-lg border border-gray-200 dark:border-dark-700" @toggle="onDetailsToggle(event.sequence_id, $event)">
            <summary class="cursor-pointer px-3 py-2 text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.fingerprintObservation.contextManagement.details') }}
              <span class="ml-2 font-normal text-gray-400">{{ t('admin.fingerprintObservation.contextManagement.rewriteCount', { count: event.rewrite_fields?.length || 0 }) }}</span>
            </summary>
            <div class="space-y-3 border-t border-gray-200 p-3 dark:border-dark-700">
              <dl class="grid gap-2 text-xs sm:grid-cols-2">
                <div v-for="identity in identityFields(event)" :key="identity.field">
                  <dt class="font-medium text-gray-400">{{ identity.field }}</dt>
                  <dd class="mt-0.5 break-all font-mono text-gray-700 dark:text-gray-300">{{ identity.value || '—' }}</dd>
                </div>
              </dl>
              <div v-if="event.rewrites?.length" class="overflow-x-auto">
                <table class="w-full text-left text-xs">
                  <thead class="text-gray-400"><tr><th class="pb-2 pr-3 font-medium">{{ t('admin.fingerprintObservation.contextManagement.field') }}</th><th class="pb-2 pr-3 font-medium">{{ t('admin.fingerprintObservation.contextManagement.before') }}</th><th class="pb-2 font-medium">{{ t('admin.fingerprintObservation.contextManagement.after') }}</th></tr></thead>
                  <tbody class="font-mono text-gray-700 dark:text-gray-300"><tr v-for="(rewrite, index) in event.rewrites" :key="`${rewrite.field}-${index}`"><td class="break-all py-1 pr-3 align-top">{{ rewrite.field }}</td><td class="break-all py-1 pr-3 align-top">{{ rewrite.before || '—' }}</td><td class="break-all py-1 align-top">{{ rewrite.after || '—' }}</td></tr></tbody>
                </table>
              </div>
              <p v-else class="text-xs text-gray-400">{{ event.rewrite_fields?.length ? event.rewrite_fields.join(', ') : t('admin.fingerprintObservation.contextManagement.noRewrites') }}</p>
            </div>
          </details>
        </li>
      </ul>
      <div v-else class="flex min-h-64 items-center justify-center px-4 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ enabled ? t('admin.fingerprintObservation.contextManagement.empty') : t('admin.fingerprintObservation.emptyOff') }}
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexContextManagementEvent, CodexContextManagementSummary } from '@/api/admin/fingerprintObservations'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  enabled: boolean
  items: CodexContextManagementEvent[]
  summary: CodexContextManagementSummary | null
  loading: boolean
  error: string
}>()
const emit = defineEmits<{ retry: []; detailsChanged: [expanded: boolean] }>()
const { t } = useI18n()
const openedDetails = new Set<number>()
const metrics = computed(() => (['total', 'successes', 'failures', 'fallbacks', 'rewritten'] as const).map((key) => ({ key, value: props.summary?.[key] ?? 0 })))

function onDetailsToggle(sequence: number, event: Event): void {
  if ((event.target as HTMLDetailsElement).open) openedDetails.add(sequence)
  else openedDetails.delete(sequence)
  emit('detailsChanged', openedDetails.size > 0)
}

function statusClass(status: string): string {
  return status === 'delivered'
    ? 'font-semibold text-green-700 dark:text-green-300'
    : 'font-semibold text-red-700 dark:text-red-300'
}

function statusLabel(status: string): string {
  if (['delivered', 'failed', 'rejected', 'disabled'].includes(status)) {
    return t(`admin.fingerprintObservation.contextManagement.status.${status}`)
  }
  return status
}

function identityFields(event: CodexContextManagementEvent): Array<{ field: string; value: string | undefined }> {
  return [
    { field: 'session_id', value: event.session_id },
    { field: 'thread_id', value: event.thread_id },
    { field: 'window_id', value: event.window_id },
    { field: 'context_window_id', value: event.context_window_id },
  ]
}

function formatTime(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}
</script>
