import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CodexTurnStateObservationDetails from '../components/CodexTurnStateObservationDetails.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))

describe('Codex turn-state wire observation details', () => {
  it.each(['model_excluded', 'model_policy_unavailable', 'model_policy_changed', 'snapshot_unavailable'])('keeps an enabled account distinct from maintenance paused by %s', (reason) => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { account_enabled: true, enabled: false, maintenance_reason: reason,
        action: 'passthrough', model: 'gpt-outside', outbound_length: 0, response_length: 332, response_shape: 'target' } }
    })
    const values = Object.fromEntries(wrapper.findAll('dl > div').map(item => [item.get('dt').text(), item.get('dd').text()]))
    expect(values['admin.accounts.codexTurnState.accountCache']).toBe('admin.accounts.codexTurnState.cacheEnabled')
    expect(values['admin.accounts.codexTurnState.cacheMode']).toBe('admin.accounts.codexTurnState.maintenancePaused')
    expect(values['admin.accounts.codexTurnState.maintenanceReason']).toBe(`admin.accounts.codexTurnState.reasons.${reason}`)
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.observationOnly')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.shapes.target (332)')
  })

  it('shows actual wire length, model and response shape without making a quality claim', () => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: true, action: 'injected', source: 'business', model: 'gpt-final',
        outbound_length: 332, response_length: 356, response_shape: 'suspect', renewal_reason: 'extended_state' } }
    })
    expect(wrapper.text()).toContain('gpt-final')
    expect(wrapper.text()).toContain('332')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.shapes.suspect (356)')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.sources.business')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.cacheEnabled')
    expect(wrapper.text()).toContain('extended_state')
  })

  it.each([
    [292, 'target'], [332, 'target'], [312, 'suspect'], [356, 'suspect'],
  ])('shows a %s character response while cache maintenance is disabled', (length, shape) => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: false, action: 'passthrough', source: 'client', model: 'gpt-observed',
        outbound_length: 292, response_length: length, response_shape: shape } }
    })
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.observationOnly')
    expect(wrapper.text()).toContain(`admin.accounts.codexTurnState.shapes.${shape} (${length})`)
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.actions.passthrough')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.sources.client')
    expect(wrapper.text()).toContain('gpt-observed')
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.cacheEnabled')
  })

  it('keeps absent request and response state distinct from a cached value', () => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: false, action: 'passthrough', model: 'gpt-observed', outbound_length: 0 } }
    })
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.observationOnly')
    expect(wrapper.findAll('dd').map(item => item.text())).toContain('0')
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.shapes.target')
    expect(wrapper.text()).not.toContain('admin.accounts.codexTurnState.sources.business')
  })

  it.each(['header', 'metadata'] as const)('distinguishes the observed response %s from the outbound source', (responseSource) => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: false, action: 'passthrough', source: 'client', model: 'gpt-observed',
        outbound_length: 292, response_length: 332, response_shape: 'unknown', response_source: responseSource } }
    })
    expect(wrapper.text()).toContain(`admin.accounts.codexTurnState.sources.response_${responseSource}`)
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.sources.client')
    expect(wrapper.text()).toContain('admin.accounts.codexTurnState.shapes.unknown (332)')
  })

  it.each([
    ['body', 0, 292], ['header_and_body', 292, 332], ['ws_handshake_and_frame', 332, 292], ['ws_frame', 0, 332],
  ] as const)('shows the actual %s carrier lengths independently', (carrier, headerLength, bodyLength) => {
    const wrapper = mount(CodexTurnStateObservationDetails, {
      props: { state: { enabled: false, action: 'passthrough', source: 'client', model: 'gpt-observed',
        outbound_length: headerLength || bodyLength, outbound_carrier: carrier,
        outbound_header_length: headerLength, outbound_body_length: bodyLength } }
    })
    const values = Object.fromEntries(wrapper.findAll('dl > div').map(item => [item.get('dt').text(), item.get('dd').text()]))
    expect(values['admin.accounts.codexTurnState.carrier']).toBe(`admin.accounts.codexTurnState.carriers.${carrier}`)
    expect(values['admin.accounts.codexTurnState.headerLength']).toBe(String(headerLength))
    expect(values['admin.accounts.codexTurnState.bodyLength']).toBe(String(bodyLength))
  })
})
