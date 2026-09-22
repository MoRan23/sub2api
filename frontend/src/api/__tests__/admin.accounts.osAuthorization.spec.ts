import { beforeEach, describe, expect, it, vi } from 'vitest'
const { get, post, put, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, put, delete: remove } }))
import { exportCodexAuth, getAvailableModels, getCodexTurnState, refreshOpenAIToken, revokeOpenAIOAuthOS, setDefaultOpenAIOAuthOS } from '@/api/admin/accounts'

describe('OpenAI per-system account APIs', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    for (const request of [get, post, put, remove]) request.mockResolvedValue({ data: { id: 42 } })
  })

  it('selects the same OS in export, model listing, and turn-state requests', async () => {
    await exportCodexAuth(42, 'linux')
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/codex-auth', { params: { os: 'linux' } })
    await getAvailableModels(42, 'linux')
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/models', { params: { os: 'linux' } })
    const signal = new AbortController().signal
    await getCodexTurnState(42, signal, 'linux')
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/42/codex-turn-state', { params: { os: 'linux' }, signal })
  })

  it('sends manual imports to the explicit account and OS', async () => {
    await refreshOpenAIToken('synthetic-refresh', 5, '/admin/openai/refresh-token', undefined, 'macos', 42)
    expect(post).toHaveBeenCalledWith('/admin/openai/refresh-token', { refresh_token: 'synthetic-refresh', proxy_id: 5, os: 'macos', account_id: 42 })
  })

  it('uses dedicated authorization mutations', async () => {
    await revokeOpenAIOAuthOS(42, 'windows')
    expect(remove).toHaveBeenCalledWith('/admin/accounts/42/openai/os-auth/windows')
    await setDefaultOpenAIOAuthOS(42, 'macos')
    expect(put).toHaveBeenCalledWith('/admin/accounts/42/openai/os-auth/macos/default')
  })
})
