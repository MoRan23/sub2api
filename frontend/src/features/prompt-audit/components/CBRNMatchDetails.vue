<template>
  <div class="space-y-3" data-test="cbrn-match-details">
    <div v-for="kind in kinds" :key="kind">
      <h5 class="text-sm font-medium text-gray-900 dark:text-white">{{ t(`admin.promptAudit.cbrn.matched${kind === 'topic' ? 'Topic' : 'Intent'}`) }}</h5>
      <template v-if="match?.[kind]">
        <p class="mt-1 whitespace-pre-wrap break-words text-sm" :data-test="`cbrn-${kind}-content`">{{ match[kind].content }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400" :data-test="`cbrn-${kind}-location`">
          {{ t(`admin.promptAudit.cbrn.${match[kind].source === 'latest_user' ? 'sourceLatestUser' : 'sourceHistory'}`) }} ·
          {{ t('admin.promptAudit.cbrn.location', { segment: match[kind].segment_index, start: match[kind].start, end: match[kind].end }) }}
        </p>
      </template>
      <p v-else class="mt-1 text-sm text-gray-500">{{ t('admin.promptAudit.keyword.notRecorded') }}</p>
    </div>
    <p v-if="match" class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.cbrn.locationHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { PromptCBRNMatch } from '../types'

defineProps<{ match?: PromptCBRNMatch }>()
const { t } = useI18n()
const kinds = ['topic', 'intent'] as const
</script>
