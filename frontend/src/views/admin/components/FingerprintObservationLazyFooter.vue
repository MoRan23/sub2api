<template>
  <div
    v-if="state.loading"
    class="flex items-center justify-center gap-2 px-4 py-3 text-xs text-gray-500 dark:text-gray-400"
  >
    <Icon name="refresh" size="xs" class="animate-spin" />
    <span>{{ t('common.loading') }}</span>
  </div>

  <div
    v-else-if="state.error"
    class="flex items-center justify-center gap-3 px-4 py-3 text-xs text-red-600 dark:text-red-400"
  >
    <span>{{ state.error }}</span>
    <button type="button" class="btn btn-ghost btn-sm" @click="emit('retry')">
      {{ t('admin.fingerprintObservation.retry') }}
    </button>
  </div>

  <div v-else-if="state.nextCursor" class="flex justify-center px-4 py-3">
    <button type="button" class="btn btn-ghost btn-sm" @click="emit('loadMore')">
      {{ t('admin.fingerprintObservation.loadMore') }}
    </button>
  </div>

  <div
    v-else-if="state.loaded && state.items.length === 0"
    class="px-4 py-3 text-center text-xs text-gray-400"
  >
    {{ t('admin.fingerprintObservation.noChildren') }}
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

defineProps<{
  state: {
    items: unknown[]
    nextCursor: string
    loaded: boolean
    loading: boolean
    error: string
  }
}>()

const emit = defineEmits<{
  retry: []
  loadMore: []
}>()

const { t } = useI18n()
</script>
