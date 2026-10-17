import { computed, ref, watch, type Ref } from 'vue'
import { attributionAPI, type AttributionProbeModelAccount } from '@/api/admin/modelAttribution'

/** Account catalog suggestions never select or replace a probe model. */
export function useAttributionProbeModels(accountIds: Readonly<Ref<number[]>>) {
  const models = ref<string[]>([])
  const accounts = ref<AttributionProbeModelAccount[]>([])
  const loading = ref(false)
  const error = ref(false)
  watch(() => [...new Set(accountIds.value)].join(','), async (_, __, onCleanup) => {
    let active = true
    const controller = new AbortController()
    onCleanup(() => { active = false; controller.abort() })
    models.value = []; accounts.value = []; error.value = false; loading.value = false
    const ids = [...new Set(accountIds.value)]
    if (!ids.length || ids.length > 500) return
    loading.value = true
    try {
      const result = await attributionAPI.probeModels(ids, controller.signal)
      if (!active) return
      models.value = result.models
      accounts.value = result.accounts
      error.value = result.accounts.some(account => account.status !== 'success')
    } catch { if (active) error.value = true }
    finally { if (active) loading.value = false }
  }, { immediate: true })
  const coverage = computed(() => Object.fromEntries(models.value.map(model => [model, accounts.value.filter(account => account.models.includes(model)).length])))
  return { models, accounts, loading, error, coverage }
}
