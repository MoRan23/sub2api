import { apiClient } from '../client'

export type CodexTelemetryType = 'analytics' | 'metrics'
export type CodexTelemetryStatus = 'queued' | 'sent' | 'failed' | 'dropped' | 'cancelled' | 'skipped'

export interface CodexTelemetryEntry {
  id: number
  created_at: string
  updated_at: string
  account_id: number
  account_name: string
  type: CodexTelemetryType
  status: CodexTelemetryStatus
  event_names: string[]
  is_worktree?: boolean | null
  contains_simulated: boolean
  attempt_id: number
  attempt_count: number
  turn_count: number
  session_id: string
  thread_id: string
  turn_id: string
  parent_thread_id: string
  parent_turn_id: string
  root_turn_id: string
  model: string
  user_agent: string
  originator: string
  version: string
  http_status: number
  error: string
}

export interface CodexTelemetryObservationsResponse {
  configured_enabled: boolean
  effective_enabled: boolean
  forced_off_reason: string
  queue_depth: number
  counters: Record<'attempts' | CodexTelemetryStatus, number>
  items: CodexTelemetryEntry[]
  total: number
  page: number
  page_size: number
}

export interface CodexTelemetryListParams {
  account_id?: number
  status?: CodexTelemetryStatus
  type?: CodexTelemetryType
  page?: number
  page_size?: number
}

async function list(
  params: CodexTelemetryListParams = {},
  options: { signal?: AbortSignal } = {},
): Promise<CodexTelemetryObservationsResponse> {
  const { data } = await apiClient.get<CodexTelemetryObservationsResponse>(
    '/admin/openai/telemetry-observations',
    { params, signal: options.signal },
  )
  return {
    ...data,
    items: Array.isArray(data.items)
      ? data.items.filter((entry) => entry != null).map((entry) => ({
        ...entry,
        event_names: Array.isArray(entry.event_names)
          ? entry.event_names.filter((name): name is string => typeof name === 'string')
          : [],
      }))
      : [],
  }
}

export const codexTelemetryAPI = { list }
export default codexTelemetryAPI
