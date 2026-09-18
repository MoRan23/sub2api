<template>
  <section :aria-label="t(`${prefix}.title`)" class="min-w-0 max-w-[calc(100vw-4rem)] space-y-3 border-t border-gray-200 pt-3 sm:max-w-none dark:border-dark-700" data-testid="codex-outbound-metadata">
    <h4 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.title`) }}</h4>
    <p class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.sourceHint`) }}</p>
    <dl class="grid min-w-0 gap-3 sm:grid-cols-2">
      <div v-for="item in scalarItems" :key="item.key" class="min-w-0">
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.fields.${item.key}`) }}</dt>
        <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ item.value }}</dd>
      </div>
    </dl>

    <details class="min-w-0 rounded-lg border border-gray-200 dark:border-dark-700" data-testid="codex-compaction" @toggle="compactionOpen = ($event.target as HTMLDetailsElement).open">
      <summary class="cursor-pointer break-words px-3 py-2 text-gray-700 dark:text-gray-300">
        <span class="font-medium">{{ t(`${prefix}.fields.compaction`) }}</span>
        <span class="ml-2">{{ statusLabel('compaction') }}</span>
      </summary>
      <div v-if="compactionOpen" class="min-w-0 border-t border-gray-200 p-3 dark:border-dark-700">
        <dl v-if="compaction" class="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="key in compactionKeys" :key="key" class="min-w-0">
            <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.compactionFields.${key}`) }}</dt>
            <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ compaction[key] || t(`${prefix}.status.missing`) }}</dd>
          </div>
        </dl>
        <p v-else class="text-gray-500 dark:text-gray-400">{{ statusLabel('compaction') }}</p>
      </div>
    </details>

    <details class="min-w-0 rounded-lg border border-gray-200 dark:border-dark-700" data-testid="codex-tool-namespaces" @toggle="toolsOpen = ($event.target as HTMLDetailsElement).open">
      <summary class="cursor-pointer break-words px-3 py-2 text-gray-700 dark:text-gray-300">
        <span class="font-medium">{{ t(`${prefix}.fields.tool_namespaces_info`) }}</span>
        <span class="ml-2">{{ toolSummary }}</span>
        <span v-if="status('tool_namespaces_info') === 'truncated'" class="ml-2 text-amber-700 dark:text-amber-400">{{ t(`${prefix}.status.truncated`) }}</span>
      </summary>
      <div v-if="toolsOpen" class="min-w-0 space-y-3 border-t border-gray-200 p-3 dark:border-dark-700">
        <p v-if="status('tool_namespaces_info') === 'truncated'" class="text-amber-700 dark:text-amber-400">{{ t(`${prefix}.truncatedHint`) }}</p>
        <ul v-if="namespaces.length" class="min-w-0 space-y-3">
          <li v-for="(namespace, index) in namespaces" :key="index" class="min-w-0 rounded-md bg-gray-100 p-3 dark:bg-dark-800">
            <dl class="grid min-w-0 gap-3 sm:grid-cols-2">
              <div class="min-w-0">
                <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.toolFields.namespace`) }}</dt>
                <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ namespace.namespace || '—' }}</dd>
              </div>
              <div class="min-w-0">
                <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.toolFields.name`) }}</dt>
                <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ namespace.name || '—' }}</dd>
              </div>
            </dl>
            <ul v-if="namespace.functions.length" class="mt-3 min-w-0 space-y-3">
              <li v-for="(fn, functionIndex) in namespace.functions" :key="functionIndex" class="min-w-0 border-t border-gray-200 pt-3 dark:border-dark-700">
                <dl class="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <div v-for="field in functionFields(fn)" :key="field.key" class="min-w-0">
                    <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.toolFields.${field.key}`) }}</dt>
                    <dd class="mt-1 break-all font-mono text-gray-800 dark:text-gray-200">{{ field.value }}</dd>
                  </div>
                </dl>
              </li>
            </ul>
            <p v-else class="mt-3 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.${status('tool_namespaces_info') === 'truncated' ? 'functionsNotRecorded' : 'noFunctions'}`) }}</p>
          </li>
        </ul>
        <p v-else class="text-gray-500 dark:text-gray-400">{{ status('tool_namespaces_info') === 'valid' ? t(`${prefix}.noNamespaces`) : statusLabel('tool_namespaces_info') }}</p>
      </div>
    </details>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexToolNamespaceMetadata, FingerprintObservationEntry } from '@/api/admin/fingerprintObservations'

const props = defineProps<{ observation: FingerprintObservationEntry }>()
const { t } = useI18n()
const prefix = 'admin.fingerprintObservation.request.outboundMetadata'
const compactionOpen = ref(false)
const toolsOpen = ref(false)
const compactionKeys = ['trigger', 'reason', 'implementation', 'phase', 'strategy'] as const
type MetadataKey = keyof NonNullable<FingerprintObservationEntry['metadata_status']>

function status(key: MetadataKey): string {
  const value = props.observation.metadata_status?.[key]
  return value === 'missing' || value === 'valid' || value === 'invalid' || value === 'truncated' ? value : 'not_collected'
}
function statusLabel(key: MetadataKey): string { return t(`${prefix}.status.${status(key)}`) }

const scalarItems = computed(() => (['request_kind', 'history_ingest_requested'] as const).map(key => ({
  key,
  value: status(key) === 'valid' && props.observation[key] !== undefined
    ? String(props.observation[key])
    : statusLabel(key),
})))
const compaction = computed(() => status('compaction') === 'valid' ? props.observation.compaction : undefined)
const namespaces = computed(() => {
  if (!['valid', 'truncated'].includes(status('tool_namespaces_info'))) return []
  return Array.isArray(props.observation.tool_namespaces_info) ? props.observation.tool_namespaces_info : []
})
const toolSummary = computed(() => ['valid', 'truncated'].includes(status('tool_namespaces_info'))
  ? t(`${prefix}.toolCounts`, { namespaces: namespaces.value.length, functions: namespaces.value.reduce((sum, ns) => sum + ns.functions.length, 0) })
  : statusLabel('tool_namespaces_info'))

function functionFields(fn: CodexToolNamespaceMetadata['functions'][number]) {
  return [
    { key: 'function', value: fn.function || '—' },
    { key: 'name', value: fn.name || '—' },
    { key: 'direct', value: String(fn.direct) },
    { key: 'deferred', value: String(fn.deferred) },
    { key: 'code_mode_name', value: fn.code_mode_name || t(`${prefix}.status.missing`) },
    { key: 'source', value: fn.source?.kind || t(`${prefix}.status.missing`) },
    ...(fn.source?.kind === 'mcp' ? [{ key: 'server_name', value: fn.source.server_name || t(`${prefix}.status.missing`) }] : []),
  ]
}
</script>
