import { describe, expect, it, vi } from 'vitest'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { getCodexTurnState } from '@/api/admin/accounts'

describe('Codex turn-state status API', () => {
  it('uses the read-only account endpoint and forwards cancellation', async () => {
    const state = { account_id: 12, enabled: false, models: [] }
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data: state })
    await expect(getCodexTurnState(12, signal)).resolves.toEqual(state)
    expect(get).toHaveBeenCalledWith('/admin/accounts/12/codex-turn-state', { signal })
  })
})
