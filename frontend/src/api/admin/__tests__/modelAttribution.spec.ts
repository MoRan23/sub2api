import { describe, expect, it, vi } from 'vitest'
const post = vi.hoisted(() => vi.fn().mockResolvedValue({ data: { items: [] } }))
const get = vi.hoisted(() => vi.fn())
vi.mock('@/api/client', () => ({ apiClient: { post, get } }))
import { attributionAPI } from '../modelAttribution'

describe('attribution expected-model request', () => {
  it('passes cancellation to configuration and detector catalog requests', async () => {
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data: { base_url: 'http://detector:8080' } })
    await attributionAPI.config(signal)
    expect(get).toHaveBeenLastCalledWith('/admin/model-attribution/config', { signal })
    const result = { models: ['candidate'], detector: { revision: 'current' } }
    post.mockResolvedValueOnce({ data: result })
    expect(await attributionAPI.connection('http://detector:8080', signal)).toEqual(result)
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/models', { base_url: 'http://detector:8080' }, { timeout: 25000, signal })
  })
  it('submits a spaced probe model as one unchanged JSON field', async () => {
    await attributionAPI.create([1], '5.6 Cyber')
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs', { account_ids: [1], model: '5.6 Cyber' })
  })
  it('loads account probe catalogs separately from detector candidates', async () => {
    const result = { models: ['target'], accounts: [{ account_id: 2, account_name: 'Account', status: 'partial', models: ['target'], error: 'model_catalog_failed' }] }
    post.mockResolvedValueOnce({ data: result })
    const signal = new AbortController().signal
    expect(await attributionAPI.probeModels([2], signal)).toEqual(result)
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/probe-models', { account_ids: [2] }, { timeout: 30000, signal })
  })
  it('returns the cancelled job from the administrator cancellation endpoint', async () => {
    const job = { id: 17, status: 'cancelled' }
    post.mockResolvedValueOnce({ data: job })
    expect(await attributionAPI.cancel(17)).toEqual(job)
    expect(post).toHaveBeenLastCalledWith('/admin/model-attribution/jobs/17/cancel')
  })
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
