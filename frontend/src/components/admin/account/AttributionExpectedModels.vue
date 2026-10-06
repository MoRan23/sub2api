<template>
  <fieldset class="space-y-2" :disabled="disabled">
    <legend class="mb-2 text-sm font-medium">{{ t('attribution.expectedModels') }}</legend>
    <select v-if="allowInheritance" class="input w-full max-w-lg" data-testid="expected-models-mode" :aria-label="t('attribution.expectedModels')" :value="modelValue == null ? 'inherit' : 'custom'" @change="setMode">
      <option value="inherit">{{ t('attribution.expectedInherit') }}</option>
      <option value="custom">{{ t('attribution.expectedCustom') }}</option>
    </select>
    <p v-if="allowInheritance && modelValue == null" class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.expectedInheritHelp') }}</p>
    <template v-else>
      <ModelWhitelistSelector :model-value="modelValue || []" :options="options" @update:model-value="emit('update:modelValue', $event)" />
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.expectedHelp') }}</p>
      <p v-if="!modelValue?.length" class="text-xs text-primary-600 dark:text-primary-400" data-testid="expected-models-follow-probe">{{ t('attribution.expectedFollowProbe') }}</p>
    </template>
  </fieldset>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
const props = defineProps<{ modelValue?: string[] | null; candidates?: string[]; allowInheritance?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string[] | null] }>()
const { t } = useI18n()
const options = computed(() => (props.candidates || []).map(value => ({ value, label: value })))
function setMode(event: Event) {
  emit('update:modelValue', (event.target as HTMLSelectElement).value === 'inherit' ? null : [])
}
</script>
