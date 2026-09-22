import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpenAIOAuthOSProfiles from '../OpenAIOAuthOSProfiles.vue'
import type { OpenAIOAuthOSProfiles as Profiles } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAI OS authorization cards', () => {
  it('does not treat installation identity as authorization and restricts default selection', async () => {
    const profiles = { default_os: 'windows', profiles: {
      windows: { installation_id: 'installed-windows', authorization: { status: 'unauthorized' } },
      macos: { installation_id: 'installed-macos', authorization: { status: 'authorized' } },
      linux: { installation_id: 'installed-linux', authorization: { status: 'reauth_required', last_error: 'reauth_required' } }
    } } as Profiles
    const wrapper = mount(OpenAIOAuthOSProfiles, { props: { profiles }, global: { stubs: { Icon: true } } })
    expect(wrapper.get('[data-testid="openai-os-authorization-windows"]').text()).toContain('unauthorized')
    expect(wrapper.find('[data-testid="openai-os-default-linux"]').exists()).toBe(false)
    await wrapper.get('[data-testid="openai-os-default-macos"]').trigger('click')
    expect(wrapper.emitted('set-default')?.[0]).toEqual(['macos'])
    await wrapper.get('[data-testid="openai-os-authorize-windows"]').trigger('click')
    expect(wrapper.emitted('authorize')?.[0]).toEqual(['windows'])
    await wrapper.get('[data-testid="openai-os-revoke-linux"]').trigger('click')
    expect(wrapper.emitted('revoke')?.[0]).toEqual(['linux'])
  })
})
