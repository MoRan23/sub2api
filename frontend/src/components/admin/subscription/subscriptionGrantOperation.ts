import type { AssignSubscriptionRequest, BulkAssignSubscriptionRequest } from '@/types'

type GrantRequest = AssignSubscriptionRequest | BulkAssignSubscriptionRequest
const pendingKeys = new Map<string, string>()

export function prepareSubscriptionGrant<T extends GrantRequest>(input: T) {
  const request = {
    ...input,
    ...('user_ids' in input ? { user_ids: [...new Set(input.user_ids)].sort((a, b) => a - b) } : {})
  } as T
  let adminId: number | string = 'unknown'
  try {
    adminId = JSON.parse(localStorage.getItem('auth_user') ?? 'null')?.id ?? 'unknown'
  } catch { /* Keep in-memory retry protection when storage is unavailable. */ }
  const storageKey = `sub2api:admin:subscription-grant:${adminId}:${JSON.stringify(request)}`
  let key = pendingKeys.get(storageKey)
  try { key ??= sessionStorage.getItem(storageKey) ?? undefined } catch { /* Optional persistence. */ }
  if (!key) {
    const id = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
    key = `subscription-grant-${adminId}-${id}`
  }
  pendingKeys.set(storageKey, key)
  try { sessionStorage.setItem(storageKey, key) } catch { /* Optional persistence. */ }
  return { request, key, storageKey }
}

export function completeSubscriptionGrant(operation: { storageKey: string }) {
  pendingKeys.delete(operation.storageKey)
  try { sessionStorage.removeItem(operation.storageKey) } catch { /* Optional persistence. */ }
}
