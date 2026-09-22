import { beforeEach, describe, expect, it, vi } from 'vitest'
const { get, post, put, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, put, delete: remove } }))
import { exportCodexAuth, getAvailableModels, getCodexTurnState, refreshOpenAIToken, revokeOpenAIOAuth, setDefaultOpenAIOAuthOS } from '@/api/admin/accounts'

describe('OpenAI shared authorization and system identity APIs', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    for (const request of [get, post, put, remove]) request.mockResolvedValue({ data: { id: 42 } })
  })

  it('exports shared auth while retaining identity selection for model and turn-state requests', async () => {
    await exportCodexAuth(42)
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/codex-auth', undefined)
    await getAvailableModels(42, 'linux')
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/models', { params: { os: 'linux' } })
    const signal = new AbortController().signal
    await getCodexTurnState(42, signal, 'linux')
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/codex-turn-state', { params: { os: 'linux' }, signal })
  })

  it('sends manual imports to the explicit account without selecting a system grant', async () => {
    await refreshOpenAIToken('synthetic-refresh', 5, '/admin/openai/refresh-token', undefined, 42)
    expect(post).toHaveBeenCalledWith('/admin/openai/refresh-token', { refresh_token: 'synthetic-refresh', proxy_id: 5, account_id: 42 })
  })

  it('uses dedicated authorization mutations', async () => {
    await revokeOpenAIOAuth(42)
    expect(remove).toHaveBeenCalledWith('/admin/accounts/42/openai-oauth-authorization')
    await setDefaultOpenAIOAuthOS(42, 'macos')
    expect(put).toHaveBeenCalledWith('/admin/accounts/42/openai/os-auth/macos/default')
  })
})
