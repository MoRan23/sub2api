<template>
  <section class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ t('admin.groups.openaiFast.title') }}
    </h4>
    <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">
      {{ t(scope === 'account' ? 'admin.groups.openaiFast.accountHint' : 'admin.groups.openaiFast.hint') }}
    </p>
    <div v-for="row in rows" :key="row.key" class="mt-4">
      <div class="flex items-center justify-between gap-4">
        <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-400">
          <input
            v-if="selectedFields"
            type="checkbox"
            :data-testid="`${testPrefix}-apply-${row.key}`"
            :aria-label="t('admin.groups.openaiFast.applyField', { field: t(row.label) })"
            :checked="selectedFields.includes(row.key)"
            :disabled="disabled"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            @change="selectField(row.key, ($event.target as HTMLInputElement).checked)"
          />
          {{ t(row.label) }}
        </label>
        <Toggle
          :data-testid="`${testPrefix}-${row.key.replace(/_/g, '-')}`"
          :aria-label="t(row.label)"
          :disabled="disabled || (selectedFields !== undefined && !selectedFields.includes(row.key))"
          :model-value="modelValue[row.key]"
          @update:model-value="setValue(row.key, $event)"
        />
      </div>
      <p v-if="row.hint" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t(row.hint) }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import {
  readOpenAIServiceTierSettings, updateOpenAIServiceTierSetting,
  type OpenAIServiceTierKey, type OpenAIServiceTierSettings
} from './openaiServiceTierSettings'

const props = withDefaults(defineProps<{
  modelValue: OpenAIServiceTierSettings
  scope?: 'account' | 'group'
  testPrefix?: string
  disabled?: boolean
  selectedFields?: OpenAIServiceTierKey[]
}>(), { scope: 'account', testPrefix: 'account', disabled: false })
const emit = defineEmits<{
  'update:modelValue': [value: OpenAIServiceTierSettings]
  change: [key: OpenAIServiceTierKey]
  'update:selectedFields': [keys: OpenAIServiceTierKey[]]
}>()
const { t } = useI18n()
const rows: { key: OpenAIServiceTierKey; label: string; hint?: string }[] = [
  { key: 'force_openai_fast', label: 'admin.groups.openaiFast.force' },
  { key: 'force_openai_ultrafast', label: 'admin.groups.openaiFast.forceUltrafast', hint: 'admin.groups.openaiFast.ultrafastHint' },
  { key: 'free_openai_fast', label: 'admin.groups.openaiFast.free', hint: 'admin.groups.openaiFast.freeHint' },
  { key: 'free_openai_ultrafast', label: 'admin.groups.openaiFast.freeUltrafast', hint: 'admin.groups.openaiFast.freeUltrafastHint' }
]
function setValue(key: OpenAIServiceTierKey, enabled: boolean) {
  emit('update:modelValue', updateOpenAIServiceTierSetting(readOpenAIServiceTierSettings(props.modelValue), key, enabled))
  emit('change', key)
}
function selectField(key: OpenAIServiceTierKey, selected: boolean) {
  const keys = new Set(props.selectedFields)
  const changed: OpenAIServiceTierKey[] = key.startsWith('force_')
    ? ['force_openai_fast', 'force_openai_ultrafast'] : [key]
  for (const entry of changed) {
    if (selected) keys.add(entry)
    else keys.delete(entry)
  }
  emit('update:selectedFields', [...keys])
}
</script>
