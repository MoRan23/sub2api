import { beforeEach, describe, expect, it, vi } from 'vitest'
const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client, buildApiUrl: (url: string) => url }))
import { usageExportsAPI } from '@/api/admin/usageExports'

beforeEach(() => vi.resetAllMocks())
describe('usage export download', () => {
  it('uses a short ticket and a direct link without a buffered file request', async () => {
    client.post.mockResolvedValue({ data: { url: '/api/v1/usage-exports/download/ticket', expires_in: 60 } })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    await usageExportsAPI.download('task')
    expect(click).toHaveBeenCalledOnce()
    expect(client.post).toHaveBeenCalledWith('/admin/usage/exports/task/download-ticket', undefined, { signal: undefined })
    expect(client.get).not.toHaveBeenCalled()
    click.mockRestore()
  })
  it('rejects an unexpected download location', async () => {
    client.post.mockResolvedValue({ data: { url: 'https://example.invalid/file' } })
    await expect(usageExportsAPI.download('task')).rejects.toThrow('Invalid download URL')
  })
})
