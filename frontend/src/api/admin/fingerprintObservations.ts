/**
 * Admin OpenAI fingerprint observation API (read-only).
 *
 * The observer captures the identity values that the gateway emits on each
 * OpenAI request while the diagnostic switch is enabled. The response is a
 * bounded, in-memory snapshot and is intentionally not persisted.
 */

import { apiClient } from '../client'

export interface FingerprintObservationEntry {
  timestamp: string
  account_id: number
  account_name: string
  pinned: boolean
  client_reported_installation_id: string
  outbound_installation_id: string
  /** Server-managed UUIDv7 session identity (empty when unavailable). */
  session_id: string
  /** Server-managed UUIDv7 thread identity (empty when unavailable). */
  thread_id: string
  user_agent: string
  originator: string
  openai_beta: string
  version: string
  inbound_endpoint: string
}

export interface FingerprintObservationsResponse {
  enabled: boolean
  entries: FingerprintObservationEntry[]
}

/**
 * List recent OpenAI fingerprint observations (newest first).
 * @param limit - optional cap on returned entries (server default 200).
 */
export async function list(limit?: number): Promise<FingerprintObservationsResponse> {
  const { data } = await apiClient.get<FingerprintObservationsResponse>(
    '/admin/openai/fingerprint-observations',
    { params: limit ? { limit } : undefined }
  )
  return data
}

export const fingerprintObservationsAPI = {
  list
}

export default fingerprintObservationsAPI
