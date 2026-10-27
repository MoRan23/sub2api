<template>
  <div class="space-y-4">
    <AttributionProbeModelField :model-value="modelValue.model" :group-id="groupId" @update:model-value="set('model', $event)" />
    <AttributionExpectedModels :model-value="modelValue.expected_models || []" :candidates="candidates" @update:model-value="set('expected_models', $event || [])" />
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.accountActions.modeHelp') }}</p>
    <div class="grid gap-4 xl:grid-cols-2">
      <AttributionTierFields ref="highFields" :model-value="modelValue" tier="high" :groups="groups" @update:model-value="emit('update:modelValue', $event)" />
      <AttributionTierFields ref="lowFields" :model-value="modelValue" tier="low" :groups="groups" @update:model-value="emit('update:modelValue', $event)" />
    </div>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref } from 'vue'
import AttributionTierFields from './AttributionTierFields.vue'
import AttributionExpectedModels from './AttributionExpectedModels.vue'
import AttributionProbeModelField from './AttributionProbeModelField.vue'
import type { AttributionPolicy } from '@/api/admin/modelAttribution'
import type { AdminGroup } from '@/types'
const props = defineProps<{ modelValue: AttributionPolicy; candidates?: string[]; groupId?: number; groups?: AdminGroup[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: AttributionPolicy] }>()
const { t } = useI18n()
const highFields = ref<InstanceType<typeof AttributionTierFields>>()
const lowFields = ref<InstanceType<typeof AttributionTierFields>>()
function set<K extends keyof AttributionPolicy>(key: K, value: AttributionPolicy[K]) { emit('update:modelValue', { ...props.modelValue, [key]: value }) }
defineExpose({ validate: () => Boolean(highFields.value?.validate() && lowFields.value?.validate()) })
</script>
