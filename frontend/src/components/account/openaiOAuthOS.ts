import type { Account, OpenAIOAuthOS } from '@/types'

export const openAIOperatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
export const openAIOSLabels: Record<OpenAIOAuthOS, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }

export function defaultOpenAIOS(account?: Pick<Account, 'openai_oauth_os_profiles'> | null): OpenAIOAuthOS {
  return account?.openai_oauth_os_profiles?.default_os || 'windows'
}

/** Managed desktop identities apply to regular OAuth credentials, not PAT or Agent Identity. */
export function supportsManagedOpenAIOAuthIdentity(account: Pick<Account, 'platform' | 'type' | 'credentials'>): boolean {
  if (account.platform !== 'openai' || account.type !== 'oauth') return false
  const modes = [account.credentials?.auth_mode, account.credentials?.openai_auth_mode]
    .map(value => String(value || '').trim().toLowerCase())
  return !modes.some(mode => ['personalaccesstoken', 'personal_access_token', 'agentidentity', 'agent_identity'].includes(mode))
}
