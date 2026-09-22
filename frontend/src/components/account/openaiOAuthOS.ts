import type { Account, OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'

export const openAIOperatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
export const openAIOSLabels: Record<OpenAIOAuthOS, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }

export function defaultOpenAIOS(account?: Pick<Account, 'openai_oauth_os_profiles'> | null): OpenAIOAuthOS {
  return account?.openai_oauth_os_profiles?.default_os || 'windows'
}

export function isOpenAIOSAuthorized(profiles: OpenAIOAuthOSProfiles | undefined, os: OpenAIOAuthOS): boolean {
  return profiles?.profiles?.[os]?.authorization?.status === 'authorized'
}
