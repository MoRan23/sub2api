import { describe, expect, it } from 'vitest'
import { defaultOpenAIOS, supportsManagedOpenAIOAuthIdentity } from '../openaiOAuthOS'

describe('managed OpenAI identities independent of retired turn-state cache', () => {
  it('preserves ordinary OAuth identities and the default system', () => {
    expect(supportsManagedOpenAIOAuthIdentity({ platform: 'openai', type: 'oauth', credentials: {} })).toBe(true)
    expect(defaultOpenAIOS()).toBe('windows')
    expect(defaultOpenAIOS({ openai_oauth_os_profiles: { default_os: 'linux', profiles: {} as never } })).toBe('linux')
  })
  it.each(['personalaccesstoken', 'personal_access_token', 'agentidentity', 'agent_identity'])('does not treat %s as managed OAuth', mode => {
    for (const key of ['auth_mode', 'openai_auth_mode']) {
      expect(supportsManagedOpenAIOAuthIdentity({ platform: 'openai', type: 'oauth', credentials: { [key]: ` ${mode.toUpperCase()} ` } })).toBe(false)
    }
  })
  it('excludes API keys and other platforms', () => {
    expect(supportsManagedOpenAIOAuthIdentity({ platform: 'openai', type: 'apikey' })).toBe(false)
    expect(supportsManagedOpenAIOAuthIdentity({ platform: 'anthropic', type: 'oauth' })).toBe(false)
  })
})
