import { onScopeDispose, ref, watch, type Ref } from 'vue'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateBatch, CodexTurnStateStatus } from '@/api/admin/accounts'
import { supportsCodexTurnState } from '@/components/account/codexTurnState'

type FetchBatch = (ids: number[], signal: AbortSignal) => Promise<CodexTurnStateBatch>

/** Visible account rows share bounded, read-only status requests. */
export function useCodexTurnStateBatch(
  accounts: Ref<AccountListItem[]>, visible: Ref<boolean>, tableLoading: Ref<boolean>, fetchBatch: FetchBatch
) {
  const statuses = ref<Record<string, CodexTurnStateStatus>>({})
  const errors = ref<Set<number>>(new Set())
  const models = ref<string[]>([])
  const loading = ref(false)
  const observedAt = ref(Date.now())
  let controller: AbortController | null = null
  let generation = 0

  async function refresh() {
    controller?.abort()
    const currentGeneration = ++generation
    loading.value = false
    if (!visible.value) {
      statuses.value = {}
      errors.value = new Set()
      models.value = []
      return
    }
    const rows = accounts.value.filter(supportsCodexTurnState)
    const ids = [...new Set(rows.map(account => account.id))]
    // Keep the last result while refreshing the same rows. Prune departed or
    // reparented accounts before awaiting any response so their state cannot
    // appear on a different page or credential owner.
    const retained: Record<string, CodexTurnStateStatus> = {}
    const retainedErrors = new Set<number>()
    for (const account of rows) {
      const previous = statuses.value[String(account.id)]
      const ownerID = account.codex_turn_state_inherited_from_account_id ?? account.parent_account_id ?? account.id
      if (previous?.owner_account_id === ownerID) retained[String(account.id)] = previous
      if (errors.value.has(account.id) && (!previous || previous.owner_account_id === ownerID)) retainedErrors.add(account.id)
    }
    statuses.value = retained
    errors.value = retainedErrors
    if (!ids.length || tableLoading.value) return
    const currentController = new AbortController()
    controller = currentController
    const isCurrent = () => currentGeneration === generation && !currentController.signal.aborted
    loading.value = true
    const batches = Array.from({ length: Math.ceil(ids.length / 200) }, (_, index) => ids.slice(index * 200, (index + 1) * 200))
    let nextBatch = 0
    const worker = async () => {
      while (isCurrent() && nextBatch < batches.length) {
        const batch = batches[nextBatch++]!
        try {
          const result = await fetchBatch(batch, currentController.signal)
          if (!isCurrent()) return
          if (Array.isArray(result.models)) models.value = result.models.filter(model => typeof model === 'string')
          const next = { ...statuses.value }
          const missing = new Set(errors.value)
          for (const id of batch) {
            const status = result.items?.[String(id)]
            if (status?.account_id === id && typeof status.enabled === 'boolean' && Array.isArray(status.models)) {
              next[String(id)] = status
              missing.delete(id)
            } else missing.add(id)
          }
          statuses.value = next
          errors.value = missing
        } catch {
          if (!isCurrent()) return
          errors.value = new Set([...errors.value, ...batch])
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(2, batches.length) }, worker))
    if (isCurrent()) {
      observedAt.value = Date.now()
      loading.value = false
    }
  }

  watch([accounts, visible, tableLoading], () => { void refresh() }, { immediate: true })
  onScopeDispose(() => {
    ++generation
    controller?.abort()
    statuses.value = {}
    errors.value = new Set()
  })
  return { statuses, errors, models, loading, observedAt, refresh }
}
