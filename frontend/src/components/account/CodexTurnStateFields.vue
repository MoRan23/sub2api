<template>
  <section class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="codex-turn-state-fields">
    <div class="flex items-center justify-between gap-4">
      <div>
        <h3 class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t(`${prefix}.title`) }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.description`) }}</p>
      </div>
      <input
        type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600"
        :aria-label="t(`${prefix}.enable`)" :checked="modelValue.enabled" :disabled="inheritedFrom != null"
        @change="update({ enabled: ($event.target as HTMLInputElement).checked })"
      />
    </div>
    <p v-if="inheritedFrom != null" class="text-sm text-blue-600 dark:text-blue-400" data-testid="codex-turn-state-inherited">
      {{ t(`${prefix}.inherited`, { id: inheritedFrom }) }}
    </p>
    <template v-if="modelValue.enabled">
      <label class="block">
        <span class="input-label">{{ t(`${prefix}.accountType`) }}</span>
        <select class="input" :value="modelValue.account_type" :disabled="inheritedFrom != null"
          @change="update({ account_type: ($event.target as HTMLSelectElement).value as CodexTurnStateConfig['account_type'] })">
          <option value="auto">{{ t(`${prefix}.types.auto`) }}</option>
          <option value="personal">{{ t(`${prefix}.types.personal`) }}</option>
          <option value="team_business">{{ t(`${prefix}.types.team_business`) }}</option>
        </select>
      </label>
      <p class="input-hint">{{ t(`${prefix}.typeHint`) }}</p>
      <div>
        <label class="input-label">{{ t(`${prefix}.collectorProxy`) }}</label>
        <ProxySelector :model-value="modelValue.collector_proxy_id" :proxies="proxies"
          :disabled="inheritedFrom != null" :no-proxy-label="t(`${prefix}.noCollectorProxy`)"
          @update:model-value="update({ collector_proxy_id: $event })" />
        <p class="input-hint">{{ t(`${prefix}.proxyHint`) }}</p>
      </div>
    </template>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.experimental`) }}</p>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { CodexTurnStateConfig, Proxy } from '@/types'
import ProxySelector from '@/components/common/ProxySelector.vue'

const props = defineProps<{ modelValue: CodexTurnStateConfig; proxies: Proxy[]; inheritedFrom?: number | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: CodexTurnStateConfig] }>()
const { t } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
function update(patch: Partial<CodexTurnStateConfig>) {
  if (props.inheritedFrom != null) return
  emit('update:modelValue', { ...props.modelValue, ...patch })
}
</script>
