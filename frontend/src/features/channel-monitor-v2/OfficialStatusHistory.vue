<template>
  <div class="mt-2" :aria-label="t('channelMonitorV2.officialStatus.historyLabel', { product: group.name })">
    <div v-if="days.length" class="flex h-6 gap-px" data-testid="official-history">
      <button
        v-for="day in days"
        :key="day.date"
        type="button"
        class="min-w-0 flex-1 rounded-sm focus-visible:z-10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500"
        :class="statusClasses(day.status).dot"
        :aria-label="`${day.date} UTC · ${statusLabel(day.status)}`"
        :data-official-day="day.date"
        :data-state="day.status"
        @mouseenter="showDay($event, day)"
        @focus="showDay($event, day)"
        @click="showDay($event, day, true)"
        @mouseleave="tooltip?.hide(owner, $event.currentTarget)"
        @blur="tooltip?.dismiss(owner)"
      />
    </div>
    <div v-else class="flex h-6 items-center justify-center rounded-sm bg-gray-100 text-[10px] text-gray-500 dark:bg-dark-700 dark:text-gray-400" data-testid="official-history-empty">
      {{ t('channelMonitorV2.officialStatus.noHistory') }}
    </div>
    <div class="mt-1 flex items-center justify-between gap-2 text-[10px] text-gray-500 dark:text-gray-400">
      <span>{{ t('channelMonitorV2.officialStatus.historyPast') }}</span>
      <span v-if="uptime !== null" data-testid="official-history-uptime">{{ t('channelMonitorV2.officialStatus.historyUptime', { value: uptime }) }}</span>
      <span v-if="lastDay" data-testid="official-history-end">{{ lastDay === today ? t('channelMonitorV2.officialStatus.historyToday') : lastDay }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OfficialStatusGroup, OfficialStatusHistory } from '@/api/officialStatus'
import { overviewTooltipKey } from './overviewTooltip'

const props = defineProps<{
  group: OfficialStatusGroup
  today: string
  statusLabel: (status: string) => string
  statusClasses: (status: string) => { dot: string; text: string }
}>()
const { t, locale } = useI18n()
const tooltip = inject(overviewTooltipKey, undefined)
const owner = { apis: 1, chatgpt: 2, codex: 3 }[props.group.id]
const days = computed(() => {
  const history = props.group.history?.days || []
  return history.length > 91 ? history.slice(-91) : history
})
const lastDay = computed(() => days.value.at(-1)?.date)
const numberFormatter = computed(() => new Intl.NumberFormat(locale.value || undefined, { maximumFractionDigits: 3 }))
const uptime = computed(() => {
  const value = props.group.history?.uptime_percent
  return days.value.length && typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 100
    ? numberFormatter.value.format(value)
    : null
})
function showDay(event: MouseEvent | FocusEvent, day: OfficialStatusHistory['days'][number], pinned = false) {
  // Dates are official UTC calendar days, not instants in the viewer's timezone.
  tooltip?.show(event, owner, () => [`${day.date} UTC`, props.statusLabel(day.status)], pinned)
}
watch(() => [props.group.history, locale.value], () => tooltip?.dismiss(owner))
onBeforeUnmount(() => tooltip?.dismiss(owner))
</script>
