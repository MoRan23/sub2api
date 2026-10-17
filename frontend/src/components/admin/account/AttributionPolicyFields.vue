<template>
  <div class="space-y-4">
    <AttributionProbeModelField :model-value="modelValue.model" :group-id="groupId" @update:model-value="set('model', $event)" />
    <AttributionExpectedModels :model-value="modelValue.expected_models || []" :candidates="candidates" @update:model-value="set('expected_models', $event || [])" />
    <div class="grid gap-4 lg:grid-cols-2">
      <div><h3 class="mb-2 text-sm font-medium">{{ t('attribution.high') }}</h3><ModelWhitelistSelector :model-value="modelValue.high_models" platform="openai" @update:model-value="set('high_models', $event)" /></div>
      <div><h3 class="mb-2 text-sm font-medium">{{ t('attribution.low') }}</h3><ModelWhitelistSelector :model-value="modelValue.low_models" platform="openai" @update:model-value="set('low_models', $event)" /></div>
    </div>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import AttributionExpectedModels from './AttributionExpectedModels.vue'
import AttributionProbeModelField from './AttributionProbeModelField.vue'
import type { AttributionPolicy } from '@/api/admin/modelAttribution'
const props = defineProps<{ modelValue: AttributionPolicy; candidates?: string[]; groupId?: number }>()
const emit = defineEmits<{ 'update:modelValue': [value: AttributionPolicy] }>()
const { t } = useI18n()
function set<K extends keyof AttributionPolicy>(key: K, value: AttributionPolicy[K]) { emit('update:modelValue', { ...props.modelValue, [key]: value }) }
</script>
