import type { Account, CodexTurnStateConfig } from '@/types'

export function defaultCodexTurnStateConfig(): CodexTurnStateConfig {
  return { enabled: false, account_type: 'auto', collector_proxy_id: null }
}

export function readCodexTurnStateConfig(config?: CodexTurnStateConfig): CodexTurnStateConfig {
  return config ? { ...config } : defaultCodexTurnStateConfig()
}

export function codexTurnStateConfigChanged(current: CodexTurnStateConfig, initial: CodexTurnStateConfig): boolean {
  return current.enabled !== initial.enabled || current.account_type !== initial.account_type ||
    current.collector_proxy_id !== initial.collector_proxy_id
}

export function supportsCodexTurnState(account: Pick<Account, 'platform' | 'type' | 'credentials'>): boolean {
  if (account.platform !== 'openai' || account.type !== 'oauth') return false
  const modes = [account.credentials?.auth_mode, account.credentials?.openai_auth_mode]
    .map(value => String(value || '').trim().toLowerCase())
  return !modes.some(mode => ['personalaccesstoken', 'personal_access_token', 'agentidentity', 'agent_identity'].includes(mode))
}
