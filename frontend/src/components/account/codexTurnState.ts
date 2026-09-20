import type { Account, CodexTurnStateConfig } from '@/types'

export type EditableCodexTurnStateConfig = CodexTurnStateConfig & { collector_proxy_ids: number[] }

export function collectorProxyIDs(config?: Pick<CodexTurnStateConfig, 'collector_proxy_ids' | 'collector_proxy_id'>): number[] {
  if (Array.isArray(config?.collector_proxy_ids)) return [...config.collector_proxy_ids]
  return config?.collector_proxy_id ? [config.collector_proxy_id] : []
}

export function defaultCodexTurnStateConfig(): EditableCodexTurnStateConfig {
  return { enabled: false, account_type: 'auto', collector_proxy_ids: [] }
}

export function readCodexTurnStateConfig(config?: CodexTurnStateConfig): EditableCodexTurnStateConfig {
  return config ? { enabled: config.enabled, account_type: config.account_type, collector_proxy_ids: collectorProxyIDs(config) } : defaultCodexTurnStateConfig()
}

export function codexTurnStateConfigChanged(current: CodexTurnStateConfig, initial: CodexTurnStateConfig): boolean {
  return current.enabled !== initial.enabled || current.account_type !== initial.account_type ||
    JSON.stringify(collectorProxyIDs(current)) !== JSON.stringify(collectorProxyIDs(initial))
}

export function supportsCodexTurnState(account: Pick<Account, 'platform' | 'type' | 'credentials'>): boolean {
  if (account.platform !== 'openai' || account.type !== 'oauth') return false
  const modes = [account.credentials?.auth_mode, account.credentials?.openai_auth_mode]
    .map(value => String(value || '').trim().toLowerCase())
  return !modes.some(mode => ['personalaccesstoken', 'personal_access_token', 'agentidentity', 'agent_identity'].includes(mode))
}
