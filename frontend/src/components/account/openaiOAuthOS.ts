import type { Account, OpenAIOAuthAuthorizationSummary, OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'

export const openAIOperatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
export const openAIOSLabels: Record<OpenAIOAuthOS, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }

export function defaultOpenAIOS(account?: Pick<Account, 'openai_oauth_os_profiles'> | null): OpenAIOAuthOS {
  return account?.openai_oauth_os_profiles?.default_os || 'windows'
}

export function openAIAccountAuthorization(profiles: OpenAIOAuthOSProfiles | undefined): OpenAIOAuthAuthorizationSummary | undefined {
  // Older responses exposed the shared credential mirror on the default profile.
  return profiles?.authorization ?? (profiles?.default_os ? profiles.profiles?.[profiles.default_os]?.authorization : undefined)
}
