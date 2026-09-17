<template>
  <dl class="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1" data-testid="search-location-fields">
    <template v-for="field in fields" :key="field">
      <dt class="text-gray-500 dark:text-gray-400">{{ t(`admin.fingerprintObservation.request.searchLocation.fields.${field}`) }}</dt>
      <dd class="min-w-0 break-all font-mono text-gray-800 dark:text-gray-200" :data-location-field="field">{{ location[field] || '—' }}</dd>
    </template>
  </dl>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { RequestSearchLocation } from '@/api/admin/fingerprintObservations'

defineProps<{ location: RequestSearchLocation }>()
const { t } = useI18n()
const fields = ['type', 'country', 'region', 'city', 'timezone'] as const
</script>
