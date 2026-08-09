/**
 * Admin OpenAI fingerprint observation API.
 *
 * The top-level endpoint pages by user. Each lower level is loaded lazily
 * against the opaque snapshot token returned by the top-level request.
 */

import { apiClient } from '../client'

export type FingerprintObservationRelation = 'root' | 'descendant' | 'unthreaded'

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

export interface FingerprintObservationUserSummary {
  node_id: string
  user_id: number
  username: string
  email: string
  unattributed: boolean
  api_key_count: number
  session_count: number
  thread_count: number
  observation_count: number
  unattributed_observation_count: number
  first_observed_at: string
  last_observed_at: string
}

export interface FingerprintObservationAPIKeySummary {
  node_id: string
  user_id: number
  api_key_id: number
  api_key_name: string
  unattributed: boolean
  session_count: number
  thread_count: number
  observation_count: number
  unattributed_observation_count: number
  first_observed_at: string
  last_observed_at: string
}

export interface FingerprintObservationSessionSummary {
  node_id: string
  user_id: number
  api_key_id: number
  session_id: string
  unattributed: boolean
  child_thread_count: number
  has_root_thread: boolean
  has_unthreaded: boolean
  unthreaded_observation_count: number
  thread_count: number
  observation_count: number
  first_observed_at: string
  last_observed_at: string
}

export interface FingerprintObservationThreadSummary {
  node_id: string
  session_id: string
  thread_id: string
  parent_thread_id: string
  forked_from_thread_id: string
  relation: FingerprintObservationRelation
  unthreaded: boolean
  observation_count: number
  first_observed_at: string
  last_observed_at: string
}

export interface FingerprintObservationsResponse {
  enabled: boolean
  snapshot_token: string
  items: FingerprintObservationUserSummary[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface FingerprintObservationChildrenResponse<T> {
  items: T[]
  total: number
  next_cursor: string
}

export interface FingerprintObservationListParams {
  page?: number
  page_size?: number
  snapshot_token?: string
}

export interface FingerprintObservationChildrenParams {
  snapshot_token: string
  parent_node_id: string
  cursor?: string
  limit?: number
}

export interface FingerprintObservationRequestOptions {
  signal?: AbortSignal
}

async function list(
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

async function listChildren<T>(
  path: string,
  params: FingerprintObservationChildrenParams,
  options: FingerprintObservationRequestOptions
): Promise<FingerprintObservationChildrenResponse<T>> {
  const { data } = await apiClient.get<FingerprintObservationChildrenResponse<T>>(path, {
    params,
    signal: options.signal,
  })
  return data
}

function listAPIKeys(
  params: FingerprintObservationChildrenParams,
  options: FingerprintObservationRequestOptions = {}
): Promise<FingerprintObservationChildrenResponse<FingerprintObservationAPIKeySummary>> {
  return listChildren('/admin/openai/fingerprint-observations/api-keys', params, options)
}

function listSessions(
  params: FingerprintObservationChildrenParams,
  options: FingerprintObservationRequestOptions = {}
): Promise<FingerprintObservationChildrenResponse<FingerprintObservationSessionSummary>> {
  return listChildren('/admin/openai/fingerprint-observations/sessions', params, options)
}

function listThreads(
  params: FingerprintObservationChildrenParams,
  options: FingerprintObservationRequestOptions = {}
): Promise<FingerprintObservationChildrenResponse<FingerprintObservationThreadSummary>> {
  return listChildren('/admin/openai/fingerprint-observations/threads', params, options)
}

function listEntries(
  params: FingerprintObservationChildrenParams,
  options: FingerprintObservationRequestOptions = {}
): Promise<FingerprintObservationChildrenResponse<FingerprintObservationEntry>> {
  return listChildren('/admin/openai/fingerprint-observations/entries', params, options)
}

export const fingerprintObservationsAPI = {
  list,
  listAPIKeys,
  listSessions,
  listThreads,
  listEntries,
}

export default fingerprintObservationsAPI
