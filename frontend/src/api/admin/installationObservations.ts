/**
 * Admin OpenAI installation_id observation API (read-only).
 *
 * The gateway can pin each OpenAI OAuth account to its own installation_id and
 * emit only that value upstream. This endpoint exposes the in-memory ring buffer
 * of recent outbound requests so operators can see, per request, the pinned vs
 * client-reported installation_id plus the outbound UA / originator / OpenAI-Beta
 * / version. Data is only recorded while global observation is enabled; when it
 * is off the buffer is cleared and `enabled` is false.
 */

import { apiClient } from '../client'

export interface InstallationObservationEntry {
  timestamp: string
  account_id: number
  account_name: string
  pinned: boolean
  client_reported_installation_id: string
  outbound_installation_id: string
  user_agent: string
  originator: string
  openai_beta: string
  version: string
  inbound_endpoint: string
}

export interface InstallationObservationsResponse {
  enabled: boolean
  entries: InstallationObservationEntry[]
}

/**
 * List recent installation_id observations (newest first).
 * @param limit - optional cap on returned entries (server default 200).
 */
export async function list(limit?: number): Promise<InstallationObservationsResponse> {
  const { data } = await apiClient.get<InstallationObservationsResponse>(
    '/admin/openai/installation-observations',
    { params: limit ? { limit } : undefined }
  )
  return data
}

export const installationObservationsAPI = {
  list
}

export default installationObservationsAPI
