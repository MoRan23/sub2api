<template>
  <section :class="compact ? 'text-xs' : 'rounded-xl border border-gray-200 p-4 dark:border-dark-600'" data-test="enforcement-status">
    <h4 v-if="!compact" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.promptAudit.enforcement.eventTitle') }}</h4>
    <p v-if="event.matched_scanners.includes('keyword')" class="mt-1 text-gray-700 dark:text-dark-200">{{ t('admin.promptAudit.enforcement.keywordKind') }}: <span>{{ keywordLabel }}</span></p>
    <p v-if="!event.enforcement" class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.enforcement.notRecorded') }}</p>
    <template v-else>
      <p v-if="!compact" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.enforcement.snapshotHint') }}</p>
      <p v-else class="mt-1 text-gray-600 dark:text-dark-300">{{ t('admin.promptAudit.enforcement.countSummary', { count: event.enforcement.violation_count, threshold: event.enforcement.ban_threshold, hours: event.enforcement.window_hours }) }}</p>
      <dl :class="compact ? 'mt-2 space-y-1' : 'mt-3 grid grid-cols-1 gap-3 text-sm sm:grid-cols-2'">
        <div v-for="row in rows" :key="row.key">
          <dt :class="compact ? 'inline text-gray-500 dark:text-dark-400' : 'text-gray-500 dark:text-dark-400'">{{ t(`admin.promptAudit.enforcement.${row.key}`) }}{{ compact ? ': ' : '' }}</dt>
          <dd :class="compact ? 'inline break-words text-gray-900 dark:text-white' : 'mt-1 break-words text-gray-900 dark:text-white'">{{ row.value }}</dd>
        </div>
      </dl>
      <p v-if="event.enforcement.reason && !compact" class="mt-3 break-words text-sm text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.enforcement.reason') }}: {{ reasonLabel }}</p>
      <div v-if="canUnban" class="mt-4 space-y-2">
        <button type="button" class="btn btn-primary btn-sm" :disabled="disabled || unbanning" :aria-busy="unbanning" @click="unban">
          {{ t(unbanning ? 'admin.promptAudit.enforcement.unbanning' : 'admin.promptAudit.enforcement.unban') }}
        </button>
        <p v-if="!compact" class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.enforcement.unbanHint') }}</p>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptAuditEvent } from '../types'

const props = withDefaults(defineProps<{ event: PromptAuditEvent; unbanning?: boolean; disabled?: boolean; compact?: boolean }>(), { unbanning: false, disabled: false, compact: false })
const emit = defineEmits<{ unban: [userId: number] }>()
const { t } = useI18n()
const knownEmailStatuses = new Set(['not_requested', 'queued', 'sending', 'retry', 'sent', 'failed', 'cancelled'])
const knownUserStatuses = new Set(['active', 'disabled', 'deleted'])
const canUnban = computed(() => !!props.event.enforcement?.can_unban && props.event.enforcement.user_status === 'disabled' && Number.isSafeInteger(props.event.snapshot.user_id) && props.event.snapshot.user_id > 0)
const keywordLabel = computed(() => t(`admin.promptAudit.enforcement.keywordKinds.${props.event.keyword_kind === 'jailbreak' || props.event.keyword_kind === 'other' ? props.event.keyword_kind : 'unknown'}`))
const reasonLabel = computed(() => {
  const reason = props.event.enforcement?.reason || ''
  return reason === 'cycle_reset' || reason === 'already_active' ? t(`admin.promptAudit.enforcement.reasons.${reason}`) : reason
})
const rows = computed(() => {
  const state = props.event.enforcement
  if (!state) return []
  const yesNo = (value: boolean) => t(`admin.promptAudit.enforcement.${value ? 'yes' : 'no'}`)
  const statusLabel = (value: string, known: Set<string>, prefix: string) => value
    ? known.has(value) ? t(`admin.promptAudit.enforcement.${prefix}.${value}`) : t('admin.promptAudit.enforcement.unknownStatus', { value })
    : t('admin.promptAudit.enforcement.notRecorded')
  return [
    { key: 'recorded', value: yesNo(state.recorded) },
    { key: 'counted', value: yesNo(state.counted) },
    { key: 'violationCount', value: String(state.violation_count) },
    { key: 'eventWindow', value: t('admin.promptAudit.enforcement.hours', { count: state.window_hours }) },
    { key: 'eventThreshold', value: String(state.ban_threshold) },
    { key: 'autoBanned', value: yesNo(state.auto_banned) },
    { key: 'epoch', value: String(state.epoch) },
    { key: 'userStatus', value: statusLabel(state.user_status, knownUserStatuses, 'userStatuses') },
    { key: 'emailStatus', value: statusLabel(state.email_status, knownEmailStatuses, 'emailStatuses') },
  ].filter(row => !props.compact || ['autoBanned', 'userStatus', 'emailStatus'].includes(row.key))
})

function unban() {
  if (canUnban.value && !props.disabled && !props.unbanning) emit('unban', props.event.snapshot.user_id)
}
</script>
