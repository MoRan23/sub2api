<template>
  <div :data-testid="`account-codex-turn-state-${account.id}`">
  <span v-if="!supported" class="text-sm text-gray-400 dark:text-dark-500" :title="t(`${prefix}.columnUnsupported`)">—</span>
  <button
    v-else type="button" aria-haspopup="dialog" :aria-label="t(`${prefix}.viewStatus`)"
    class="block w-64 max-w-full space-y-1.5 rounded text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
    data-testid="account-codex-turn-state-cell" @click.stop="$emit('open')"
  >
    <span v-if="loading && !status && !failed" class="block text-gray-500">{{ t(`${prefix}.loading`) }}</span>
    <span v-else-if="failed || !status" class="block text-amber-700 dark:text-amber-400">{{ t(`${prefix}.columnUnavailable`) }}</span>
    <template v-else>
      <span v-if="status.inherited" class="block text-blue-600 dark:text-blue-400">{{ t(`${prefix}.columnInherited`, { id: status.owner_account_id }) }}</span>
      <span v-if="!status.enabled" class="block text-gray-500 dark:text-gray-400">{{ t(`${prefix}.disabled`) }}</span>
      <template v-else>
        <span v-if="!status.expected_length" class="block text-amber-700 dark:text-amber-400">{{ t(`${prefix}.columnUnknownPlan`) }}</span>
        <span v-if="status.reason === 'model_policy_unavailable'" class="block text-amber-700 dark:text-amber-400">{{ label('states', 'model_policy_unavailable') }}</span>
        <span v-else-if="!allModels.length" class="block text-gray-500 dark:text-gray-400">{{ t(`${prefix}.columnEmptyList`) }}</span>
        <span v-for="model in visibleModels" :key="model.model" class="block space-y-0.5">
          <span class="flex items-start justify-between gap-2">
            <span class="min-w-0 break-all font-mono text-gray-800 dark:text-gray-200">{{ model.model }}</span>
            <span class="shrink-0 text-right text-[11px]" :class="currentState(model) === 'ready' ? 'text-emerald-700 dark:text-emerald-400' : 'text-gray-500 dark:text-gray-400'">{{ label('states', currentState(model)) }}</span>
          </span>
          <span class="block text-[11px] text-gray-500 dark:text-gray-400">
            <template v-if="model.token_length > 0">{{ t(`${prefix}.characters`, { count: model.token_length }) }}<span v-if="model.shape !== 'target' || currentState(model) !== 'ready'"> · {{ label('shapes', model.shape || 'unknown') }}</span></template>
            <template v-else>{{ t(`${prefix}.columnNoCache`) }}</template>
            <span v-if="remaining(model) > 0"> · {{ t(`${prefix}.columnRemaining`, { minutes: Math.ceil(remaining(model) / 60) }) }}</span>
          </span>
        </span>
        <span v-if="allModels.length > visibleModels.length" class="block text-primary-600 dark:text-primary-400">{{ t(`${prefix}.columnMore`, { count: allModels.length - visibleModels.length }) }}</span>
      </template>
      <span class="block text-[11px] text-primary-600 dark:text-primary-400">{{ t(`${prefix}.columnDetails`) }}</span>
    </template>
  </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateModelStatus, CodexTurnStateStatus } from '@/api/admin/accounts'
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
function label(group: string, value: string) {
  const key = `${prefix}.${group}.${value}`
  return te(key) ? t(key) : value
}
const allModels = computed(() => {
  const stored = new Map((props.status?.models || []).map(model => [model.model, model]))
  const result = props.models.map(model => {
    const record = stored.get(model)
    stored.delete(model)
    return record || { model, state: 'missing', shape: 'unknown', token_length: 0, remaining_seconds: 0 } as CodexTurnStateModelStatus
  })
  return [...result, ...stored.values()]
})
const visibleModels = computed(() => allModels.value.slice(0, 3))
function remaining(model: CodexTurnStateModelStatus): number {
  if (model.state !== 'ready') return 0
  const expires = model.expires_at ? Date.parse(model.expires_at) : Number.NaN
  if (Number.isFinite(expires)) return Math.max(0, Math.ceil((expires - props.now) / 1000))
  return Math.max(0, model.remaining_seconds - Math.max(0, Math.floor((props.now - props.observedAt) / 1000)))
}
function currentState(model: CodexTurnStateModelStatus) {
  return model.state === 'ready' && remaining(model) === 0 ? 'expired' : model.state
}
</script>
