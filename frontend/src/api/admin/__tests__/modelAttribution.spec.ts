import { describe, expect, it, vi } from 'vitest'
const post = vi.hoisted(() => vi.fn().mockResolvedValue({ data: { items: [] } }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))
import { attributionAPI } from '../modelAttribution'

describe('attribution expected-model request', () => {
  it('preserves omitted inheritance and explicit empty override', async () => {
    await attributionAPI.create([1], 'probe')
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs', { account_ids: [1], model: 'probe' })
    await attributionAPI.create([1], undefined, null)
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs', { account_ids: [1] })
    await attributionAPI.create([1, 2], 'probe', [])
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs', { account_ids: [1, 2], model: 'probe', expected_models: [] })
    await attributionAPI.create([1], undefined, ['expected-a', 'expected-b'])
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs', { account_ids: [1], expected_models: ['expected-a', 'expected-b'] })
  })
})
