<template>
  <span v-if="account.platform !== 'openai'" class="text-gray-400">—</span>
  <button v-else type="button" class="max-w-60 space-y-1 text-left text-xs" @click.stop="emit('open')">
    <div class="flex flex-wrap gap-1">
      <AccountCandyTestStatus v-if="summary?.latest" :status="summary.latest.status" />
      <span v-else class="text-gray-400">{{ t('candyTests.noResult') }}</span>
      <AccountCandyTestStatus v-if="summary?.active" :status="summary.active.status" />
    </div>
    <div v-if="summary?.latest" class="truncate text-gray-600 dark:text-gray-300" :title="summary.latest.model">
      {{ summary.latest.model }} · {{ summary.latest.reasoning_effort || t('candyTests.defaultEffort') }}
    </div>
    <div v-if="summary?.latest?.finished_at" class="text-gray-400">{{ formatDateTime(summary.latest.finished_at) }}</div>
    <div class="text-primary-500">{{ t(summary?.active ? 'candyTests.activeBatch' : 'candyTests.details') }}</div>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { PELICAN_TEST_PROMPT_VERSION } from '@/api/admin/candyTests'
import type { CandyTestSummary } from '@/api/admin/candyTests'
import { formatDateTime } from '@/utils/format'
import AccountCandyTestStatus from './AccountCandyTestStatus.vue'

const props = defineProps<{ account: Account & { candy_test?: CandyTestSummary | null } }>()
const emit = defineEmits<{ (event: 'open'): void }>()
const { t } = useI18n()
const summary = computed(() => ({
  latest: props.account.candy_test?.latest?.prompt_version === PELICAN_TEST_PROMPT_VERSION ? props.account.candy_test.latest : undefined,
  active: props.account.candy_test?.active?.prompt_version === PELICAN_TEST_PROMPT_VERSION ? props.account.candy_test.active : undefined,
}))
</script>
