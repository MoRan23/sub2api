import type { Account, CodexTurnStateConfig } from '@/types'

export type EditableCodexTurnStateConfig = CodexTurnStateConfig & { collector_proxy_ids: number[]; use_ticket_proxy: boolean }

export function collectorProxyIDs(config?: Pick<CodexTurnStateConfig, 'collector_proxy_ids' | 'collector_proxy_id'>): number[] {
  if (Array.isArray(config?.collector_proxy_ids)) return [...config.collector_proxy_ids]
  return config?.collector_proxy_id ? [config.collector_proxy_id] : []
}

export function defaultCodexTurnStateConfig(): EditableCodexTurnStateConfig {
  return { enabled: false, account_type: 'auto', use_ticket_proxy: true, collector_proxy_ids: [] }
}

export function readCodexTurnStateConfig(config?: CodexTurnStateConfig): EditableCodexTurnStateConfig {
  return config ? {
    enabled: config.enabled,
    account_type: config.account_type,
    use_ticket_proxy: config.use_ticket_proxy !== false,
    collector_proxy_ids: collectorProxyIDs(config),
  } : defaultCodexTurnStateConfig()
}

export function codexTurnStateConfigChanged(current: CodexTurnStateConfig, initial: CodexTurnStateConfig): boolean {
  return current.enabled !== initial.enabled || current.account_type !== initial.account_type ||
    (current.use_ticket_proxy !== false) !== (initial.use_ticket_proxy !== false) ||
    JSON.stringify(collectorProxyIDs(current)) !== JSON.stringify(collectorProxyIDs(initial))
}

export function supportsCodexTurnState(account: Pick<Account, 'platform' | 'type' | 'credentials'>): boolean {
  if (account.platform !== 'openai' || account.type !== 'oauth') return false
  const modes = [account.credentials?.auth_mode, account.credentials?.openai_auth_mode]
    .map(value => String(value || '').trim().toLowerCase())
  return !modes.some(mode => ['personalaccesstoken', 'personal_access_token', 'agentidentity', 'agent_identity'].includes(mode))
}

export function codexTurnStatePackageExpiry(model: { expires_at?: string; cookie_bundle_expires_at?: string }): number {
  const expiries = [model.expires_at, model.cookie_bundle_expires_at]
    .map(value => value ? Date.parse(value) : Number.NaN)
    .filter(Number.isFinite)
  return expiries.length ? Math.min(...expiries) : Number.NaN
}
