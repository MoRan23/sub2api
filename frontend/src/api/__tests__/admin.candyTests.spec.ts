import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { candyTestsAPI } from '@/api/admin/candyTests'

describe('candy test live catalog API', () => {
  beforeEach(() => { post.mockReset(); post.mockResolvedValue({ data: {} }) })

  it('allows a bounded catalog window for all selected accounts and supports cancellation', async () => {
    const ids = Array.from({ length: 100 }, (_, index) => index + 1)
    const controller = new AbortController()
    await candyTestsAPI.options(ids, controller.signal)
    expect(post).toHaveBeenCalledWith('/admin/accounts/candy-test-options', { account_ids: ids }, { signal: controller.signal, timeout: 540000 })
  })

  it('does not time out batch creation before its live capability checks finish', async () => {
    const request = { account_ids: [1, 2, 3, 4, 4], model: 'upstream-model', reasoning_effort: '', idempotency_key: 'submission' }
    await candyTestsAPI.create(request)
    expect(post).toHaveBeenCalledWith('/admin/accounts/candy-tests', request, { timeout: 60000 })
  })
})
