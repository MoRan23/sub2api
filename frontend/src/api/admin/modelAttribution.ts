import { apiClient } from '../client'

export interface AttributionPolicy { model: string; high_models: string[]; low_models: string[] }
export interface AttributionGroup extends AttributionPolicy { group_id: number; enabled: boolean }
export interface NewAccountTestConfig { attribution: boolean; pelican: boolean; attribution_model: string; pelican_model: string }
export interface AttributionConfig { version: number; enabled: boolean; base_url: string; default: AttributionPolicy; groups: AttributionGroup[]; group_priority: number[]; new_account_tests: NewAccountTestConfig }
export type AttributionStatus = 'queued' | 'running' | 'passed' | 'mismatch' | 'abnormal' | 'failed' | 'skipped'
export interface AttributionJob {
  id: number
  account_id: number
  account_name: string
  source: string
  status: AttributionStatus
  reason?: string
  snapshot: { config_version: number; group_id: number; policy: AttributionPolicy }
  result: {
    analysis?: { prediction: string; probability: number; used_outputs: number; results?: { model: string; probability: number }[]; diagnostics?: { index: number; accepted: boolean; parsed_numbers: number; minimum_numbers: number }[] }
    duration_ms: number
    retries?: number
    action: string
    pass_streak?: number
    before?: Record<string, unknown>
    after?: Record<string, unknown>
    actual_models?: string[]
    upstream_models?: string[]
    usage?: ({ input_tokens: number; output_tokens: number } | null)[]
  }
  created_at: string
  started_at?: string
  finished_at?: string
}
export interface AttributionSummary { latest?: AttributionJob; active?: AttributionJob; skip_reason?: string }
export interface AttributionPage { items: AttributionJob[]; total: number; page: number; page_size: number; queued: number; running: number }
const root = '/admin/model-attribution'
export const attributionAPI = {
  async config(): Promise<AttributionConfig> { return (await apiClient.get<AttributionConfig>(`${root}/config`)).data },
  async save(config: AttributionConfig): Promise<AttributionConfig> { return (await apiClient.put<AttributionConfig>(`${root}/config`, config)).data },
  async models(base_url: string): Promise<string[]> { return (await apiClient.post<{ models: string[] }>(`${root}/models`, { base_url }, { timeout: 25000 })).data.models },
  async create(account_ids: number[], model?: string): Promise<AttributionJob[]> { return (await apiClient.post<{ items: AttributionJob[] }>(`${root}/jobs`, { account_ids, ...(model ? { model } : {}) })).data.items },
  async history(account_id?: number, page = 1): Promise<AttributionPage> { return (await apiClient.get<AttributionPage>(`${root}/jobs`, { params: { account_id, page, page_size: 20 } })).data },
  async job(id: number): Promise<AttributionJob> { return (await apiClient.get<AttributionJob>(`${root}/jobs/${id}`)).data }
}
