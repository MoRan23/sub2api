<template>
  <section class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600" :data-testid="`attribution-${tier}-fields`">
    <h3 class="text-sm font-medium">{{ t(`attribution.${tier}`) }}</h3>
    <ModelWhitelistSelector :model-value="models" :model-mappings="mappingEnabled ? rows : []" platform="openai" @update:model-value="setModels" />

    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" :checked="mappingEnabled" :data-testid="`${tier}-mapping-toggle`" @change="toggleMapping(($event.target as HTMLInputElement).checked)" />
      {{ t('attribution.accountActions.replaceMapping') }}
    </label>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(mappingEnabled ? 'attribution.accountActions.replaceMappingHelp' : 'attribution.accountActions.keepMappingHelp') }}</p>
    <div v-if="mappingEnabled" class="space-y-3">
      <div v-for="(row, index) in rows" :key="row.id" class="flex flex-wrap items-center gap-2" :data-testid="`${tier}-mapping-row`">
        <input v-model="row.from" class="input min-w-0 flex-1 font-mono text-xs" maxlength="200" :aria-label="t('attribution.accountActions.mappingFrom')" :placeholder="t('attribution.accountActions.mappingFrom')" :data-testid="`${tier}-mapping-from`" @input="publishRows" />
        <span aria-hidden="true">→</span>
        <input v-model="row.to" class="input min-w-0 flex-1 font-mono text-xs" maxlength="200" :aria-label="t('attribution.accountActions.mappingTo')" :placeholder="t('attribution.accountActions.mappingTo')" :data-testid="`${tier}-mapping-to`" @input="publishRows" />
        <button type="button" class="text-xs text-red-600 dark:text-red-400" :aria-label="t('attribution.accountActions.removeMapping', { index: index + 1 })" @click="removeRow(index)">{{ t('attribution.remove') }}</button>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :data-testid="`${tier}-mapping-add`" @click="addRow">{{ t('attribution.accountActions.addMapping') }}</button>
      <p v-if="mappingError" role="alert" class="text-xs text-red-600 dark:text-red-400" :data-testid="`${tier}-mapping-error`">{{ t(`attribution.accountActions.${mappingError}`) }}</p>
      <details v-else class="text-xs" :data-testid="`${tier}-mapping-preview`">
        <summary class="cursor-pointer">{{ t('attribution.accountActions.preview') }}</summary>
        <pre class="mt-2 overflow-auto rounded bg-gray-100 p-2 dark:bg-dark-900">{{ JSON.stringify(preview, null, 2) }}</pre>
      </details>
    </div>

    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" :checked="groupsEnabled" :data-testid="`${tier}-groups-toggle`" @change="toggleGroups(($event.target as HTMLInputElement).checked)" />
      {{ t('attribution.accountActions.replaceGroups') }}
    </label>
    <div v-if="groupsEnabled" class="space-y-3">
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.accountActions.replaceGroupsHelp') }}</p>
      <GroupSelector :model-value="selectedGroups" :groups="groups" platform="openai" @update:model-value="setGroups" />
      <p v-if="selectedGroups.length === 0" role="alert" class="text-xs text-red-600 dark:text-red-400">{{ t('attribution.accountActions.groupRequired') }}</p>
      <ol class="space-y-2">
        <li v-for="(id, index) in selectedGroups" :key="id" class="flex items-center gap-2 text-xs" :data-testid="`${tier}-group-row`">
          <span class="min-w-0 flex-1">{{ index + 1 }}. {{ groups.find(group => group.id === id)?.name || `#${id}` }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="index === 0" :data-testid="`${tier}-group-up`" @click="moveGroup(index, -1)">{{ t('attribution.priority.up') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="index === selectedGroups.length - 1" :data-testid="`${tier}-group-down`" @click="moveGroup(index, 1)">{{ t('attribution.priority.down') }}</button>
          <button type="button" class="text-red-600 dark:text-red-400" @click="setGroups(selectedGroups.filter(groupID => groupID !== id))">{{ t('attribution.remove') }}</button>
        </li>
      </ol>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, toRaw, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import type { AttributionPolicy } from '@/api/admin/modelAttribution'
import type { AdminGroup } from '@/types'
import { attributionMappingError, validAttributionActions } from '@/utils/modelAttribution'

const props = withDefaults(defineProps<{ modelValue: AttributionPolicy; tier: 'high' | 'low'; groups?: AdminGroup[] }>(), { groups: () => [] })
const emit = defineEmits<{ 'update:modelValue': [value: AttributionPolicy] }>()
const { t } = useI18n()
const mapping = computed(() => props.modelValue[`${props.tier}_model_mapping`])
const mappingEnabled = computed(() => mapping.value != null)
const groupsEnabled = computed(() => props.modelValue[`${props.tier}_group_ids`] != null)
const models = computed(() => props.modelValue[`${props.tier}_models`])
const selectedGroups = computed(() => props.modelValue[`${props.tier}_group_ids`] || [])
let nextRowID = 0
let publishedMapping: Record<string, string> | undefined
const rows = ref<{ id: number; from: string; to: string }[]>([])
const savedGroups = ref<number[]>([])

watch(mapping, value => {
  if (value == null) return
  if (toRaw(value) === publishedMapping) return
  rows.value = Object.entries(value).map(([from, to]) => ({ id: ++nextRowID, from, to }))
}, { immediate: true, deep: true })
watch(selectedGroups, value => { if (groupsEnabled.value) savedGroups.value = [...value] }, { immediate: true })

const mappingError = computed(() => mappingEnabled.value ? attributionMappingError(rows.value) : undefined)
const preview = computed(() => Object.fromEntries([
  ...models.value.map(model => [model.trim(), model.trim()]),
  ...rows.value.map(row => [row.from.trim(), row.to.trim()])
]))

function set<K extends keyof AttributionPolicy>(key: K, value: AttributionPolicy[K]) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}
function setModels(value: string[]) { set(`${props.tier}_models`, value) }
function setGroups(value: number[]) { set(`${props.tier}_group_ids`, value) }
function toggleGroups(enabled: boolean) { set(`${props.tier}_group_ids`, enabled ? [...savedGroups.value] : null) }
function toggleMapping(enabled: boolean) {
  if (!enabled) { set(`${props.tier}_model_mapping`, null); return }
  const value = Object.fromEntries(rows.value.map(row => [row.from.trim(), row.to.trim()]))
  publishedMapping = value
  set(`${props.tier}_model_mapping`, value)
}
function publishRows() {
  if (attributionMappingError(rows.value)) return
  const value = Object.fromEntries(rows.value.map(row => [row.from.trim(), row.to.trim()]))
  publishedMapping = value
  set(`${props.tier}_model_mapping`, value)
}
function addRow() { rows.value.push({ id: ++nextRowID, from: '', to: '' }) }
function removeRow(index: number) { rows.value.splice(index, 1); publishRows() }
function moveGroup(index: number, delta: number) {
  const ids = [...selectedGroups.value]
  const target = index + delta
  if (target < 0 || target >= ids.length) return
  ;[ids[index], ids[target]] = [ids[target], ids[index]]
  setGroups(ids)
}
function validate() { return !mappingError.value && validAttributionActions(props.modelValue) }
defineExpose({ validate })
</script>
