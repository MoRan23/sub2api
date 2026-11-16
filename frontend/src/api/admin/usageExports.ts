import { apiClient, buildApiUrl } from '../client'
import type { AdminUsageQueryParams } from './usage'

export type UsageExportStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'deleting'
export interface UsageExportTask {
  id: string
  status: UsageExportStatus
  rows_written: number
  total_rows: number | null
  size_bytes: number
  error_code?: string
  created_at: string
  expires_at?: string | null
}
export interface UsageExportRequest extends AdminUsageQueryParams {
  language: 'zh' | 'en'
  timezone: string
}
export const usageExportsAPI = {
  async create(payload: UsageExportRequest, signal?: AbortSignal): Promise<UsageExportTask> {
    const { data } = await apiClient.post<UsageExportTask>('/admin/usage/exports', payload, { signal })
    return data
  },
  async list(signal?: AbortSignal): Promise<UsageExportTask[]> {
    const { data } = await apiClient.get<UsageExportTask[]>('/admin/usage/exports', { signal })
    return data
  },
  async cancel(id: string, signal?: AbortSignal): Promise<void> {
    await apiClient.post(`/admin/usage/exports/${encodeURIComponent(id)}/cancel`, undefined, { signal })
  },
  async remove(id: string, signal?: AbortSignal): Promise<void> {
    await apiClient.delete(`/admin/usage/exports/${encodeURIComponent(id)}`, { signal })
  },
  async download(id: string, signal?: AbortSignal): Promise<void> {
    const { data } = await apiClient.post<{ url: string; expires_in: number }>(`/admin/usage/exports/${encodeURIComponent(id)}/download-ticket`, undefined, { signal })
    if (signal?.aborted) return
    if (!data.url.startsWith('/api/v1/usage-exports/download/')) throw new Error('Invalid download URL')
    const anchor = document.createElement('a')
    anchor.href = buildApiUrl(data.url)
    anchor.download = ''
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
  }
}
