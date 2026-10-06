<template>
  <section class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('attribution.overview') }}</h2><button class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('attribution.refresh') }}</button></div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <div v-if="data" class="flex flex-wrap gap-3 text-xs text-gray-500 dark:text-gray-400"><span v-if="data.queued">{{ t('attribution.queuedCount', { count: data.queued }) }}</span><span>{{ t('attribution.runningCount', { count: data.running }) }}</span><span>{{ t('attribution.total', { count: data.total }) }}</span></div>
    <div class="overflow-auto rounded-xl border border-gray-200 dark:border-dark-600">
      <table class="w-full text-left text-sm"><thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800"><tr><th class="p-3">{{ t('attribution.account') }}</th><th class="p-3">{{ t('attribution.latest') }}</th><th class="p-3">{{ t('attribution.top') }}</th><th class="p-3">{{ t('attribution.time') }}</th><th class="p-3"><span class="sr-only">{{ t('attribution.details') }}</span></th></tr></thead>
        <tbody><tr v-for="job in data?.items" :key="job.id" class="border-t border-gray-100 dark:border-dark-600"><td class="p-3">{{ job.account_name }}</td><td class="p-3">{{ t(`attribution.status.${job.status}`) }}</td><td class="p-3"><span v-if="job.result.analysis">{{ job.result.analysis.prediction }} · {{ job.result.analysis.probability == null ? t('attribution.unavailable') : (job.result.analysis.probability * 100).toFixed(2) + '%' }}</span><span v-else>—</span></td><td class="whitespace-nowrap p-3 text-xs">{{ formatDateTime(job.finished_at || job.started_at || job.created_at) }}</td><td class="p-3"><button class="text-primary-600 dark:text-primary-400" @click="select(job.id)">{{ t('attribution.details') }}</button></td></tr></tbody>
      </table><p v-if="!data?.items.length" class="p-6 text-center text-sm text-gray-500">{{ loading ? t('attribution.loading') : t('attribution.empty') }}</p>
    </div>
    <div class="flex justify-end gap-3"><button class="btn btn-secondary btn-sm" :disabled="page <= 1 || loading" @click="page--">{{ t('attribution.previous') }}</button><span class="self-center text-sm">{{ page }}</span><button class="btn btn-secondary btn-sm" :disabled="!data || page * data.page_size >= data.total || loading" @click="page++">{{ t('attribution.next') }}</button></div>
    <AttributionResult v-if="selected" :job="selected" />
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { attributionAPI, type AttributionJob, type AttributionPage } from '@/api/admin/modelAttribution'
import { formatDateTime } from '@/utils/format'
import AttributionResult from './AttributionResult.vue'
const props = defineProps<{ accountId?: number }>()
const { t } = useI18n()
const page = ref(1)
const data = ref<AttributionPage>()
const selected = ref<AttributionJob>()
const loading = ref(false)
const error = ref('')
let generation = 0
let disposed = false
async function load() {
  const request = ++generation
  loading.value = true
  try {
    const out = await attributionAPI.history(props.accountId, page.value)
    if (disposed || request !== generation) return
    data.value = out
    error.value = ''
    if (selected.value && ['queued', 'running'].includes(selected.value.status)) {
      const updated = await attributionAPI.job(selected.value.id)
      if (!disposed && request === generation && selected.value?.id === updated.id) selected.value = updated
    }
  } catch { if (!disposed && request === generation) error.value = t('attribution.error') }
  finally { if (!disposed && request === generation) loading.value = false }
}
let selection = 0
async function select(id: number) {
  const request = ++selection
  try { const job = await attributionAPI.job(id); if (!disposed && request === selection) selected.value = job }
  catch { error.value = t('attribution.error') }
}
watch(() => props.accountId, () => { page.value = 1; selected.value = undefined; selection++; void load() }, { immediate: true })
watch(page, load)
const timer = setInterval(() => { if (!loading.value) void load() }, 5000)
onBeforeUnmount(() => { disposed = true; generation++; selection++; clearInterval(timer) })
defineExpose({ refresh: load })
</script>
