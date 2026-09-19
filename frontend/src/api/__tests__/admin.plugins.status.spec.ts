import { describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { status } from '@/api/admin/plugins'

describe('plugin runtime status API', () => {
  it('uses the read-only status endpoint and preserves an idle snapshot', async () => {
    const snapshot = { healthy: false, message: 'Plugin not running' }
    get.mockResolvedValueOnce({ data: snapshot })
    await expect(status(7)).resolves.toEqual(snapshot)
    expect(get).toHaveBeenCalledWith('/admin/plugins/7/status')
  })
})
