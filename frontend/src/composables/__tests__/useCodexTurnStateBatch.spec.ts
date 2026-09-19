import { effectScope, nextTick, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateBatch, CodexTurnStateStatus } from '@/api/admin/accounts'
import { useCodexTurnStateBatch } from '../useCodexTurnStateBatch'

function row(id: number, overrides = {}): AccountListItem {
  return { id, platform: 'openai', type: 'oauth', credentials: {}, ...overrides } as AccountListItem
}
function state(id: number, enabled = true): CodexTurnStateStatus {
  return { account_id: id, owner_account_id: id, enabled, models: [] } as unknown as CodexTurnStateStatus
}
function result(ids: number[]): CodexTurnStateBatch {
  return { models: ['gpt-6-astra'], items: Object.fromEntries(ids.map(id => [String(id), state(id)])) }
}
function deferred() {
  let resolve!: (value: CodexTurnStateBatch) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<CodexTurnStateBatch>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const scopes: ReturnType<typeof effectScope>[] = []
function setup(rows: AccountListItem[], fetcher = vi.fn().mockImplementation(async (ids: number[]) => result(ids))) {
  const accounts = ref(rows)
  const visible = ref(true)
  const tableLoading = ref(false)
  const scope = effectScope()
  scopes.push(scope)
  const batch = scope.run(() => useCodexTurnStateBatch(accounts, visible, tableLoading, fetcher))!
  return { accounts, visible, tableLoading, scope, batch, fetcher }
}
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()) })

describe('useCodexTurnStateBatch', () => {
  it('batches only supported visible OAuth rows, including Spark inheritance', async () => {
    const { fetcher, batch } = setup([row(1), row(2, { parent_account_id: 1 }), row(3, { type: 'apikey' }), row(4, { credentials: { auth_mode: 'personalAccessToken' } })])
    await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(fetcher).toHaveBeenCalledWith([1, 2], expect.any(AbortSignal))
    expect(Object.keys(batch.statuses.value)).toEqual(['1', '2'])
  })

  it('refreshes an edited row even when its ID has not changed', async () => {
    const { accounts, fetcher } = setup([row(1)])
    await flushPromises()
    accounts.value = [row(1, { codex_turn_state: { enabled: false } })]
    await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('retains same-page results and model names until a refresh completes', async () => {
    const pending = deferred()
    const fetcher = vi.fn().mockResolvedValueOnce(result([1])).mockReturnValueOnce(pending.promise)
    const { accounts, tableLoading, batch } = setup([row(1)], fetcher)
    await flushPromises()
    const previous = batch.statuses.value['1']
    tableLoading.value = true
    await nextTick()
    expect(batch.statuses.value['1']).toBe(previous)
    accounts.value = [row(1, { name: 'Refreshed name' })]
    tableLoading.value = false
    await nextTick()
    expect(batch.loading.value).toBe(true)
    expect(batch.statuses.value['1']).toBe(previous)
    expect(batch.models.value).toEqual(['gpt-6-astra'])
    pending.resolve({ ...result([1]), items: { '1': state(1, false) } })
    await flushPromises()
    expect(batch.statuses.value['1']?.enabled).toBe(false)
    expect(batch.loading.value).toBe(false)
  })

  it('retains names on refresh failure, marks stale colors unavailable and recovers on success', async () => {
    const retry = deferred()
    const fetcher = vi.fn().mockResolvedValueOnce(result([1]))
      .mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(retry.promise)
    const { batch } = setup([row(1)], fetcher)
    await flushPromises()
    await batch.refresh()
    expect(batch.statuses.value['1']).toBeDefined()
    expect(batch.models.value).toEqual(['gpt-6-astra'])
    expect(batch.errors.value.has(1)).toBe(true)
    const refresh = batch.refresh()
    expect(batch.errors.value.has(1)).toBe(true)
    retry.resolve(result([1]))
    await refresh
    expect(batch.errors.value.has(1)).toBe(false)
  })

  it('does not reuse retained status after an account changes its credential parent', async () => {
    const pending = deferred()
    const fetcher = vi.fn().mockResolvedValueOnce(result([1])).mockReturnValueOnce(pending.promise)
    const { accounts, batch } = setup([row(1)], fetcher)
    await flushPromises()
    accounts.value = [row(1, { parent_account_id: 2, codex_turn_state_inherited_from_account_id: 2 })]
    await nextTick()
    expect(batch.statuses.value['1']).toBeUndefined()
    pending.resolve({ ...result([1]), items: { '1': { ...state(1), owner_account_id: 2 } } })
    await flushPromises()
    expect(batch.statuses.value['1']?.owner_account_id).toBe(2)
  })

  it('ignores superseded responses while retaining the last completed same-page result', async () => {
    const oldRequest = deferred()
    const newRequest = deferred()
    const fetcher = vi.fn().mockResolvedValueOnce(result([1]))
      .mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(newRequest.promise)
    const { batch } = setup([row(1)], fetcher)
    await flushPromises()
    const oldRefresh = batch.refresh()
    const newRefresh = batch.refresh()
    expect(batch.statuses.value['1']?.enabled).toBe(true)
    expect((fetcher.mock.calls[1]![1] as AbortSignal).aborted).toBe(true)
    newRequest.resolve({ ...result([1]), items: { '1': state(1, false) } })
    await newRefresh
    oldRequest.resolve(result([1]))
    await oldRefresh
    expect(batch.statuses.value['1']?.enabled).toBe(false)
  })

  it('cancels hidden columns and ignores a late response before resuming when shown', async () => {
    const first = deferred()
    const fetcher = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(result([1]))
    const { visible, batch } = setup([row(1)], fetcher)
    const signal = fetcher.mock.calls[0]![1] as AbortSignal
    visible.value = false
    await nextTick()
    expect(signal.aborted).toBe(true)
    first.resolve(result([1]))
    await flushPromises()
    expect(batch.statuses.value).toEqual({})
    await batch.refresh()
    expect(fetcher).toHaveBeenCalledTimes(1)
    visible.value = true
    await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('drops old page data and errors while loading a different page', async () => {
    const first = deferred()
    const fetcher = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(result([2]))
    const { accounts, tableLoading, batch } = setup([row(1)], fetcher)
    tableLoading.value = true
    await nextTick()
    accounts.value = [row(2)]
    await nextTick()
    expect(fetcher).toHaveBeenCalledTimes(1)
    first.reject(new Error('old page failure'))
    tableLoading.value = false
    await flushPromises()
    expect(Object.keys(batch.statuses.value)).toEqual(['2'])
    expect(batch.errors.value.size).toBe(0)
  })

  it('marks omitted items unavailable instead of assuming their account cache is disabled', async () => {
    const fetcher = vi.fn().mockResolvedValue(result([1]))
    const { batch } = setup([row(1), row(2)], fetcher)
    await flushPromises()
    expect(batch.errors.value.has(2)).toBe(true)
    expect(batch.statuses.value['2']).toBeUndefined()
  })

  it('marks an omitted refresh item unavailable while retaining its model names', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(result([1, 2])).mockResolvedValueOnce(result([1]))
    const { batch } = setup([row(1), row(2)], fetcher)
    await flushPromises()
    await batch.refresh()
    expect(batch.errors.value.has(2)).toBe(true)
    expect(batch.statuses.value['2']).toBeDefined()
    expect(batch.errors.value.has(1)).toBe(false)
  })

  it('caps batches at 200 IDs and runs at most two requests concurrently', async () => {
    const pending = [deferred(), deferred(), deferred()]
    const fetcher = vi.fn().mockImplementation(() => pending[fetcher.mock.calls.length - 1]!.promise)
    const { batch } = setup(Array.from({ length: 451 }, (_, i) => row(i + 1)), fetcher)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(fetcher.mock.calls.map(call => call[0].length)).toEqual([200, 200])
    pending[0]!.resolve(result(fetcher.mock.calls[0]![0]))
    await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(fetcher.mock.calls[2]![0]).toHaveLength(51)
    pending[1]!.resolve(result(fetcher.mock.calls[1]![0]))
    pending[2]!.resolve(result(fetcher.mock.calls[2]![0]))
    await flushPromises()
    expect(Object.keys(batch.statuses.value)).toHaveLength(451)
    expect(batch.loading.value).toBe(false)
  })

  it('refreshes runtime state independently of an unchanged account page and aborts on disposal', async () => {
    const pending = deferred()
    const fetcher = vi.fn().mockResolvedValueOnce(result([1])).mockReturnValueOnce(pending.promise)
    const { batch, scope } = setup([row(1)], fetcher)
    await flushPromises()
    const refresh = batch.refresh()
    expect(fetcher).toHaveBeenCalledTimes(2)
    const signal = fetcher.mock.calls[1]![1] as AbortSignal
    scope.stop()
    expect(signal.aborted).toBe(true)
    pending.resolve(result([1]))
    await refresh
    expect(batch.statuses.value).toEqual({})
  })
})
