import { apiClient } from '@/api/client'
import type { PaginatedResponse } from '@/types'
import type {
  PromptAuditConfig,
  PromptAuditEvent,
  PromptAuditGroup,
  PromptAuditRuntime,
  PromptAuditUpdateRequest,
  PromptDeletePreview,
  PromptDeleteResult,
  PromptEventFilters,
  PromptEventPage,
  PromptProbeResult,
  PromptAuditEndpointDraft,
  PromptKeywordUserOption,
  PromptKeywordKeyOption,
  PromptKeywordWhitelistKey,
  PromptKeywordWhitelistIdentity,
  PromptEnforcementOutcome,
} from './types'
import { eventFilterPayload, eventQueryParams } from './viewModel'

const basePath = '/admin/prompt-audit'

export async function getKeywordWhitelistIdentities(keys: PromptKeywordWhitelistKey[]): Promise<PromptKeywordWhitelistIdentity[]> {
  const { data } = await apiClient.post<{ items: PromptKeywordWhitelistIdentity[] }>(`${basePath}/keyword-whitelist-identities`, {
    keys: keys.map(({ user_id, api_key_id }) => ({ user_id, api_key_id })),
  })
  return data.items.map(({ user_id, api_key_id, user_email, username, key_name, available }) => ({ user_id, api_key_id, user_email, username, key_name, available }))
}

export async function listKeywordWhitelistUsers(search: string, page = 1): Promise<PaginatedResponse<PromptKeywordUserOption>> {
  const { data } = await apiClient.get<PaginatedResponse<PromptKeywordUserOption>>('/admin/users', {
    params: { search, page, page_size: 20 },
  })
  return { ...data, items: data.items.map(({ id, email }) => ({ id, email })) }
}

export async function listKeywordWhitelistKeys(userId: number, page = 1): Promise<PaginatedResponse<PromptKeywordKeyOption>> {
  const { data } = await apiClient.get<PaginatedResponse<PromptKeywordKeyOption>>(`/admin/users/${userId}/api-keys`, {
    params: { page, page_size: 20 },
  })
  // The general admin API also returns key secrets. Keep only selection metadata.
  return { ...data, items: data.items.map(({ id, user_id, name }) => ({ id, user_id, name })) }
}

export async function getConfig(): Promise<PromptAuditConfig> {
  const { data } = await apiClient.get<PromptAuditConfig>(`${basePath}/config`)
  return data
}

export async function updateConfig(payload: PromptAuditUpdateRequest): Promise<PromptAuditConfig> {
  const { data } = await apiClient.put<PromptAuditConfig>(`${basePath}/config`, payload)
  return data
}

export async function probeEndpoint(endpoint: PromptAuditEndpointDraft): Promise<PromptProbeResult> {
  const { data } = await apiClient.post<PromptProbeResult>(`${basePath}/endpoints/probe`, {
    endpoint: {
      id: endpoint.id,
      name: endpoint.name,
      protocol: 'openai_compatible',
      base_url: endpoint.base_url,
      model: endpoint.model,
      token: endpoint.token || undefined,
      timeout_ms: endpoint.timeout_ms,
      input_limit: endpoint.input_limit,
      enabled: endpoint.enabled,
    },
  })
  return data
}

export async function getRuntime(): Promise<PromptAuditRuntime> {
  const { data } = await apiClient.get<PromptAuditRuntime>(`${basePath}/runtime`)
  return data
}

export async function listEvents(
  filters: PromptEventFilters,
  page: number,
  pageSize: number,
): Promise<PromptEventPage> {
  const { data } = await apiClient.get<PromptEventPage>(`${basePath}/events`, {
    params: { page, page_size: pageSize, ...eventQueryParams(filters) },
  })
  return data
}

export async function getEvent(id: number): Promise<PromptAuditEvent> {
  const { data } = await apiClient.get<PromptAuditEvent>(`${basePath}/events/${id}`)
  return data
}

export async function unbanUser(userId: number): Promise<PromptEnforcementOutcome> {
  const { data } = await apiClient.post<PromptEnforcementOutcome>(`${basePath}/users/${userId}/unban`)
  return data
}

export async function deleteEvent(id: number): Promise<PromptDeleteResult> {
  const { data } = await apiClient.delete<PromptDeleteResult>(`${basePath}/events/${id}`)
  return data
}

export async function batchDeleteEvents(ids: number[]): Promise<PromptDeleteResult> {
  const { data } = await apiClient.post<PromptDeleteResult>(`${basePath}/events/batch-delete`, { ids })
  return data
}

export async function previewDelete(filters: PromptEventFilters): Promise<PromptDeletePreview> {
  const { data } = await apiClient.post<PromptDeletePreview>(
    `${basePath}/events/delete-preview`,
    eventFilterPayload(filters),
  )
  return data
}

export async function deleteEventsByFilter(
  filters: PromptEventFilters,
  preview: PromptDeletePreview,
): Promise<PromptDeleteResult> {
  const { data } = await apiClient.post<PromptDeleteResult>(`${basePath}/events/delete-by-filter`, {
    filter: eventFilterPayload(filters),
    snapshot_max_id: preview.snapshot_max_id,
    filter_hash: preview.filter_hash,
    confirmation_token: preview.confirmation_token,
    confirm: true,
  })
  return data
}

export async function listGroups(): Promise<PromptAuditGroup[]> {
  const { data } = await apiClient.get<PromptAuditGroup[]>('/admin/groups/all', {
    params: { include_inactive: true },
  })
  return data
}

export const promptAuditAPI = {
  getConfig,
  updateConfig,
  probeEndpoint,
  getRuntime,
  listEvents,
  getEvent,
  unbanUser,
  deleteEvent,
  batchDeleteEvents,
  previewDelete,
  deleteEventsByFilter,
  listGroups,
}

export default promptAuditAPI
