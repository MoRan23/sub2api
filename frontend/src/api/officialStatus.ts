import { apiClient } from './client'

export type OfficialStatusGroupID = 'apis' | 'chatgpt' | 'codex'

export interface OfficialStatusGroup {
  id: OfficialStatusGroupID
  name: string
  status: string
  fetched_at: string | null
  stale: boolean
}

export interface OfficialStatusIncident {
  id: string
  title: string
  url: string
  status: string
  groups: string[]
  components: string[]
  updated_at: string
  summary: string
}

export interface OfficialStatusResponse {
  source_url: string
  refresh_interval_seconds: number
  groups: OfficialStatusGroup[]
  incidents: OfficialStatusIncident[]
  incidents_fetched_at: string | null
  incidents_stale: boolean
}

export async function getOfficialStatus(signal?: AbortSignal): Promise<OfficialStatusResponse> {
  const { data } = await apiClient.get<OfficialStatusResponse>('/channel-monitor-v2/official-status', { signal })
  return data
}
