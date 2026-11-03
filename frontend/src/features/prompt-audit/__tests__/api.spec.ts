import { beforeEach, describe, expect, it, vi } from 'vitest'
import { emptyEventFilters } from '../viewModel'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))

import promptAuditAPI, { getKeywordWhitelistIdentities, listKeywordWhitelistKeys, listKeywordWhitelistUsers } from '../api'

describe('Prompt Audit API', () => {
  beforeEach(() => Object.values(client).forEach((mock) => mock.mockReset()))

  it('loads paginated user-owned keys and discards secret fields', async () => {
    client.get.mockResolvedValue({ data: { items: [{ id: 8, email: 'owner@example.test', notes: 'private' }], total: 21, page: 2, page_size: 20, pages: 2 } })
    expect((await listKeywordWhitelistUsers('owner', 2)).items).toEqual([{ id: 8, email: 'owner@example.test' }])
    expect(client.get).toHaveBeenLastCalledWith('/admin/users', { params: { search: 'owner', page: 2, page_size: 20 } })
    client.get.mockResolvedValue({ data: { items: [{ id: 17, user_id: 8, name: 'my-key', key: 'secret-must-not-remain' }], total: 21, page: 2, page_size: 20, pages: 2 } })
    const result = await listKeywordWhitelistKeys(8, 2)
    expect(result.items).toEqual([{ id: 17, user_id: 8, name: 'my-key' }])
    expect(JSON.stringify(result)).not.toContain('secret-must-not-remain')
    expect(client.get).toHaveBeenLastCalledWith('/admin/users/8/api-keys', { params: { page: 2, page_size: 20 } })
  })

  it('uses the independent admin route namespace', async () => {
    client.get.mockResolvedValue({ data: { config_version: 1 } })
    await promptAuditAPI.getConfig()
    expect(client.get).toHaveBeenCalledWith('/admin/prompt-audit/config')

    client.get.mockResolvedValue({ data: { process_status: 'running' } })
    await promptAuditAPI.getRuntime()
    expect(client.get).toHaveBeenCalledWith('/admin/prompt-audit/runtime')
  })

  it('looks up saved identity pairs using only IDs and retains no secret fields', async () => {
    const pair = { user_id: 8, api_key_id: 17, key: 'not-in-request' }
    client.post.mockResolvedValue({ data: { items: [{ user_id: 8, api_key_id: 17, user_email: 'owner@example.test', username: 'owner', key_name: 'main', available: true, key: 'not-in-response' }] } })
    expect(await getKeywordWhitelistIdentities([pair])).toEqual([{ user_id: 8, api_key_id: 17, user_email: 'owner@example.test', username: 'owner', key_name: 'main', available: true }])
    expect(client.post).toHaveBeenCalledWith('/admin/prompt-audit/keyword-whitelist-identities', { keys: [{ user_id: 8, api_key_id: 17 }] })
  })

  it('sends a temporary probe token only in the request and never invents response credentials', async () => {
    client.post.mockResolvedValue({ data: { ok: true, token_applied: true } })
    const result = await promptAuditAPI.probeEndpoint({
      id: 'guard-1', name: 'Guard', protocol: 'openai_compatible', base_url: 'http://127.0.0.1:8000', model: 'guard',
      token: 'api-canary-secret', clear_token: false, timeout_ms: 1000, input_limit: 1000, enabled: true, has_token: false, token_status: 'missing',
    })
    expect(client.post).toHaveBeenCalledWith('/admin/prompt-audit/endpoints/probe', expect.objectContaining({ endpoint: expect.objectContaining({ token: 'api-canary-secret' }) }))
    expect(JSON.stringify(result)).not.toContain('api-canary-secret')
  })

  it('passes a server preview token through the confirmed filter-delete contract', async () => {
    client.post.mockResolvedValue({ data: { deleted_events: 2, deleted_jobs: 2 } })
    const filters = emptyEventFilters()
    filters.start_at = '2026-07-15T00:00'
    filters.end_at = '2026-07-16T00:00'
    await promptAuditAPI.deleteEventsByFilter(filters, {
      matched_count: 2, filter_summary: {}, snapshot_max_id: 10, filter_hash: 'a'.repeat(64), confirmation_token: 'opaque-token', expires_at: '2026-07-16T00:05:00Z',
    })
    expect(client.post).toHaveBeenCalledWith('/admin/prompt-audit/events/delete-by-filter', expect.objectContaining({
      snapshot_max_id: 10, filter_hash: 'a'.repeat(64), confirmation_token: 'opaque-token', confirm: true,
    }))
  })
})
