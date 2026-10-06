<template>
  <BaseDialog :show="show" :title="t('attribution.column')" width="extra-wide" @close="emit('close')">
    <div class="space-y-5">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.selected', { count: accountIds.length }) }} · {{ t('attribution.notice') }}</p>
      <div class="space-y-2">
        <label for="manual-attribution-model" class="block text-sm font-medium">{{ t('attribution.model') }}</label>
        <Select id="manual-attribution-model" v-model="model" data-testid="manual-attribution-model" :aria-label="t('attribution.model')" :options="modelOptions" :searchable="true" creatable :disabled="creating" class="max-w-lg" :placeholder="t('attribution.manualModelDefault')" />
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.manualHelp') }}</p>
        <p v-if="modelsError" class="text-xs text-amber-600 dark:text-amber-400">{{ t('attribution.modelsUnavailable') }}</p>
      </div>
      <AttributionExpectedModels v-model="expectedModels" :candidates="expectedCandidates" allow-inheritance :disabled="creating" data-testid="manual-expected-models" />
      <div class="flex flex-wrap items-center gap-3"><button data-testid="attribution-run" class="btn btn-primary" :disabled="creating || !ready || !accountIds.length || accountIds.length > 500" @click="create">{{ t('attribution.run') }}</button><RouterLink class="text-sm text-primary-600 dark:text-primary-400" to="/admin/model-attribution">{{ t('attribution.title') }}</RouterLink><span v-if="!loading && !ready" class="text-sm text-gray-500">{{ t('attribution.reasons.service_unconfigured') }}</span></div>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p><p v-if="message" role="status" class="text-sm text-primary-600">{{ message }}</p>
      <ul v-if="created.length" class="max-h-40 overflow-auto text-xs"><li v-for="job in created" :key="job.id" class="py-1">{{ job.account_name }}: {{ t(`attribution.status.${job.status}`) }}<span v-if="job.reason"> · {{ reason(job.reason) }}</span></li></ul>
      <AttributionHistory v-if="show" ref="history" :account-id="accountIds.length === 1 ? accountIds[0] : undefined" />
    </div>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { attributionAPI, type AttributionJob } from '@/api/admin/modelAttribution'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import AttributionHistory from './AttributionHistory.vue'
import AttributionExpectedModels from './AttributionExpectedModels.vue'
import { validAttributionExpectedModels } from '@/utils/modelAttribution'
const props = defineProps<{ show: boolean; accountIds: number[] }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const { t, te } = useI18n()
const ready = ref(false)
const loading = ref(false)
const model = ref('')
const expectedModels = ref<string[] | null>(null)
const expectedCandidates = ref<string[]>([])
const models = ref<string[]>([])
const modelsError = ref(false)
const modelOptions = computed(() => [{ value: '', label: t('attribution.manualModelDefault') }, ...models.value.map(value => ({ value, label: value }))])
const creating = ref(false)
const error = ref('')
const message = ref('')
const created = ref<AttributionJob[]>([])
const history = ref<InstanceType<typeof AttributionHistory>>()
const reason = (code: string) => te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : code
watch(() => props.show, async (show, _, onCleanup) => {
  let active = true
  onCleanup(() => { active = false })
  if (!show) return
  ready.value = false; loading.value = true; model.value = ''; models.value = []; modelsError.value = false
  expectedModels.value = null; expectedCandidates.value = []
  error.value = ''; message.value = ''; created.value = []
  try {
    const config = await attributionAPI.config()
    if (!active) return
    ready.value = Boolean(config.base_url.trim())
    models.value = [...new Set([config.default.model, ...config.groups.filter(g => g.enabled).map(g => g.model)].filter(Boolean))]
    if (ready.value) {
      try {
        const candidates = await attributionAPI.models(config.base_url)
        if (active) { models.value = [...new Set([...models.value, ...candidates])]; expectedCandidates.value = candidates }
      } catch { if (active) modelsError.value = true }
    }
  } catch { if (active) error.value = t('attribution.error') }
  finally { if (active) loading.value = false }
}, { immediate: true })
async function create() {
  if (creating.value || !ready.value) return
  const selectedModel = model.value.trim()
  if (!validAttributionExpectedModels(expectedModels.value)) { error.value = t('attribution.invalidExpectedModels'); return }
  if (selectedModel.length > 200 || /[\s*]/.test(selectedModel)) {
    error.value = t('attribution.invalidManualModel')
    return
  }
  creating.value = true; error.value = ''; message.value = ''
  try { created.value = await attributionAPI.create([...props.accountIds], selectedModel || undefined, expectedModels.value ?? undefined); message.value = t('attribution.created'); emit('updated'); await history.value?.refresh() }
  catch { error.value = t('attribution.error') }
  finally { creating.value = false }
}
</script>
