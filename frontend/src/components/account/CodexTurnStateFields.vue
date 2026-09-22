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
        <div class="space-y-2">
          <div v-for="(proxyId, index) in proxyIDs" :key="proxyId" class="flex items-center gap-2" data-testid="codex-turn-state-proxy-row">
            <span class="w-4 shrink-0 text-xs text-gray-500">{{ index + 1 }}</span>
            <div class="min-w-0 flex-1">
              <ProxySelector :model-value="proxyId" :proxies="availableProxies(proxyId)"
                :disabled="inheritedFrom != null" :no-proxy-label="t(`${prefix}.removeCollectorProxy`)"
                @update:model-value="replaceProxy(index, $event)" />
            </div>
            <button type="button" class="btn btn-secondary px-2" :disabled="inheritedFrom != null || index === 0"
              :aria-label="t(`${prefix}.moveProxyUp`)" data-testid="codex-turn-state-proxy-up" @click="moveProxy(index, -1)">↑</button>
            <button type="button" class="btn btn-secondary px-2" :disabled="inheritedFrom != null || index === proxyIDs.length - 1"
              :aria-label="t(`${prefix}.moveProxyDown`)" data-testid="codex-turn-state-proxy-down" @click="moveProxy(index, 1)">↓</button>
            <button type="button" class="btn btn-secondary px-2" :disabled="inheritedFrom != null"
              :aria-label="t(`${prefix}.removeCollectorProxy`)" data-testid="codex-turn-state-proxy-remove" @click="replaceProxy(index, null)">×</button>
          </div>
          <p v-if="!proxyIDs.length" class="text-xs text-gray-500 dark:text-gray-400" data-testid="codex-turn-state-proxy-empty">{{ t(`${prefix}.noCollectorProxy`) }}</p>
          <button type="button" class="btn btn-secondary" :disabled="inheritedFrom != null || !availableProxies().length"
            data-testid="codex-turn-state-proxy-add" @click="addProxy">{{ t(`${prefix}.addCollectorProxy`) }}</button>
        </div>
        <p class="input-hint">{{ t(`${prefix}.proxyHint`) }}</p>
        <label class="mt-3 flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input
            type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600"
            data-testid="codex-turn-state-use-ticket-proxy"
            :checked="modelValue.use_ticket_proxy !== false" :disabled="inheritedFrom != null"
            @change="update({ use_ticket_proxy: ($event.target as HTMLInputElement).checked })"
          />
          {{ t(`${prefix}.useTicketProxy`) }}
        </label>
        <p class="input-hint" data-testid="codex-turn-state-bundle-routing-hint">{{ t(`${prefix}.bundleRoutingHint`) }}</p>
        <p class="input-hint">{{ t(`${prefix}.proxyRotationHint`) }}</p>
      </div>
    </template>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.experimental`) }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexTurnStateConfig, Proxy } from '@/types'
import ProxySelector from '@/components/common/ProxySelector.vue'
import { collectorProxyIDs, readCodexTurnStateConfig, type EditableCodexTurnStateConfig } from './codexTurnState'

const props = defineProps<{ modelValue: CodexTurnStateConfig; proxies: Proxy[]; inheritedFrom?: number | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: EditableCodexTurnStateConfig] }>()
const { t } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const proxyIDs = computed(() => collectorProxyIDs(props.modelValue))
function update(patch: Partial<CodexTurnStateConfig>) {
  if (props.inheritedFrom != null) return
  emit('update:modelValue', readCodexTurnStateConfig({ ...props.modelValue, ...patch }))
}
function availableProxies(currentId?: number) {
  return props.proxies.filter(proxy => proxy.id === currentId || !proxyIDs.value.includes(proxy.id))
}
function addProxy() {
  const proxy = availableProxies()[0]
  if (proxy) update({ collector_proxy_ids: [...proxyIDs.value, proxy.id] })
}
function replaceProxy(index: number, id: number | null) {
  if (id != null && (!props.proxies.some(proxy => proxy.id === id) || proxyIDs.value.some((existing, position) => position !== index && existing === id))) return
  const ids = [...proxyIDs.value]
  if (id == null) ids.splice(index, 1)
  else ids[index] = id
  update({ collector_proxy_ids: ids })
}
function moveProxy(index: number, direction: number) {
  const target = index + direction
  if (target < 0 || target >= proxyIDs.value.length) return
  const ids = [...proxyIDs.value]
  ;[ids[index], ids[target]] = [ids[target]!, ids[index]!]
  update({ collector_proxy_ids: ids })
}
</script>
