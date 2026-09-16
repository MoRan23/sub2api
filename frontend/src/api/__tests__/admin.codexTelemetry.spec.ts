import { beforeEach, describe, expect, it, vi } from 'vitest'
import { codexTelemetryAPI } from '../admin/codexTelemetry'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get } }))

describe('codexTelemetryAPI', () => {
  beforeEach(() => { get.mockReset() })

  it('preserves filters and cancellation without using fingerprint endpoints', async () => {
    get.mockResolvedValue({ data: { items: [], total: 0 } })
    const controller = new AbortController()
    await codexTelemetryAPI.list({ account_id: 42, status: 'failed', type: 'metrics', page: 2, page_size: 50 }, { signal: controller.signal })
    expect(get).toHaveBeenCalledWith('/admin/openai/telemetry-observations', {
      params: { account_id: 42, status: 'failed', type: 'metrics', page: 2, page_size: 50 },
      signal: controller.signal,
    })
  })

  it('normalizes absent arrays so an empty API result cannot break rendering', async () => {
    get.mockResolvedValueOnce({ data: { items: null } })
    expect((await codexTelemetryAPI.list()).items).toEqual([])
    get.mockResolvedValueOnce({ data: { items: [null, { id: 1, event_names: null }, { id: 2, event_names: ['turn.completed', null, 7] }] } })
    expect((await codexTelemetryAPI.list()).items).toEqual([
      { id: 1, event_names: [] }, { id: 2, event_names: ['turn.completed'] },
    ])
  })
})
