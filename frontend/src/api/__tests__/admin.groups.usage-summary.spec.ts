import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get },
}))

import { getUsageSummary } from '@/api/admin/groups'

describe('admin group usage summary API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: [] })
  })

  it('does not send browser timezone parameters', async () => {
    const summary = [
      { group_id: 1, today_cost: 1.25, yesterday_cost: 2.5, total_cost: 9.75 },
    ]
    get.mockResolvedValue({ data: summary })

    await expect(getUsageSummary()).resolves.toEqual(summary)

    expect(get).toHaveBeenCalledWith('/admin/groups/usage-summary', { params: undefined, signal: undefined })
  })

  it('scopes summaries to current page IDs and supports cancellation', async () => {
    const signal = new AbortController().signal
    await getUsageSummary([2, 9], signal)
    expect(get).toHaveBeenCalledWith('/admin/groups/usage-summary', { params: { group_ids: '2,9' }, signal })
    await getUsageSummary([], signal)
    expect(get).toHaveBeenLastCalledWith('/admin/groups/usage-summary', { params: { group_ids: '' }, signal })
  })
})
