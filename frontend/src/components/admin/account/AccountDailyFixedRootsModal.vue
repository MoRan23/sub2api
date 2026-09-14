<template>
  <BaseDialog :show="show" :title="t('admin.accounts.dailyFixedRoots.title')" @close="$emit('close')">
    <p class="mb-4 break-all text-sm font-medium text-gray-700 dark:text-gray-200">{{ accountName }}</p>
    <dl v-if="pool" class="space-y-4 text-sm">
      <div>
        <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.dailyFixedRoots.businessDate') }}</dt>
        <dd class="mt-1 text-gray-800 dark:text-gray-200">{{ pool.business_date }}</dd>
      </div>
      <div v-for="(root, index) in pool.stream_session_ids" :key="index">
        <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.dailyFixedRoots.streamRoot') }} {{ index }}</dt>
        <dd class="mt-1 select-text whitespace-normal break-all font-mono text-gray-800 dark:text-gray-200">{{ root || '—' }}</dd>
      </div>
      <div>
        <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.dailyFixedRoots.syncRoot') }}</dt>
        <dd class="mt-1 select-text whitespace-normal break-all font-mono text-gray-800 dark:text-gray-200">{{ pool.sync_session_id || '—' }}</dd>
      </div>
      <div>
        <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.dailyFixedRoots.generation') }}</dt>
        <dd class="mt-1 select-text whitespace-normal break-all font-mono text-gray-800 dark:text-gray-200">{{ pool.generation || '—' }}</dd>
      </div>
    </dl>
    <p v-else class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accounts.dailyFixedRoots.empty') }}</p>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OAuthDailySessionPool } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

defineProps<{ show: boolean; accountName: string; pool?: OAuthDailySessionPool }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
</script>
