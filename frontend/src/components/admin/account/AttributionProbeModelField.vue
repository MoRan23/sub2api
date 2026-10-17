<template>
  <div class="space-y-2" data-testid="attribution-probe-field">
    <label :for="sourceId" class="block text-sm font-medium">{{ t('attribution.catalogSource') }}</label>
    <Select :id="sourceId" v-model="sourceAccountId" data-testid="probe-catalog-source" :options="accountOptions" :aria-label="t('attribution.catalogSource')" searchable remote clearable :loading="searching" :disabled="disabled" :placeholder="t('attribution.catalogSourcePlaceholder')" @search="searchAccounts" @click="ensureAccounts" />
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.catalogSourceHelp') }}</p>
    <p v-if="searchError" role="status" class="text-xs text-amber-600 dark:text-amber-400">{{ t('attribution.catalogAccountsUnavailable') }}</p>
    <label :for="inputId" class="block text-sm font-medium">{{ label || t('attribution.model') }}</label>
    <input :id="inputId" :value="modelValue" :list="listId" :data-testid="inputTestId" class="input w-full" placeholder="gpt-6-astra" maxlength="200" :disabled="disabled" @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)" />
    <datalist :id="listId"><option v-for="model in models" :key="model" :value="model" /></datalist>
    <p v-if="loading" class="text-xs text-gray-500" role="status">{{ t('attribution.loading') }}</p>
    <p v-if="catalogError" class="text-xs text-amber-600 dark:text-amber-400" role="status">{{ t('attribution.modelsUnavailable') }}</p>
    <p v-else-if="sourceAccountId && !loading" class="text-xs text-gray-500" role="status">{{ t('attribution.catalogReady', { count: models.length }) }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { list } from '@/api/admin/accounts'
import { useAttributionProbeModels } from '@/composables/useAttributionProbeModels'
import type { AccountListItem } from '@/types'

const props = defineProps<{ modelValue: string; groupId?: number; oauthOnly?: boolean; label?: string; inputTestId?: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const id = useId()
const sourceId = `${id}-source`
const inputId = `${id}-model`
const listId = `${id}-models`
const sourceAccountId = ref<number | null>(null)
const searchResults = ref<AccountListItem[]>([])
const selectedAccount = ref<AccountListItem>()
const searching = ref(false)
const searchError = ref(false)
let searched = false
let searchGeneration = 0
let controller: AbortController | undefined
const accountOptions = computed(() => {
  const accounts = new Map(searchResults.value.map(account => [account.id, account]))
  if (selectedAccount.value) accounts.set(selectedAccount.value.id, selectedAccount.value)
  return [...accounts.values()].map(account => ({ value: account.id, label: `${account.name} · #${account.id}` }))
})
watch(sourceAccountId, id => { selectedAccount.value = searchResults.value.find(account => account.id === id) || (selectedAccount.value?.id === id ? selectedAccount.value : undefined) })
const ids = computed(() => sourceAccountId.value ? [sourceAccountId.value] : [])
const { models, loading, error: catalogError } = useAttributionProbeModels(ids)

async function searchAccounts(keyword: string) {
  const generation = ++searchGeneration
  controller?.abort()
  controller = new AbortController()
  searching.value = true; searchError.value = false; searched = true
  try {
    const types = props.oauthOnly ? ['oauth'] : ['oauth', 'apikey']
    const pages = await Promise.all(types.map(type => list(1, 20, { platform: 'openai', type, search: keyword, ...(props.groupId ? { group: String(props.groupId) } : {}) }, { signal: controller!.signal })))
    if (generation !== searchGeneration) return
    searchResults.value = pages.flatMap(page => page.items).filter(account => account.platform === 'openai' && (account.type === 'oauth' || (!props.oauthOnly && account.type === 'apikey')))
  } catch { if (generation === searchGeneration) { searchResults.value = []; searchError.value = true } }
  finally { if (generation === searchGeneration) searching.value = false }
}
function ensureAccounts() { if (!searched) void searchAccounts('') }
watch(() => [props.groupId, props.oauthOnly], () => {
  searchGeneration++; controller?.abort(); searched = false
  sourceAccountId.value = null; selectedAccount.value = undefined; searchResults.value = []; searching.value = false; searchError.value = false
})
onUnmounted(() => { searchGeneration++; controller?.abort() })
</script>
