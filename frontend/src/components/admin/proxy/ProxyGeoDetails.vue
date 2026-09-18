<template>
  <div class="min-w-0 max-w-72 space-y-1 whitespace-normal" data-testid="proxy-geo-details">
    <div class="break-words text-sm text-gray-700 dark:text-gray-200">{{ location || '—' }}</div>
    <div class="break-all font-mono text-xs text-gray-500 dark:text-gray-400">{{ geo.timezone || t(`${prefix}.timezoneUnknown`) }}</div>
    <span class="inline-flex rounded px-1.5 py-0.5 text-xs" :class="geo.geo_status === 'success' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'">
      {{ t(`${prefix}.status.${geo.geo_status || 'unknown'}`) }}
    </span>
    <details class="text-xs" @toggle="open = ($event.target as HTMLDetailsElement).open">
      <summary class="cursor-pointer text-gray-500 dark:text-gray-400">{{ t(`${prefix}.details`) }}</summary>
      <dl v-if="open" class="mt-2 space-y-2">
        <div class="min-w-0">
          <dt class="text-gray-400">{{ t(`${prefix}.ip`) }}</dt>
          <dd class="break-all font-mono text-gray-700 dark:text-gray-200">{{ geo.ip_address || '—' }}</dd>
        </div>
        <div>
          <dt class="text-gray-400">{{ t(`${prefix}.source`) }}</dt>
          <dd class="text-gray-700 dark:text-gray-200">{{ geo.geo_status ? t(`${prefix}.sourceValue`) : t(`${prefix}.notCollected`) }}</dd>
        </div>
        <div>
          <dt class="text-gray-400">{{ t(`${prefix}.checkedAt`) }}</dt>
          <dd class="break-words text-gray-700 dark:text-gray-200">{{ checkedAt }}</dd>
        </div>
        <div v-if="geo.geo_reason">
          <dt class="text-gray-400">{{ t(`${prefix}.reason`) }}</dt>
          <dd class="break-words text-amber-700 dark:text-amber-400">{{ reason }}</dd>
        </div>
      </dl>
      <p v-if="open && geo.geo_status === 'failed'" class="mt-2 break-words text-gray-500 dark:text-gray-400">{{ t(`${prefix}.failedHint`) }}</p>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProxyGeoInfo } from '@/types'

const props = defineProps<{ geo: ProxyGeoInfo }>()
const { t, te, locale } = useI18n()
const prefix = 'admin.proxies.geo'
const open = ref(false)
const location = computed(() => [...new Set([props.geo.country || props.geo.country_code, props.geo.region, props.geo.city].filter(Boolean))].join(' · '))
const checkedAt = computed(() => {
  if (!props.geo.geo_checked_at) return t(`${prefix}.notCollected`)
  const date = new Date(props.geo.geo_checked_at)
  return Number.isNaN(date.getTime()) ? t(`${prefix}.notCollected`) : date.toLocaleString(locale.value)
})
const reason = computed(() => {
  const key = `${prefix}.reasons.${props.geo.geo_reason}`
  return te(key) ? t(key) : t(`${prefix}.reasonUnknown`)
})
</script>
