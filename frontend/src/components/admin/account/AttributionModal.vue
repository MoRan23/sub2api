<template>
  <BaseDialog :show="show" :title="t('attribution.column')" width="extra-wide" @close="emit('close')">
    <div class="space-y-5">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.selected', { count: accountIds.length }) }} · {{ t('attribution.notice') }}</p>
      <div class="flex flex-wrap items-center gap-3"><button class="btn btn-primary" :disabled="creating || !enabled || !accountIds.length || accountIds.length > 500" @click="create">{{ t('attribution.run') }}</button><RouterLink class="text-sm text-primary-600 dark:text-primary-400" to="/admin/model-attribution">{{ t('attribution.title') }}</RouterLink><span v-if="!enabled" class="text-sm text-gray-500">{{ t('attribution.reasons.disabled') }}</span></div>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p><p v-if="message" role="status" class="text-sm text-primary-600">{{ message }}</p>
      <ul v-if="created.length" class="max-h-40 overflow-auto text-xs"><li v-for="job in created" :key="job.id" class="py-1">{{ job.account_name }}: {{ t(`attribution.status.${job.status}`) }}<span v-if="job.reason"> · {{ reason(job.reason) }}</span></li></ul>
      <AttributionHistory v-if="show" ref="history" :account-id="accountIds.length === 1 ? accountIds[0] : undefined" />
    </div>
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { attributionAPI, type AttributionJob } from '@/api/admin/modelAttribution'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AttributionHistory from './AttributionHistory.vue'
const props = defineProps<{ show: boolean; accountIds: number[] }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const { t, te } = useI18n()
const enabled = ref(false)
const creating = ref(false)
const error = ref('')
const message = ref('')
const created = ref<AttributionJob[]>([])
const history = ref<InstanceType<typeof AttributionHistory>>()
const reason = (code: string) => te(`attribution.reasons.${code}`) ? t(`attribution.reasons.${code}`) : code
watch(() => props.show, async show => {
  if (!show) return
  enabled.value = false; error.value = ''; message.value = ''; created.value = []
  try { enabled.value = (await attributionAPI.config()).enabled } catch { error.value = t('attribution.error') }
}, { immediate: true })
async function create() {
  creating.value = true; error.value = ''; message.value = ''
  try { created.value = await attributionAPI.create([...props.accountIds]); message.value = t('attribution.created'); emit('updated'); await history.value?.refresh() }
  catch { error.value = t('attribution.error') }
  finally { creating.value = false }
}
</script>
