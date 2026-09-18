<template>
  <details class="min-w-0 max-w-[calc(100vw-4rem)] rounded-lg border border-gray-200 p-3 dark:border-dark-700 sm:max-w-none" data-testid="egress-location-details" @toggle="open = ($event.target as HTMLDetailsElement).open">
    <summary class="cursor-pointer break-words font-medium text-gray-700 dark:text-gray-200">
      {{ t(`${prefix}.title`) }} · {{ t(`${prefix}.status.${location.status}`) }}
      <span class="ml-2 font-normal">{{ [location.country_code, location.region, location.city].filter(Boolean).join(' / ') }} · {{ location.timezone }}</span>
    </summary>
    <template v-if="open">
      <p class="mt-2 break-words text-gray-500 dark:text-gray-400">{{ t(`${prefix}.hints.${location.status}`) }}</p>
      <dl class="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div v-for="item in items" :key="item.key" class="min-w-0">
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.fields.${item.key}`) }}</dt>
          <dd class="mt-1 break-all text-gray-800 dark:text-gray-200">{{ item.value }}</dd>
        </div>
      </dl>
    </template>
  </details>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAIEgressLocation } from '@/api/admin/fingerprintObservations'

const props = defineProps<{ location: OpenAIEgressLocation }>()
const { t, te, locale } = useI18n()
const prefix = 'admin.fingerprintObservation.request.egressLocation'
const open = ref(false)
const items = computed(() => {
  const snapshot = props.location
  const date = snapshot.checked_at ? new Date(snapshot.checked_at) : undefined
  const reasonKey = `${prefix}.reasons.${snapshot.reason}`
  const values = [
    ['route', `${t(`${prefix}.routes.${snapshot.route_type}`)}${snapshot.proxy_id ? ` #${snapshot.proxy_id}` : ''}`],
    ['ip', snapshot.ip_address || '—'],
    ['source', t(`${prefix}.sources.${snapshot.source}`)],
    ['country', [snapshot.country, snapshot.country_code].filter(Boolean).join(' / ')],
    ['region', snapshot.region], ['city', snapshot.city], ['timezone', snapshot.timezone],
    ['checkedAt', date && !Number.isNaN(date.getTime()) ? date.toLocaleString(locale.value) : t(`${prefix}.notCollected`)],
  ]
  if (snapshot.reason) values.push(['reason', te(reasonKey) ? t(reasonKey) : t(`${prefix}.reasonUnknown`)])
  return values.map(([key, value]) => ({ key, value: value || '—' }))
})
</script>
