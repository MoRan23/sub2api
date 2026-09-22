<template>
  <label class="block space-y-1.5">
    <span class="input-label">{{ label || t('admin.accounts.openai.authorizationOS') }}</span>
    <select :value="modelValue" class="input" :disabled="disabled" data-testid="openai-oauth-os-select" @change="$emit('update:modelValue', ($event.target as HTMLSelectElement).value as OpenAIOAuthOS)">
      <option v-for="os in openAIOperatingSystems" :key="os" :value="os" :disabled="authorizedOnly && !isOpenAIOSAuthorized(profiles, os)">
        {{ openAIOSLabels[os] }}{{ profiles ? ` · ${t(`admin.accounts.openai.authorizationStatus.${profiles.profiles?.[os]?.authorization?.status || 'unauthorized'}`)}` : '' }}
      </option>
    </select>
    <span v-if="authorizedOnly && !isOpenAIOSAuthorized(profiles, modelValue)" role="alert" class="block text-xs text-amber-700 dark:text-amber-400">{{ t('admin.accounts.openai.authorizationRequired') }}</span>
  </label>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'
import { openAIOperatingSystems, openAIOSLabels, isOpenAIOSAuthorized } from './openaiOAuthOS'

defineProps<{ modelValue: OpenAIOAuthOS; profiles?: OpenAIOAuthOSProfiles; label?: string; disabled?: boolean; authorizedOnly?: boolean }>()
defineEmits<{ 'update:modelValue': [os: OpenAIOAuthOS] }>()
const { t } = useI18n()
</script>
