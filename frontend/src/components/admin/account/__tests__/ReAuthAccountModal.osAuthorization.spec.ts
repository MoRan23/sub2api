import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ReAuthAccountModal from '../ReAuthAccountModal.vue'
import type { Account } from '@/types'

const { generateAuthUrl, exchangeCode, refreshOpenAIToken, applyOAuthCredentials } = vi.hoisted(() => ({ generateAuthUrl: vi.fn(), exchangeCode: vi.fn(), refreshOpenAIToken: vi.fn(), applyOAuthCredentials: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { generateAuthUrl, exchangeCode, refreshOpenAIToken, applyOAuthCredentials } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))

const account = {
  id: 42, name: 'OpenAI', platform: 'openai', type: 'oauth', proxy_id: 5, credentials: {},
  openai_oauth_os_profiles: { default_os: 'macos', profiles: {
    windows: { authorization: { status: 'unauthorized' } },
    macos: { authorization: { status: 'authorized' } },
    linux: { authorization: { status: 'unauthorized' } }
  } }
} as Account

function render() {
  return mount(ReAuthAccountModal, {
    props: { show: true, account },
    global: { stubs: {
      BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' },
      Icon: true,
      OAuthAuthorizationFlow: defineComponent({
        emits: ['generate-url', 'validate-refresh-token'],
        setup(_, { expose }) { expose({ authCode: 'synthetic-code', oauthState: 'nonce', inputMethod: 'manual', reset: vi.fn() }); return {} },
        template: '<div><button data-testid="generate" @click="$emit(\'generate-url\')">Generate</button><button data-testid="import" @click="$emit(\'validate-refresh-token\', \'synthetic-rt\')">Import</button></div>'
      })
    } }
  })
}

describe('OpenAI authorization target', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    generateAuthUrl.mockResolvedValue({ auth_url: 'https://example.test/auth?state=nonce', session_id: 'bound-session' })
    exchangeCode.mockResolvedValue({ account, os: 'linux' })
    refreshOpenAIToken.mockResolvedValue({ account, os: 'linux' })
  })

  it('defaults to account OS, binds a selected slot, freezes it, and accepts the server account result', async () => {
    const wrapper = render()
    const selector = wrapper.get('[data-testid="openai-oauth-os-select"]')
    expect((selector.element as HTMLSelectElement).value).toBe('macos')
    await selector.setValue('linux')
    await wrapper.get('[data-testid="generate"]').trigger('click')
    await flushPromises()
    expect(generateAuthUrl).toHaveBeenCalledWith('/admin/openai/generate-auth-url', { proxy_id: 5, account_id: 42, os: 'linux', purpose: 'authorize' })
    expect(selector.attributes('disabled')).toBeDefined()
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.oauth.completeAuth')!.trigger('click')
    await flushPromises()
    expect(exchangeCode).toHaveBeenCalledWith('/admin/openai/exchange-code', { session_id: 'bound-session', code: 'synthetic-code', state: 'nonce', proxy_id: 5 })
    expect(applyOAuthCredentials).not.toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')?.[0]).toEqual([account])
    wrapper.unmount()
  })

  it('imports a refresh token directly into the selected bound account slot', async () => {
    const wrapper = render()
    await wrapper.get('[data-testid="openai-oauth-os-select"]').setValue('linux')
    await wrapper.get('[data-testid="import"]').trigger('click')
    await flushPromises()
    expect(refreshOpenAIToken).toHaveBeenCalledWith('synthetic-rt', 5, '/admin/openai/refresh-token', undefined, 'linux', 42)
    expect(applyOAuthCredentials).not.toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')?.[0]).toEqual([account])
    wrapper.unmount()
  })
})
