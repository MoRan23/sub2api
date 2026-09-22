<template>
  <label class="block space-y-1.5">
    <span class="input-label">{{ label || t('admin.accounts.openai.identityOS') }}</span>
    <select :value="modelValue" class="input" :disabled="disabled" data-testid="openai-oauth-os-select" @change="$emit('update:modelValue', ($event.target as HTMLSelectElement).value as OpenAIOAuthOS)">
      <option v-for="os in openAIOperatingSystems" :key="os" :value="os">
        {{ openAIOSLabels[os] }}
      </option>
    </select>
  </label>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'
import { openAIOperatingSystems, openAIOSLabels } from './openaiOAuthOS'

defineProps<{ modelValue: OpenAIOAuthOS; profiles?: OpenAIOAuthOSProfiles; label?: string; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [os: OpenAIOAuthOS] }>()
const { t } = useI18n()
</script>
