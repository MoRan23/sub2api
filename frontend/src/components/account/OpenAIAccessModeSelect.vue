<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="input-label" for="openai-api-key-mode">{{ t('admin.accounts.openai.accessMode') }}</label>
    <select id="openai-api-key-mode" v-model="mode" class="input" data-testid="openai-api-key-mode">
      <option value="generic">{{ t('admin.accounts.openai.accessModeGeneric') }}</option>
      <option value="codex_engine">Codex-Engine</option>
    </select>
    <p class="input-hint">{{ t('admin.accounts.openai.accessModeHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OpenAIAPIKeyMode } from '@/types'

const mode = defineModel<OpenAIAPIKeyMode>({ required: true })
const { t } = useI18n()
</script>
