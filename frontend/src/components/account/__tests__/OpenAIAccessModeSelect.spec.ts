import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpenAIAccessModeSelect from '../OpenAIAccessModeSelect.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAIAccessModeSelect', () => {
  it('explains authoritative model sync only for Engine mode', async () => {
    const wrapper = mount(OpenAIAccessModeSelect, { props: { modelValue: 'generic' } })
    expect(wrapper.find('[data-testid="engine-models-sync-hint"]').exists()).toBe(false)
    await wrapper.setProps({ modelValue: 'codex_engine' })
    expect(wrapper.get('[data-testid="engine-models-sync-hint"]').text()).toBe('admin.accounts.openai.engineModelsSyncHint')
    wrapper.unmount()
  })
})
