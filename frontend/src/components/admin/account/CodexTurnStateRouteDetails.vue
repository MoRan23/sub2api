<template>
  <dl class="grid gap-3 sm:grid-cols-2" data-testid="codex-turn-state-route-details">
    <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.wireMode`) }}</dt><dd data-testid="codex-route-wire-mode">{{ wireMode }}</dd></div>
    <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.actualProxy`) }}</dt><dd class="break-words" data-testid="codex-route-actual-proxy">{{ egress(evidence.actual_proxy_id) }}</dd></div>
    <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.routeSource`) }}</dt><dd data-testid="codex-route-source">{{ routeSource }}</dd></div>
    <div><dt class="text-xs text-gray-500">{{ t(`${prefix}.bundleProxy`) }}</dt><dd class="break-words" data-testid="codex-route-bundle-proxy">{{ egress(evidence.bundle_proxy_id) }}</dd></div>
  </dl>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexTurnStateRouteEvidence } from '@/api/admin/accounts'

const props = defineProps<{ evidence: CodexTurnStateRouteEvidence; proxyNames?: Record<number, string> }>()
const { t } = useI18n()
const prefix = 'admin.accounts.codexTurnState'
const wireMode = computed(() => t(`${prefix}.wireModes.${props.evidence.wire_mode === 'lite' || props.evidence.wire_mode === 'responses' ? props.evidence.wire_mode : 'unknown'}`))
const routeSources = new Set(['account', 'bundle', 'collector'])
const routeSource = computed(() => t(`${prefix}.routeSources.${routeSources.has(props.evidence.route_source || '') ? props.evidence.route_source : 'unknown'}`))
function egress(id?: number | null) {
  if (id === 0) return t(`${prefix}.proxyDirect`)
  if (Number.isSafeInteger(id) && (id ?? 0) > 0) {
    return props.proxyNames?.[id!] || t(`${prefix}.proxyFallback`, { id })
  }
  return t(`${prefix}.diagnosticUnknown`)
}
</script>
