<template>
  <section class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-400" :aria-label="t('admin.groups.daybreak.title')">
    <h4 class="mb-3 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groups.daybreak.title') }}</h4>
    <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.daybreak.hint') }}</p>
    <div class="flex items-center justify-between gap-4">
      <label class="text-sm text-gray-600 dark:text-gray-400">Daybreak Blue</label>
      <Toggle :model-value="blue" aria-label="Daybreak Blue" data-testid="group-daybreak-blue" @update:model-value="updateBlue" />
    </div>
    <div class="mt-4 flex items-center justify-between gap-4">
      <label class="text-sm text-gray-600 dark:text-gray-400">Daybreak Red</label>
      <Toggle :model-value="red" :disabled="!blue" aria-label="Daybreak Red" data-testid="group-daybreak-red" @update:model-value="updateRed" />
    </div>
    <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.daybreak.redHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'

const props = defineProps<{ blue: boolean; red: boolean }>()
const emit = defineEmits<{ 'update:blue': [value: boolean]; 'update:red': [value: boolean] }>()
const { t } = useI18n()

function updateBlue(value: boolean) {
  emit('update:blue', value)
  if (!value) emit('update:red', false)
}

function updateRed(value: boolean) {
  emit('update:red', props.blue && value)
}
</script>
