import { beforeEach, describe, expect, it, vi } from 'vitest'
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post } }))
import { exportCodexAuth, regenerateInstallationID } from '@/api/admin/accounts'

describe('OAuth identity management API', () => {
  beforeEach(() => { get.mockReset(); post.mockReset() })

  it('regenerates only the requested system and preserves the default compatibility call', async () => {
    post.mockResolvedValue({ data: { installation_id: 'new-id', os: 'linux' } })
    await regenerateInstallationID(42, 'linux')
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/42/installation-id/regenerate', { os: 'linux' })
    await regenerateInstallationID(42)
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/42/installation-id/regenerate', undefined)
  })

  it('reads a single auth export without triggering a token refresh', async () => {
    const exported = { auth: { auth_mode: 'chatgpt', OPENAI_API_KEY: null, tokens: { id_token: 'synthetic-id', access_token: 'synthetic-access', refresh_token: '', account_id: null } }, warnings: ['missing_refresh_token'] }
    get.mockResolvedValueOnce({ data: exported })
    await expect(exportCodexAuth(42)).resolves.toEqual(exported)
    expect(get).toHaveBeenCalledWith('/admin/accounts/42/codex-auth', undefined)
    expect(post).not.toHaveBeenCalled()
  })
})
