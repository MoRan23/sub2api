<template>
  <details class="rounded-lg border border-gray-200 bg-gray-50/60 text-xs dark:border-dark-700 dark:bg-dark-900/30" @toggle="onDetailsToggle">
    <summary class="cursor-pointer rounded-lg px-3 py-2 text-gray-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-gray-300">
      <span class="font-medium">{{ t(`${prefix}.details`) }}</span>
      <span class="ml-3">{{ t(`${prefix}.comparison.${observation.timezone_comparison_status ?? 'not_collected'}`) }}</span>
      <span class="ml-3">{{ t(`${prefix}.residency`) }}: {{ residencyValue }}</span>
      <span v-if="observation.request_integrity" class="ml-3" data-testid="request-integrity-summary">
        {{ t(`${integrityPrefix}.title`) }}: {{ t(`${integrityPrefix}.status.${observation.request_integrity.status}`) }}
      </span>
      <span v-if="observation.conversion_check" class="ml-3" data-testid="conversion-check-summary">
        {{ t(`${conversionPrefix}.title`) }}: {{ t(`${conversionPrefix}.status.${observation.conversion_check.status}`) }}
      </span>
      <span v-if="hasOutboundSearchLocation" class="ml-3" data-testid="search-location-summary">{{ t(`${prefix}.searchLocation.recorded`) }}</span>
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
      <OpenAIEgressLocationDetails v-if="observation.egress_location" :location="observation.egress_location" />
      <CodexTurnStateObservationDetails v-if="observation.codex_turn_state" :state="observation.codex_turn_state" />
      <p v-if="observation.event_kind === 'ws_response_create'" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.frameAttempt`) }}</p>

      <section v-if="observation.conversion_check" :aria-label="t(`${conversionPrefix}.title`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="conversion-check-details">
        <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${conversionPrefix}.title`) }}</h3>
        <p class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${conversionPrefix}.description`) }}</p>
        <p class="mt-2 font-medium" :class="observation.conversion_check.status === 'known_loss' ? 'text-amber-700 dark:text-amber-400' : 'text-gray-800 dark:text-gray-200'">{{ t(`${conversionPrefix}.status.${observation.conversion_check.status}`) }}</p>
        <ul v-if="observation.conversion_check.issues?.length" class="mt-3 space-y-2">
          <li v-for="(issue, index) in observation.conversion_check.issues" :key="index" class="min-w-0 rounded-md bg-gray-100 p-2 dark:bg-dark-800">
            <div class="break-all font-mono text-gray-800 dark:text-gray-200">{{ issue.path || '—' }}</div>
            <div class="mt-1 break-words text-gray-600 dark:text-gray-300">{{ conversionReasonLabel(issue.reason) }}</div>
          </li>
        </ul>
      </section>

      <section v-if="observation.request_integrity" :aria-label="t(`${integrityPrefix}.title`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="request-integrity-details">
        <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${integrityPrefix}.title`) }}</h3>
        <p class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${integrityPrefix}.description`) }}</p>
        <dl class="mt-3 grid gap-x-4 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="item in integrityItems" :key="item.key" class="min-w-0">
            <dt class="text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
            <dd class="mt-1 break-words text-gray-800 dark:text-gray-200">{{ item.value }}</dd>
          </div>
        </dl>
        <p v-if="observation.request_integrity.baseline_stage === 'responses_adapter_output'" class="mt-3 text-gray-500 dark:text-gray-400">{{ t(`${integrityPrefix}.adapterBoundary`) }}</p>
        <div v-if="observation.request_integrity.changed_fields?.length" class="mt-3">
          <h4 class="text-gray-500 dark:text-gray-400">{{ t(`${integrityPrefix}.fields`) }}</h4>
          <ul class="mt-1 space-y-1 break-all font-mono text-gray-800 dark:text-gray-200">
            <li v-for="(field, index) in observation.request_integrity.changed_fields" :key="index">{{ field }}</li>
          </ul>
        </div>
        <div v-if="observation.request_integrity.rule_codes?.length" class="mt-3">
          <h4 class="text-gray-500 dark:text-gray-400">{{ t(`${integrityPrefix}.rules`) }}</h4>
          <ul class="mt-1 space-y-1 break-words text-gray-800 dark:text-gray-200">
            <li v-for="(rule, index) in observation.request_integrity.rule_codes" :key="index">{{ integrityReasonLabel(rule) }}</li>
          </ul>
        </div>
        <p v-if="observation.request_integrity.reason" class="mt-3 break-words text-gray-600 dark:text-gray-300">{{ t(`${integrityPrefix}.reason`) }}: {{ integrityReasonLabel(observation.request_integrity.reason) }}</p>
        <p v-if="observation.request_integrity.truncated" class="mt-3 text-amber-700 dark:text-amber-400">{{ t(`${integrityPrefix}.truncated`) }}</p>
      </section>

      <section class="rounded-lg border border-gray-200 p-3 dark:border-dark-700">
        <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.codexMetadata`) }}</h3>
        <dl class="mt-2 grid gap-x-4 gap-y-2 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="item in metadataItems" :key="item.key" class="min-w-0">
            <dt class="text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
            <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ item.value || '—' }}</dd>
          </div>
        </dl>
        <CodexOutboundMetadataDetails :observation="observation" class="mt-3" />
      </section>

      <div class="grid gap-3 lg:grid-cols-2">
        <section v-for="direction in directions" :key="direction.key" :aria-label="t(`${prefix}.${direction.key}`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700">
          <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.${direction.key}`) }}</h3>
          <p class="mt-1 text-gray-500 dark:text-gray-400">{{ scanLabel(direction.scan) }}</p>
          <ul v-if="direction.scan?.items?.length" class="mt-3 space-y-3">
            <li v-for="(item, index) in direction.scan.items" :key="`${item.path}-${index}`" class="space-y-1 border-t border-gray-200 pt-2 dark:border-dark-700">
              <div class="flex flex-wrap gap-2 font-medium text-gray-700 dark:text-gray-300">
                <span>{{ sourceLabel(item.source) }}</span>
                <span v-if="item.source === 'environment_context'" class="font-normal text-gray-500 dark:text-gray-400">{{ environmentLabel(item) }}</span>
                <span v-if="item.status === 'invalid' && item.source !== 'environment_context'" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.invalidValue`) }}</span>
                <span v-else-if="item.status === 'invalid' && isQualifiedEnvironment(item.environment_source)" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.environmentIncomplete`) }}</span>
              </div>
              <div class="break-all font-mono text-gray-500 dark:text-gray-400">{{ item.path }}</div>
              <SearchLocationDetails v-if="item.location" :location="item.location" />
              <dl v-else class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
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
                  <div v-if="conversion.source === 'environment_context' && conversion.environment_source === 'reference'" class="font-normal text-gray-500 dark:text-gray-400">{{ t(`${prefix}.referenceEnvironment`) }}</div>
                  <div v-else-if="conversion.source === 'environment_context' && conversion.environment_source === 'structural_fallback'" class="font-normal text-gray-500 dark:text-gray-400">{{ t(`${prefix}.structuralFallbackEnvironment`) }}</div>
                  <div class="font-mono text-gray-500 dark:text-gray-400">{{ conversion.path }}</div>
                </td>
                <td class="max-w-60 space-y-1 break-all px-2 py-2 font-mono">
                  <SearchLocationDetails v-if="conversion.location_before" :location="conversion.location_before" />
                  <div v-else>{{ conversion.original || '—' }}</div>
                  <div v-if="conversion.date_before !== undefined">{{ conversion.date_before || '—' }}</div>
                </td>
                <td class="max-w-60 space-y-1 break-all px-2 py-2 font-mono">
                  <SearchLocationDetails v-if="conversion.location_after" :location="conversion.location_after" />
                  <div v-else>{{ conversion.output || '—' }}</div>
                  <div v-if="conversion.date_after !== undefined">{{ conversion.date_after || '—' }}</div>
                </td>
                <td class="max-w-96 space-y-1 break-words px-2 py-2">
                  <div>{{ t(`${prefix}.conversionStatus.${conversion.status}`) }}</div>
                  <div v-if="conversion.location_after && conversion.status === 'converted'" class="text-gray-600 dark:text-gray-300" data-testid="search-location-action">{{ t(`${prefix}.searchLocation.${conversion.location_added === true ? 'added' : 'replaced'}`) }}</div>
                  <div v-if="conversion.reason" class="text-gray-500 dark:text-gray-400">{{ reasonLabel(conversion.reason) }}</div>
                  <div v-if="conversion.time_basis === 'gateway_received_at'" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.dateBasis`) }}</div>
                  <div v-if="conversion.received_at" class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.receivedAt`) }}: {{ formatTargetTime(conversion.received_at) }}</div>
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
import type { FingerprintObservationEntry, RequestEnvironmentSource, RequestTimezoneObservation, RequestTimezoneScan, RequestTimezoneSource } from '@/api/admin/fingerprintObservations'
import CodexTurnStateObservationDetails from './CodexTurnStateObservationDetails.vue'
import SearchLocationDetails from './SearchLocationDetails.vue'
import CodexOutboundMetadataDetails from './CodexOutboundMetadataDetails.vue'
import OpenAIEgressLocationDetails from './OpenAIEgressLocationDetails.vue'

const props = defineProps<{ observation: FingerprintObservationEntry }>()
const { t, te, locale } = useI18n()
const prefix = 'admin.fingerprintObservation.request'
const integrityPrefix = `${prefix}.integrity`
const conversionPrefix = `${prefix}.conversionCheck`
const detailsOpen = ref(false)
const hasOutboundSearchLocation = computed(() => props.observation.outbound_timezone_observations?.items?.some(item => item.source === 'web_search' && item.location))
const directions = computed(() => [
  { key: 'inbound', scan: props.observation.inbound_timezone_observations },
  { key: 'outbound', scan: props.observation.outbound_timezone_observations },
] as const)
const residencyValue = computed(() => props.observation.outbound_codex_residency === undefined
  ? t(`${prefix}.notCollected`)
  : props.observation.outbound_codex_residency || t(`${prefix}.headerAbsent`))
const eventLabel = computed(() => t(`${prefix}.events.${props.observation.event_kind ?? 'unknown'}`))
const integrityItems = computed(() => {
  const entry = props.observation.request_integrity
  if (!entry) return []
  return [
    ['result', t(`${integrityPrefix}.status.${entry.status}`)],
    ['modeLabel', t(`${integrityPrefix}.modes.${entry.mode}`)],
    ['protocolLabel', t(`${integrityPrefix}.protocols.${entry.baseline_protocol}`)],
    ['stageLabel', t(`${integrityPrefix}.stages.${entry.baseline_stage}`)],
    ['attempt', String(entry.attempt)],
    ['transport', t(`${integrityPrefix}.transports.${entry.transport}`)],
  ].map(([key, value]) => ({ key, label: t(`${integrityPrefix}.${key}`), value }))
})

function integrityReasonLabel(reason: string): string {
  const key = `${integrityPrefix}.reasons.${reason}`
  return te(key) ? t(key) : reason
}

function conversionReasonLabel(reason: string): string {
  const key = `${conversionPrefix}.reasons.${reason}`
  return te(key) ? t(key) : reason
}
const metadataItems = computed(() => [
  ['routingOS', props.observation.routing_os_family ? { windows: 'Windows', macos: 'macOS', linux: 'Linux' }[props.observation.routing_os_family] : undefined],
  ['routingOSSource', props.observation.routing_os_source ? t(`${prefix}.routingOSSources.${props.observation.routing_os_source}`) : undefined],
  ['dailyRoot', props.observation.daily_fixed_root_enabled ? `${props.observation.daily_fixed_root_os_family ?? props.observation.daily_fixed_root_slot_index ?? '—'} / ${props.observation.daily_fixed_root_kind || '—'} / ${props.observation.daily_fixed_root_business_date || '—'}` : t(`${prefix}.disabled`)],
  ['dailyRootSession', props.observation.daily_fixed_root_session_id], ['window', props.observation.window_id],
  ['windowNumber', props.observation.window_number?.toString()], ['contextWindow', props.observation.context_window_id],
  ['turn', props.observation.turn_id], ['parentTurn', props.observation.parent_turn_id], ['rootTurn', props.observation.root_turn_id],
  ['parentThread', props.observation.parent_thread_id], ['forkedFrom', props.observation.forked_from_thread_id],
  ['agent', props.observation.agent_name], ['subagent', props.observation.subagent_kind || props.observation.openai_subagent],
  ['threadSource', props.observation.thread_source], ['turnTrigger', props.observation.turn_trigger],
  ['sandbox', props.observation.sandbox || props.observation.sandbox_mode],
  ['review', props.observation.auto_review_enabled === undefined ? undefined : String(props.observation.auto_review_enabled)],
  ['nodeReplAutoReviewRequired', props.observation.node_repl_auto_review_required?.toString()],
  ['nodeReplDisabled', props.observation.node_repl_disabled?.toString()],
  ['workspaces', props.observation.workspaces?.join(', ')],
].map(([key, value]) => ({ key, label: t(`${prefix}.${key}`), value })))

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

function isQualifiedEnvironment(source: RequestEnvironmentSource | undefined): boolean {
  return source === 'metadata' || source === 'mapped' || source === 'structural_fallback'
}

function environmentLabel(item: RequestTimezoneObservation): string {
  if (item.environment_source === 'reference') return t(`${prefix}.referenceEnvironment`)
  if (item.environment_source === 'structural_fallback') {
    return t(`${prefix}.${item.current ? 'structuralFallbackCurrentEnvironment' : 'structuralFallbackHistoricalEnvironment'}`)
  }
  if (!isQualifiedEnvironment(item.environment_source)) return t(`${prefix}.unclassifiedEnvironment`)
  return t(`${prefix}.${item.current ? 'currentEnvironment' : 'historicalEnvironment'}`)
}

const knownReasons = new Set([
  'conversion_disabled', 'historical', 'historical_timezone_converted', 'target_timezone_unavailable', 'accepted_at_unavailable',
  'patch_failed', 'timezone_converted', 'already_target', 'scan_limited', 'scan_parse_failed',
  'scan_not_applicable', 'environment_not_standalone', 'malformed_or_duplicate_tags',
  'timezone_missing', 'invalid_timezone', 'invalid_current_date', 'timezone_null', 'timezone_not_string',
  'not_sent', 'unmatched',
  'adapter_removed_source', 'source_not_in_final_body', 'source_path_changed',
  'ambiguous_source_mapping', 'final_value_differs',
  'value_not_observable', 'quoted_xml_content', 'source_changed_before_apply',
  'location_added', 'location_normalized', 'location_missing', 'location_container_not_object', 'environment_metadata_missing', 'standalone_search_source_required',
])

function reasonLabel(reason: string): string {
  return knownReasons.has(reason) ? t(`${prefix}.reasons.${reason}`) : reason
}

function formatTargetTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  const options: Intl.DateTimeFormatOptions = {
    timeZone: props.observation.timezone_target || 'UTC', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZoneName: 'short',
  }
  try {
    return new Intl.DateTimeFormat(locale?.value || 'en-US', options).format(date)
  } catch {
    // Legacy or malformed observations must not use the browser's own timezone.
    return new Intl.DateTimeFormat(locale?.value || 'en-US', { ...options, timeZone: 'UTC' }).format(date)
  }
}
</script>
