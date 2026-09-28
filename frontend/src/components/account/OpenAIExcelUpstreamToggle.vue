<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="flex items-start justify-between gap-4">
      <span>
        <span class="input-label mb-0">{{ t('admin.accounts.openai.excelUpstream') }}</span>
        <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.openai.excelUpstreamDesc') }}
        </span>
      </span>
      <input
        type="checkbox"
        role="switch"
        :checked="modelValue"
        :aria-label="t('admin.accounts.openai.excelUpstream')"
        class="mt-1 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
      />
    </label>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n()
</script>
