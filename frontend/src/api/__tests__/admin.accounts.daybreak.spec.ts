import { describe, expect, it, vi } from 'vitest'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { accountsAPI } from '@/api/admin/accounts'

describe('Daybreak capabilities API', () => {
  it('uses the account-scoped read-only endpoint', async () => {
    const capabilities = { checked_at: null, blue_available: false, red_available: false, models: [], reason: 'unavailable' }
    get.mockResolvedValue({ data: capabilities })
    await expect(accountsAPI.getDaybreakCapabilities(42)).resolves.toEqual(capabilities)
    expect(get).toHaveBeenCalledWith('/admin/accounts/42/daybreak-capabilities')
  })
})
