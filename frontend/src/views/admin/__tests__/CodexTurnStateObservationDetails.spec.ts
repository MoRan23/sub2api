import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CodexTurnStateObservationDetails from '../components/CodexTurnStateObservationDetails.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))

describe('Codex turn-state wire observation details', () => {
  it('shows actual wire length, model and response shape without making a quality claim', () => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: true, action: 'injected', source: 'business', model: 'gpt-final',
        outbound_length: 332, response_length: 356, response_shape: 'suspect', renewal_reason: 'extended_state' } }
    })
    expect(wrapper.text()).toContain('gpt-final')
    expect(wrapper.text()).toContain('332')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.shapes.suspect (356)')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.sources.business')
    expect(wrapper.text()).toContain('extended_state')
  })
})
