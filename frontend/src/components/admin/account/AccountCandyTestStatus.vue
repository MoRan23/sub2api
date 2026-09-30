<template>
  <span class="inline-flex rounded px-1.5 py-0.5 text-xs font-medium" :class="statusClass">
    {{ t(`candyTests.${status}`) }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CandyTestStatus } from '@/api/admin/candyTests'

const props = defineProps<{ status: CandyTestStatus }>()
const { t } = useI18n()
const statusClass = computed(() => {
  if (props.status === 'generated') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (props.status === 'abnormal') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (props.status === 'queued' || props.status === 'running') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
})
</script>
