import { describe, expect, it, vi } from 'vitest'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { getCodexTurnState, getCodexTurnStates } from '@/api/admin/accounts'

describe('Codex turn-state status API', () => {
  it('uses the read-only account endpoint and forwards cancellation', async () => {
    const state = { account_id: 12, enabled: false, models: [] }
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data: state })
    await expect(getCodexTurnState(12, signal)).resolves.toEqual(state)
    expect(get).toHaveBeenCalledWith('/admin/accounts/12/codex-turn-state', { signal })
  })

  it('preserves ordered proxies and per-model rotation fields in the batch response', async () => {
    const batch = { models: ['gpt-6-astra'], items: { '12': {
      account_id: 12, collector_proxy_ids: [9, 7], models: [{ model: 'gpt-6-astra', collector_proxy_id: 7, last_collector_proxy_id: 9, collector_extended_count: 2 }],
    } } }
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data: batch })
    await expect(getCodexTurnStates([12], signal)).resolves.toEqual(batch)
    expect(get).toHaveBeenCalledWith('/admin/accounts/codex-turn-state', { params: { account_ids: '12' }, signal })
  })
})
