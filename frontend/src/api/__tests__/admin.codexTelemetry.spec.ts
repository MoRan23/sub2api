import { beforeEach, describe, expect, it, vi } from 'vitest'
import { codexTelemetryAPI } from '../admin/codexTelemetry'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get } }))

describe('codexTelemetryAPI', () => {
  beforeEach(() => { get.mockReset() })

  it('preserves filters and cancellation without using fingerprint endpoints', async () => {
    get.mockResolvedValue({ data: { items: [], total: 0 } })
    const controller = new AbortController()
    await codexTelemetryAPI.list({ account_id: 42, status: 'failed', type: 'metrics', os_family: 'macos', source: 'mixed', page: 2, page_size: 50 }, { signal: controller.signal })
    expect(get).toHaveBeenCalledWith('/admin/openai/telemetry-observations', {
      params: { account_id: 42, status: 'failed', type: 'metrics', os_family: 'macos', source: 'mixed', page: 2, page_size: 50 },
      signal: controller.signal,
    })
  })

  it('normalizes absent arrays so an empty API result cannot break rendering', async () => {
    get.mockResolvedValueOnce({ data: { items: null } })
    expect((await codexTelemetryAPI.list()).items).toEqual([])
    get.mockResolvedValueOnce({ data: { items: [null, { id: 1, event_names: null }, { id: 2, event_names: ['turn.completed', null, 7] }] } })
    expect((await codexTelemetryAPI.list()).items).toEqual([
      { id: 1, event_names: [], reasons: [], field_sources: {} }, { id: 2, event_names: ['turn.completed'], reasons: [], field_sources: {} },
    ])
  })

  it('normalizes diagnostic provenance without accepting unsupported sources', async () => {
    get.mockResolvedValue({ data: { items: [{ id: 1, event_names: [], reasons: [null, 7, 'unknown_os'], field_sources: { model: 'observed', shell: 'simulated', bad: 'claimed' } }] } })
    const [entry] = (await codexTelemetryAPI.list()).items
    expect(entry?.reasons).toEqual(['unknown_os'])
    expect(entry?.field_sources).toEqual({ model: 'observed', shell: 'simulated' })
  })
})
