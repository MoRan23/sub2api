<template>
  <div :data-testid="`account-codex-turn-state-${account.id}`">
    <span v-if="!supported" class="text-sm text-gray-400 dark:text-dark-500" :title="t(`${prefix}.columnUnsupported`)">—</span>
    <button
      v-else type="button" aria-haspopup="dialog" :aria-label="t(`${prefix}.viewStatus`)"
      class="block w-44 max-w-full rounded py-0.5 text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
      :style="{ height: `${Math.max(1, visibleRows.length) * 20 + 4}px` }"
      data-testid="account-codex-turn-state-cell" @click.stop="$emit('open')"
    >
      <span
        v-for="(row, index) in visibleRows" :key="row.model"
        class="flex h-5 min-w-0 items-center gap-1.5 whitespace-nowrap"
        :data-testid="`codex-turn-state-model-${row.model}`"
        :title="row.description" :aria-label="row.description"
      >
        <span
          aria-hidden="true" class="h-2 w-2 shrink-0 rounded-full"
          :class="dotClasses[row.color]" :data-state="row.color" data-testid="codex-turn-state-dot"
        />
        <span class="min-w-0 flex-1 truncate font-mono text-gray-700 dark:text-gray-300">{{ row.model }}</span>
        <span
          v-if="index === visibleRows.length - 1 && additionalModels > 0" class="shrink-0 text-[11px] text-gray-500 dark:text-gray-400"
          :title="t(`${prefix}.columnMore`, { count: additionalModels })"
          :aria-label="t(`${prefix}.columnMore`, { count: additionalModels })"
          data-testid="codex-turn-state-more"
        >+{{ additionalModels }}</span>
      </span>
      <span v-if="!visibleRows.length" class="text-gray-400 dark:text-dark-500" :title="t(`${prefix}.columnEmptyList`)">—</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateModelStatus, CodexTurnStateObservation, CodexTurnStateStatus } from '@/api/admin/accounts'
import { supportsCodexTurnState } from '@/components/account/codexTurnState'

const props = defineProps<{
  account: AccountListItem
  status?: CodexTurnStateStatus
  models: string[]
  loading: boolean
  failed: boolean
  now: number
  observedAt: number
}>()
defineEmits<{ open: [] }>()
const { t, te } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const supported = computed(() => supportsCodexTurnState(props.account))
const defaultModels = ['gpt-6-astra', 'gpt-5.6-sol']
const dotClasses = {
  green: 'bg-emerald-500 dark:bg-emerald-400',
  yellow: 'bg-amber-500 dark:bg-amber-400',
  red: 'bg-red-500 dark:bg-red-400',
  gray: 'bg-gray-400 dark:bg-gray-500'
}
type DotColor = keyof typeof dotClasses
type ModelSummary = { model: string; cache?: CodexTurnStateModelStatus; observation?: CodexTurnStateObservation }
type Indicator = { color: DotColor; label: string }
const observedModelOrder = ref<string[]>([])
let observationOwner = ''

watch(() => [props.account.id, props.status?.owner_account_id, props.status?.observations] as const, () => {
  const owner = `${props.account.id}:${props.status?.owner_account_id ?? props.account.id}`
  if (owner !== observationOwner) observedModelOrder.value = []
  observationOwner = owner
  const names = new Set((props.status?.observations || []).map(item => item.model))
  const retained = observedModelOrder.value.filter(model => names.has(model))
  observedModelOrder.value = [...retained, ...[...names].filter(model => !retained.includes(model)).sort()]
}, { immediate: true })

function label(group: string, value: string) {
  const key = `${prefix}.${group}.${value}`
  return te(key) ? t(key) : value
}
const allModels = computed<ModelSummary[]>(() => {
  const cached = new Map((props.status?.models || []).map(model => [model.model, model]))
  const observed = new Map((props.status?.observations || []).map(model => [model.model, model]))
  const configured = props.models.length || props.status ? props.models : defaultModels
  const extras = observedModelOrder.value.filter(model => !configured.includes(model))
  const names = [...configured, ...extras]
  return [...new Set(names)].map(model => ({ model, cache: cached.get(model), observation: observed.get(model) }))
})
const additionalModels = computed(() => Math.max(0, allModels.value.length - 5))
const visibleRows = computed(() => allModels.value.slice(0, 5).map(model => {
  const state = indicator(model)
  const parts = [model.model, state.label]
  if (model.observation?.response_length) parts.push(t(`${prefix}.characters`, { count: model.observation.response_length }))
  if (model.observation?.observed_at) {
    const time = new Date(model.observation.observed_at)
    if (Number.isFinite(time.getTime())) parts.push(`${t(`${prefix}.observedAt`)}: ${time.toLocaleString()}`)
  }
  if (model.observation?.response_validation_reason) parts.push(label('validationReasons', model.observation.response_validation_reason))
  if (model.observation?.request_source) parts.push(`${t(`${prefix}.requestSource`)}: ${label('sources', model.observation.request_source)}`)
  if (props.status?.inherited) parts.push(t(`${prefix}.columnInherited`, { id: props.status.owner_account_id }))
  if (props.status && !props.status.enabled) parts.push(t(`${prefix}.passiveOnly`))
  return { model: model.model, color: state.color, description: parts.filter(Boolean).join(' · ') }
}))

function remaining(model: CodexTurnStateModelStatus): number {
  const expires = model.expires_at ? Date.parse(model.expires_at) : Number.NaN
  if (Number.isFinite(expires)) return Math.max(0, Math.ceil((expires - Math.max(props.now, props.observedAt)) / 1000))
  return Math.max(0, model.remaining_seconds - Math.max(0, Math.floor((props.now - props.observedAt) / 1000)))
}
function indicator(model: ModelSummary): Indicator {
  const result = (color: DotColor, key: string): Indicator => ({ color, label: t(`${prefix}.${key}`) })
  if (props.failed) return result('gray', 'columnUnavailable')
  if (!props.status) return result('gray', props.loading ? 'dotPending' : 'columnUnavailable')
  const cache = model.cache
  const available = cache?.cache_available ?? (cache?.state === 'ready' || cache?.state === 'paused')
  const targetCache = props.status.enabled && cache && cache.model_allowed !== false && cache.shape === 'target' &&
    [292, 332].includes(cache.token_length) && cache.token_length === props.status.expected_length &&
    cache.cipher_blocks === (cache.token_length === 292 ? 10 : 12)
  // Cache lifecycle warnings take precedence over historical response shapes.
  // A collector's failed renewal does not make the still-valid old cache expire.
  if (targetCache && cache.source === 'collector') {
    const seconds = remaining(cache)
    if (available && seconds > 0 && seconds <= 30) return result('yellow', 'dotCollectorExpiring')
    if (seconds === 0 && cache.collection_status === 'idle' && cache.collection_reason === 'idle' &&
      (cache.state === 'expired' || Number.isFinite(Date.parse(cache.expires_at || '')))) return result('gray', 'dotExpiredIdle')
  }
  const observation = model.observation
  if (observation) {
    const reason = observation.response_validation_reason
    if (reason === 'expired' || observation.response_shape === 'expired') return result('gray', 'dotExpired')
    if (reason === 'account_type_unknown' || reason === 'unexpected_shape') return result('gray', 'dotUnknown')
    if (['invalid_encoding', 'invalid_envelope', 'future_issued_at'].includes(reason || '') || observation.response_observed_shape === 'invalid' || observation.response_shape === 'invalid') return result('gray', 'dotInvalid')
    if (reason) return result('gray', 'dotUnknown')
    const shape = observation.response_observed_shape
    const personal = props.status.expected_length === 292
    const team = props.status.expected_length === 332
    if (!personal && !team) return result('gray', 'dotUnknown')
    const targetShape = observation.response_length === props.status.expected_length && (!shape || shape === (personal ? 'personal_target' : 'team_business_target'))
    const extendedShape = observation.response_length === (personal ? 312 : 356) && (!shape || shape === (personal ? 'personal_extended' : 'team_business_extended'))
    const blocks = observation.response_cipher_blocks
    if (blocks !== undefined && blocks !== (personal ? 10 : 12) + (extendedShape ? 1 : 0)) return result('gray', 'dotInvalid')
    if (extendedShape && ['suspect', 'extended'].includes(observation.response_shape)) return result('red', 'dotExtended')
    if (observation.response_shape === 'target' && targetShape) return result('green', 'dotObservedTarget')
    return result('gray', observation.response_length ? 'dotUnknown' : 'modelNotObserved')
  }
  if (targetCache && available && remaining(cache) > 0) return result('green', 'dotCachedTarget')
  if (cache?.state === 'expired' || (cache?.state === 'ready' && remaining(cache) === 0)) return result('gray', 'dotExpired')
  return result('gray', model.model ? 'modelNotObserved' : 'columnEmptyList')
}
</script>
