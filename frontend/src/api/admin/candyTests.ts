import { apiClient } from '../client'

export const PELICAN_TEST_PROMPT_VERSION = 'pelican-v1'

export type CandyTestStatus = 'queued' | 'running' | 'generated' | 'abnormal' | 'failed' | 'cancelled' | 'skipped'
export type CandyTestAnswerKey = 'q1_fixed' | 'q2_adaptive' | 'q3_fixed' | 'q3_adaptive'

export interface CandyTestModelOption {
  id: string
  display_name: string
  reasoning_efforts: string[]
}

export interface CandyTestAccountOptions {
  account_id: number
  account_name: string
  models: CandyTestModelOption[]
  skip_reason?: string
}

export interface CandyTestOptions {
  models: CandyTestModelOption[]
  accounts: CandyTestAccountOptions[]
}

export interface CandyTestExecution {
  requested_model: string
  actual_model: string
  upstream_model: string
  reasoning_effort: string
  model_conflict: boolean
  model_evidence_source: string
  usage?: {
    input_tokens: number
    output_tokens: number
    cache_read_input_tokens?: number
    cache_creation_input_tokens?: number
  }
  completed: boolean
  duration_ms: number
}

export interface CandyTestItem {
  id: number
  batch_id: string
  account_id: number
  account_name: string
  model: string
  reasoning_effort: string
  prompt_version: string
  status: CandyTestStatus
  answers?: Partial<Record<CandyTestAnswerKey, number>>
  response_text?: string
  html?: string
  failure_code?: string
  execution?: CandyTestExecution
  created_at: string
  started_at: string | null
  finished_at: string | null
  cancel_requested: boolean
}

export interface CandyTestSummary {
  latest?: CandyTestItem
  active?: CandyTestItem
}

export interface CandyTestHistory {
  items: CandyTestItem[]
  summary?: CandyTestSummary
}

export interface CandyTestBatch {
  id: string
  model: string
  reasoning_effort: string
  prompt_version: string
  created_at: string
  finished_at: string | null
  total: number
  retained_total: number
  counts: Partial<Record<CandyTestStatus, number>>
  items: CandyTestItem[]
  page: number
  page_size: number
}

export interface CreateCandyTestRequest {
  account_ids: number[]
  model: string
  reasoning_effort: string
  idempotency_key: string
}

// Each live catalog lookup has a 15-second server deadline, with three lookups
// running concurrently. Leave time for the admin request and batch persistence.
function catalogRequestTimeout(accountIds: number[]): number {
  return Math.ceil(new Set(accountIds).size / 3) * 15000 + 30000
}

export const candyTestsAPI = {
  async options(accountIds: number[], signal?: AbortSignal): Promise<CandyTestOptions> {
    const { data } = await apiClient.post<CandyTestOptions>('/admin/accounts/candy-test-options', { account_ids: accountIds }, { signal, timeout: catalogRequestTimeout(accountIds) })
    return data
  },
  async create(request: CreateCandyTestRequest): Promise<CandyTestBatch> {
    const { data } = await apiClient.post<CandyTestBatch>('/admin/accounts/candy-tests', request, { timeout: catalogRequestTimeout(request.account_ids) })
    return data
  },
  async getBatch(id: string, page = 1, pageSize = 20): Promise<CandyTestBatch> {
    const { data } = await apiClient.get<CandyTestBatch>(`/admin/accounts/candy-tests/${encodeURIComponent(id)}`, { params: { page, page_size: pageSize } })
    return data
  },
  async cancel(id: string, itemIds?: number[]): Promise<void> {
    await apiClient.post(`/admin/accounts/candy-tests/${encodeURIComponent(id)}/cancel`, itemIds ? { item_ids: itemIds } : {})
  },
  async history(accountId: number): Promise<CandyTestHistory> {
    const { data } = await apiClient.get<CandyTestHistory>(`/admin/accounts/${accountId}/candy-tests`)
    return data
  },
}

export function isCandyTestActive(status: CandyTestStatus): boolean {
  return status === 'queued' || status === 'running'
}

export default candyTestsAPI
