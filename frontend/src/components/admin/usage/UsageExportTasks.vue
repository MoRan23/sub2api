<template>
  <BaseDialog :show="show" :title="t('usage.exportTasks.title')" width="wide" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('usage.exportTasks.hint') }}</p>
      <div class="flex items-center justify-between gap-2">
        <span class="text-xs text-gray-500">{{ t('usage.exportTasks.recent') }}</span>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="manualRefresh">{{ t('common.refresh') }}</button>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <p v-if="!tasks.length && !loading" class="py-5 text-center text-sm text-gray-500">{{ t('usage.exportTasks.empty') }}</p>
      <div class="max-h-[60vh] space-y-3 overflow-y-auto">
        <article v-for="task in tasks" :key="task.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="font-medium text-gray-900 dark:text-white">{{ t(`usage.exportTasks.status.${task.status}`) }}</div>
              <div class="mt-1 text-xs text-gray-500">{{ formatTime(task.created_at) }}</div>
            </div>
            <div class="flex flex-wrap gap-2">
              <button v-if="task.status === 'completed'" type="button" class="btn btn-primary" :disabled="busy.has(task.id)" @click="act(task, 'download')">{{ t('usage.exportTasks.download') }}</button>
              <button v-if="active(task)" type="button" class="btn btn-secondary" :disabled="busy.has(task.id)" @click="act(task, 'cancel')">{{ t('common.cancel') }}</button>
              <button v-else-if="task.status !== 'deleting'" type="button" class="btn btn-secondary" :disabled="busy.has(task.id)" @click="act(task, 'remove')">{{ t('common.delete') }}</button>
            </div>
          </div>
          <div class="mt-3 text-sm text-gray-600 dark:text-gray-300">
            {{ t('usage.exportTasks.rows', { count: task.rows_written.toLocaleString() }) }}
            <span v-if="task.total_rows != null && task.total_rows > 0"> / {{ task.total_rows.toLocaleString() }}</span>
            <span v-if="task.size_bytes > 0"> · {{ (task.size_bytes / 1024 / 1024).toFixed(1) }} MiB</span>
          </div>
          <div v-if="task.status === 'completed' && task.expires_at" class="mt-1 text-xs text-gray-500">{{ t('usage.exportTasks.expires', { time: formatTime(task.expires_at) }) }}</div>
          <div v-if="task.error_code" class="mt-1 text-xs text-red-500">{{ task.error_code }}</div>
        </article>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { usageExportsAPI, type UsageExportTask } from '@/api/admin/usageExports'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; refreshKey: number }>()
const emit = defineEmits<{ close: [] }>()
const { t, locale } = useI18n()
const tasks = ref<UsageExportTask[]>([])
const loading = ref(false)
const error = ref('')
const busy = reactive(new Set<string>())
const mutations = new Set<AbortController>()
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let pollsRemaining = 200 // Stop after ten minutes; manual refresh starts a new bounded session.
let disposed = false
const active = (task: UsageExportTask) => task.status === 'queued' || task.status === 'running'
const pending = (task: UsageExportTask) => active(task) || task.status === 'deleting'
const formatTime = (value: string) => new Date(value).toLocaleString(locale.value)

async function reload() {
  if (!props.show || disposed) return
  clearTimeout(timer)
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  let succeeded = false
  try {
    const result = await usageExportsAPI.list(current.signal)
    if (current.signal.aborted || controller !== current) return
    tasks.value = result
    error.value = ''
    succeeded = true
  } catch (err) {
    if (!current.signal.aborted && controller === current) error.value = extractApiErrorMessage(err, t('usage.exportTasks.failed'))
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
      if (succeeded && !current.signal.aborted && props.show && !disposed && pollsRemaining > 0 && tasks.value.some(pending)) {
        pollsRemaining--
        timer = setTimeout(() => { void reload() }, 3000)
      }
    }
  }
}
function manualRefresh() {
  pollsRemaining = 200
  void reload()
}
async function act(task: UsageExportTask, action: 'download' | 'cancel' | 'remove') {
  if (busy.has(task.id)) return
  const current = new AbortController()
  mutations.add(current)
  busy.add(task.id)
  try {
    await usageExportsAPI[action](task.id, current.signal)
    if (!current.signal.aborted && !disposed) await reload()
  } catch (err) {
    if (!current.signal.aborted && !disposed) error.value = extractApiErrorMessage(err, t('usage.exportTasks.failed'))
  } finally {
    mutations.delete(current)
    busy.delete(task.id)
  }
}
watch(() => [props.show, props.refreshKey] as const, ([show]) => {
  if (show) manualRefresh()
  else { controller?.abort(); clearTimeout(timer) }
}, { immediate: true })
onBeforeUnmount(() => {
  disposed = true
  controller?.abort()
  clearTimeout(timer)
  for (const request of mutations) request.abort()
})
</script>
