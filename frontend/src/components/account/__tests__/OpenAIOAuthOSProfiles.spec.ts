import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpenAIOAuthOSProfiles from '../OpenAIOAuthOSProfiles.vue'
import type { OpenAIOAuthOSProfiles as Profiles } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAI shared authorization and OS identity cards', () => {
  it('shows one authorization and allows every saved identity as default', async () => {
    const profiles = { default_os: 'windows', authorization: { status: 'authorized' }, profiles: {
      windows: { installation_id: 'installed-windows', authorization: { status: 'unauthorized' } },
      macos: { installation_id: 'installed-macos', authorization: { status: 'authorized' } },
      linux: { installation_id: 'installed-linux', authorization: { status: 'reauth_required', last_error: 'reauth_required' } }
    } } as Profiles
    const wrapper = mount(OpenAIOAuthOSProfiles, { props: { profiles }, global: { stubs: { Icon: true } } })
    expect(wrapper.get('[data-testid="openai-account-authorization"]').text()).toContain('authorizationStatus.authorized')
    expect(wrapper.find('[data-testid="openai-os-authorization-windows"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="openai-os-authorize-linux"]').exists()).toBe(false)
    await wrapper.get('[data-testid="openai-os-default-linux"]').trigger('click')
    expect(wrapper.emitted('set-default')?.[0]).toEqual(['linux'])
    await wrapper.get('[data-testid="openai-os-default-macos"]').trigger('click')
    expect(wrapper.emitted('set-default')?.[1]).toEqual(['macos'])
    await wrapper.get('[data-testid="openai-account-authorize"]').trigger('click')
    expect(wrapper.emitted('authorize')?.[0]).toEqual([])
    await wrapper.get('[data-testid="openai-account-revoke"]').trigger('click')
    expect(wrapper.emitted('revoke')?.[0]).toEqual([])
  })
  it('shows inherited account authorization without mutation controls', () => {
    const profiles = { default_os: 'windows', authorization: { status: 'reauth_required' }, profiles: {} } as Profiles
    const wrapper = mount(OpenAIOAuthOSProfiles, { props: { profiles, inherited: true }, global: { stubs: { Icon: true } } })
    expect(wrapper.get('[data-testid="openai-account-authorization"]').text()).toContain('reauth_required')
    expect(wrapper.findAll('button')).toHaveLength(0)
  })
})
