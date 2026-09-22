import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpenAIOAuthOSProfiles from '../OpenAIOAuthOSProfiles.vue'
import type { OpenAIOAuthOSProfiles as Profiles } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAI OS identity cards', () => {
  it('keeps identity controls without an additional authorization panel', async () => {
    const profiles = { default_os: 'windows', authorization: { status: 'authorized' }, profiles: {
      windows: { installation_id: 'installed-windows', authorization: { status: 'unauthorized' } },
      macos: { installation_id: 'installed-macos', authorization: { status: 'authorized' } },
      linux: { installation_id: 'installed-linux', authorization: { status: 'reauth_required', last_error: 'reauth_required' } }
    } } as Profiles
    const wrapper = mount(OpenAIOAuthOSProfiles, { props: { profiles }, global: { stubs: { Icon: true } } })
    expect(wrapper.find('[data-testid="openai-account-authorization"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="openai-os-authorization-windows"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="openai-os-authorize-linux"]').exists()).toBe(false)
    await wrapper.get('[data-testid="openai-os-default-linux"]').trigger('click')
    expect(wrapper.emitted('set-default')?.[0]).toEqual(['linux'])
    await wrapper.get('[data-testid="openai-os-default-macos"]').trigger('click')
    expect(wrapper.emitted('set-default')?.[1]).toEqual(['macos'])
    expect(wrapper.find('[data-testid="openai-account-authorize"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="openai-account-revoke"]').exists()).toBe(false)
    await wrapper.get('[data-testid="openai-installation-regenerate-windows"]').trigger('click')
    expect(wrapper.emitted('regenerate')?.[0]).toEqual(['windows'])
  })
  it('shows inherited identities without mutation controls or authorization state', () => {
    const profiles = { default_os: 'windows', authorization: { status: 'reauth_required' }, profiles: {} } as Profiles
    const wrapper = mount(OpenAIOAuthOSProfiles, { props: { profiles, inherited: true }, global: { stubs: { Icon: true } } })
    expect(wrapper.text()).not.toContain('reauth_required')
    expect(wrapper.find('[data-testid="openai-account-authorization"]').exists()).toBe(false)
    expect(wrapper.findAll('button')).toHaveLength(0)
  })
})
