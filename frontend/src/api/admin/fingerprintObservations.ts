/**
 * Admin OpenAI fingerprint observation API (read-only).
 *
 * Sessions are the pagination unit. A positive snapshot sequence pins later
 * pages to the same ring-buffer high-water mark as page one.
 */

import { apiClient } from '../client'

export type FingerprintObservationRelation = 'root' | 'descendant'

export interface FingerprintObservationEntry {
  sequence_id: number
  timestamp: string
  user_id: number
  username: string
  email: string
  api_key_id: number
  api_key_name: string
  account_id: number
  account_name: string
  pinned: boolean
  client_reported_installation_id: string
  outbound_installation_id: string
  session_id: string
  thread_id: string
  parent_thread_id: string
  forked_from_thread_id: string
  user_agent: string
  originator: string
  openai_beta: string
  version: string
  inbound_endpoint: string
}

export interface FingerprintObservationThreadNode {
  thread_id: string
  parent_thread_id: string
  forked_from_thread_id: string
  relation: FingerprintObservationRelation
  first_observed_at: string
  last_observed_at: string
  observation_count: number
  observations: FingerprintObservationEntry[]
}

export interface FingerprintObservationSessionNode {
  user_id: number
  username: string
  email: string
  api_key_id: number
  api_key_name: string
  session_id: string
  first_observed_at: string
  last_observed_at: string
  observation_count: number
  root_thread: FingerprintObservationThreadNode | null
  child_threads: FingerprintObservationThreadNode[]
  unthreaded_observations: FingerprintObservationEntry[]
}

export interface FingerprintObservationsResponse {
  enabled: boolean
  items: FingerprintObservationSessionNode[]
  total: number
  page: number
  page_size: number
  pages: number
  snapshot_seq: number
}

export interface FingerprintObservationListParams {
  page?: number
  page_size?: number
  snapshot_seq?: number
}

export interface FingerprintObservationRequestOptions {
  signal?: AbortSignal
}

export async function list(
  params: FingerprintObservationListParams = {},
  options: FingerprintObservationRequestOptions = {}
): Promise<FingerprintObservationsResponse> {
  const { data } = await apiClient.get<FingerprintObservationsResponse>(
    '/admin/openai/fingerprint-observations',
    {
      params,
      signal: options.signal,
    }
  )
  return data
}

export const fingerprintObservationsAPI = {
  list,
}

export default fingerprintObservationsAPI
