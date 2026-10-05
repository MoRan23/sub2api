<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="openai-daybreak-settings">
    <div class="flex items-center justify-between gap-3">
      <h3 class="input-label mb-0">Daybreak</h3>
      <button v-if="accountId" type="button" class="btn btn-secondary btn-sm" :disabled="loading || !active" data-testid="daybreak-refresh" @click="loadCapabilities">
        {{ t(loading ? 'admin.accounts.openai.daybreak.checking' : 'admin.accounts.openai.daybreak.refresh') }}
      </button>
    </div>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.daybreak.description') }}</p>
    <div class="flex items-center justify-between gap-3">
      <label for="daybreak-blue-toggle" class="text-sm">Daybreak Blue</label>
      <Toggle id="daybreak-blue-toggle" :model-value="blue" :disabled="!blue && !canEnableBlue" aria-label="Daybreak Blue" data-testid="daybreak-blue" @update:model-value="updateBlue" />
    </div>
    <div class="flex items-center justify-between gap-3">
      <label for="daybreak-red-toggle" class="text-sm">Daybreak Red</label>
      <Toggle id="daybreak-red-toggle" :model-value="red" :disabled="!red && !canEnableRed" aria-label="Daybreak Red" data-testid="daybreak-red" @update:model-value="updateRed" />
    </div>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.daybreak.redRequiresBlue') }}</p>
    <p v-if="!accountId" class="text-xs text-gray-500 dark:text-gray-400" data-testid="daybreak-create-hint">{{ t('admin.accounts.openai.daybreak.createHint') }}</p>
    <p v-else-if="loading" class="text-xs text-gray-500 dark:text-gray-400" role="status">{{ t('admin.accounts.openai.daybreak.checking') }}</p>
    <p v-else-if="error" class="text-xs text-red-600 dark:text-red-400" role="alert">{{ error }}</p>
    <template v-else-if="capabilities">
      <p v-if="capabilities.reason" class="text-xs text-amber-600 dark:text-amber-400" data-testid="daybreak-reason">{{ capabilities.reason }}</p>
      <p v-else-if="!capabilities.blue_available" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.daybreak.unavailable') }}</p>
      <ul v-if="capabilities.models.length" class="space-y-1 text-xs text-gray-600 dark:text-gray-300" data-testid="daybreak-models">
        <li v-for="model in capabilities.models" :key="model.model">
          <span class="font-mono">{{ model.model }}</span>
          · {{ model.required_tier === 'red' ? 'Blue + Red' : 'Blue' }}
          · <span class="font-mono">{{ model.cyber }}</span>
        </li>
      </ul>
      <p v-if="capabilities.checked_at" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.daybreak.checkedAt', { time: formatDateTime(capabilities.checked_at) }) }}</p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { OpenAIDaybreakCapabilities } from '@/types'
import { formatDateTime } from '@/utils/format'
import Toggle from '@/components/common/Toggle.vue'

const props = withDefaults(defineProps<{
  accountId?: number
  active?: boolean
  blue: boolean
  red: boolean
}>(), { active: true })
const emit = defineEmits<{
  'update:blue': [value: boolean]
  'update:red': [value: boolean]
}>()
const { t } = useI18n()
const capabilities = ref<OpenAIDaybreakCapabilities | null>(null)
const loading = ref(false)
const error = ref('')
let requestVersion = 0
const canEnableBlue = computed(() => props.active && !!props.accountId && !loading.value && !error.value && capabilities.value?.blue_available === true)
const canEnableRed = computed(() => canEnableBlue.value && props.blue && capabilities.value?.red_available === true)

function updateBlue(value: boolean) {
  if (value && !canEnableBlue.value) return
  emit('update:blue', value)
  if (!value) emit('update:red', false)
}

function updateRed(value: boolean) {
  if (value && !canEnableRed.value) return
  emit('update:red', value)
}

async function loadCapabilities() {
  const version = ++requestVersion
  const accountId = props.accountId
  capabilities.value = null
  error.value = ''
  loading.value = false
  if (!props.active || !accountId) return
  loading.value = true
  try {
    const result = await adminAPI.accounts.getDaybreakCapabilities(accountId)
    if (version === requestVersion) capabilities.value = result
  } catch (cause: unknown) {
    if (version !== requestVersion) return
    const message = (cause as { response?: { data?: { message?: string } } })?.response?.data?.message
    error.value = message || t('admin.accounts.openai.daybreak.checkFailed')
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

watch(() => [props.accountId, props.active], loadCapabilities, { immediate: true })
onBeforeUnmount(() => { requestVersion++ })
</script>
