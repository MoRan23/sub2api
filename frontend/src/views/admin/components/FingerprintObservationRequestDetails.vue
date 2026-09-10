<template>
  <details class="rounded-lg border border-gray-200 bg-gray-50/60 text-xs dark:border-dark-700 dark:bg-dark-900/30" @toggle="onDetailsToggle">
    <summary class="cursor-pointer rounded-lg px-3 py-2 text-gray-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-gray-300">
      <span class="font-medium">{{ t(`${prefix}.details`) }}</span>
      <span class="ml-3">{{ t(`${prefix}.comparison.${observation.timezone_comparison_status ?? 'not_collected'}`) }}</span>
      <span class="ml-3">{{ t(`${prefix}.residency`) }}: {{ residencyValue }}</span>
    </summary>

    <div v-if="detailsOpen" class="space-y-4 border-t border-gray-200 p-3 dark:border-dark-700">
      <dl class="grid gap-3 sm:grid-cols-3">
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.event`) }}</dt>
          <dd class="mt-1 text-gray-800 dark:text-gray-200">{{ eventLabel }}</dd>
        </div>
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.target`) }}</dt>
          <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ observation.timezone_target || t(`${prefix}.notCollected`) }}</dd>
        </div>
        <div>
          <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.residency`) }}</dt>
          <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ residencyValue }}</dd>
          <dd v-if="observation.outbound_codex_residency_source === 'ws_handshake'" class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.residencyHandshake`) }}</dd>
        </div>
      </dl>
      <p v-if="observation.event_kind === 'ws_response_create'" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.frameAttempt`) }}</p>

      <div class="grid gap-3 lg:grid-cols-2">
        <section v-for="direction in directions" :key="direction.key" :aria-label="t(`${prefix}.${direction.key}`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700">
          <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.${direction.key}`) }}</h3>
          <p class="mt-1 text-gray-500 dark:text-gray-400">{{ scanLabel(direction.scan) }}</p>
          <ul v-if="direction.scan?.items?.length" class="mt-3 space-y-3">
            <li v-for="(item, index) in direction.scan.items" :key="`${item.path}-${index}`" class="space-y-1 border-t border-gray-200 pt-2 dark:border-dark-700">
              <div class="flex flex-wrap gap-2 font-medium text-gray-700 dark:text-gray-300">
                <span>{{ sourceLabel(item.source) }}</span>
                <span v-if="item.source === 'environment_context'" class="font-normal text-gray-500 dark:text-gray-400">{{ t(`${prefix}.${item.current ? 'currentEnvironment' : 'historicalEnvironment'}`) }}</span>
                <span v-if="item.status === 'invalid'" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.invalidValue`) }}</span>
              </div>
              <div class="break-all font-mono text-gray-500 dark:text-gray-400">{{ item.path }}</div>
              <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
                <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.timezone`) }}</dt>
                <dd class="break-all font-mono text-gray-800 dark:text-gray-200">{{ item.value || '—' }}</dd>
                <template v-if="item.current_date !== undefined">
                  <dt class="text-gray-500 dark:text-gray-400">current_date</dt>
                  <dd class="break-all font-mono text-gray-800 dark:text-gray-200">{{ item.current_date || '—' }}</dd>
                </template>
              </dl>
              <p v-if="item.reason" class="break-words text-gray-500 dark:text-gray-400">{{ reasonLabel(item.reason) }}</p>
            </li>
          </ul>
        </section>
      </div>

      <section :aria-label="t(`${prefix}.conversions`)">
        <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.conversions`) }}</h3>
        <p v-if="!observation.timezone_conversions" class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.notCollected`) }}</p>
        <p v-else-if="observation.timezone_conversions.length === 0" class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.noConversions`) }}</p>
        <div v-else class="mt-2 overflow-x-auto">
          <table class="w-full min-w-[640px] text-left">
            <thead class="text-gray-500 dark:text-gray-400">
              <tr>
                <th class="px-2 py-2">{{ t(`${prefix}.location`) }}</th>
                <th class="px-2 py-2">{{ t(`${prefix}.before`) }}</th>
                <th class="px-2 py-2">{{ t(`${prefix}.after`) }}</th>
                <th class="px-2 py-2">{{ t(`${prefix}.result`) }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(conversion, index) in observation.timezone_conversions" :key="`${conversion.path}-${index}`" class="border-t border-gray-200 align-top dark:border-dark-700">
                <td class="max-w-80 space-y-1 break-all px-2 py-2">
                  <div>{{ sourceLabel(conversion.source) }}</div>
                  <div class="font-mono text-gray-500 dark:text-gray-400">{{ conversion.path }}</div>
                </td>
                <td class="max-w-60 space-y-1 break-all px-2 py-2 font-mono">
                  <div>{{ conversion.original || '—' }}</div>
                  <div v-if="conversion.date_before !== undefined">{{ conversion.date_before || '—' }}</div>
                </td>
                <td class="max-w-60 space-y-1 break-all px-2 py-2 font-mono">
                  <div>{{ conversion.output || '—' }}</div>
                  <div v-if="conversion.date_after !== undefined">{{ conversion.date_after || '—' }}</div>
                </td>
                <td class="max-w-96 space-y-1 break-words px-2 py-2">
                  <div>{{ t(`${prefix}.conversionStatus.${conversion.status}`) }}</div>
                  <div v-if="conversion.reason" class="text-gray-500 dark:text-gray-400">{{ reasonLabel(conversion.reason) }}</div>
                  <div v-if="conversion.time_basis === 'gateway_received_at'" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.dateBasis`) }}</div>
                  <div v-if="conversion.received_at" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.receivedAt`) }}: {{ formatSeattleTime(conversion.received_at) }}</div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FingerprintObservationEntry, RequestTimezoneScan, RequestTimezoneSource } from '@/api/admin/fingerprintObservations'

const props = defineProps<{ observation: FingerprintObservationEntry }>()
const { t, locale } = useI18n()
const prefix = 'admin.fingerprintObservation.request'
const detailsOpen = ref(false)
const directions = computed(() => [
  { key: 'inbound', scan: props.observation.inbound_timezone_observations },
  { key: 'outbound', scan: props.observation.outbound_timezone_observations },
] as const)
const residencyValue = computed(() => props.observation.outbound_codex_residency === undefined
  ? t(`${prefix}.notCollected`)
  : props.observation.outbound_codex_residency || t(`${prefix}.headerAbsent`))
const eventLabel = computed(() => t(`${prefix}.events.${props.observation.event_kind ?? 'unknown'}`))

function onDetailsToggle(event: Event): void {
  detailsOpen.value = (event.target as HTMLDetailsElement).open
}

function scanLabel(scan: RequestTimezoneScan | undefined): string {
  if (!scan) return t(`${prefix}.notCollected`)
  if (scan.scan_status === 'complete' && !scan.items?.length) return t(`${prefix}.notFound`)
  return t(`${prefix}.scan.${scan.scan_status}`)
}

function sourceLabel(source: RequestTimezoneSource): string {
  return t(`${prefix}.sources.${source}`)
}

const knownReasons = new Set([
  'conversion_disabled', 'historical', 'target_timezone_unavailable', 'accepted_at_unavailable',
  'patch_failed', 'timezone_converted', 'already_target', 'scan_limited', 'scan_parse_failed',
  'scan_not_applicable', 'environment_not_standalone', 'malformed_or_duplicate_tags',
  'timezone_missing', 'invalid_timezone', 'invalid_current_date', 'timezone_null', 'timezone_not_string',
  'not_sent', 'unmatched',
  'adapter_removed_source', 'source_not_in_final_body', 'source_path_changed',
  'ambiguous_source_mapping', 'final_value_differs',
  'value_not_observable', 'quoted_xml_content', 'source_changed_before_apply',
])

function reasonLabel(reason: string): string {
  return knownReasons.has(reason) ? t(`${prefix}.reasons.${reason}`) : reason
}

function formatSeattleTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(locale?.value || 'en-US', {
    timeZone: 'America/Los_Angeles', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZoneName: 'short',
  }).format(date)
}
</script>
