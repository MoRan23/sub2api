import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateObservation, CodexTurnStateStatus } from '@/api/admin/accounts'
import AccountCodexTurnStateCell from '../AccountCodexTurnStateCell.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : ''), te: () => true
}) }))
const now = Date.parse('2026-09-20T12:00:00Z')
const models = ['gpt-6-astra', 'gpt-5.6-sol']
const account = { id: 1, platform: 'openai', type: 'oauth', credentials: {}, name: 'OAuth' } as AccountListItem
const status: CodexTurnStateStatus = {
  account_id: 1, owner_account_id: 1, inherited: false, enabled: true, account_type: 'auto',
  resolved_account_type: 'team_business', collector_proxy_id: null, expected_length: 332, reason: '',
  models: [{ model: models[0]!, state: 'ready', shape: 'target', source: 'business', token_length: 332,
    cipher_blocks: 12, expires_at: '2026-09-20T12:30:00Z', remaining_seconds: 1800, collector_paused: false }]
}
function show(overrides: Partial<InstanceType<typeof AccountCodexTurnStateCell>['$props']> = {}) {
  return mount(AccountCodexTurnStateCell, { props: { account, status, models, loading: false, failed: false, now, observedAt: now, ...overrides } })
}
function observation(overrides: Partial<CodexTurnStateObservation> = {}): CodexTurnStateObservation {
  return { model: models[0]!, observed_at: '2026-09-20T12:00:00Z', outbound_length: 0, response_length: 332,
    response_shape: 'target', response_observed_shape: 'team_business_target', ...overrides }
}
function colors(wrapper: ReturnType<typeof show>) {
  return wrapper.findAll('[data-testid="codex-turn-state-dot"]').map(dot => dot.attributes('data-state'))
}

describe('AccountCodexTurnStateCell', () => {
  it('renders only fixed-height model rows and opens details for the entire cell', async () => {
    const wrapper = show()
    expect(wrapper.text()).toBe(models.join(''))
    expect(wrapper.get('button').attributes('style')).toContain('height: 44px')
    const rows = wrapper.findAll('[data-testid^="codex-turn-state-model-"]')
    expect(rows).toHaveLength(2)
    expect(rows.every(row => row.classes().includes('h-5'))).toBe(true)
    expect(colors(wrapper)).toEqual(['green', 'gray'])
    expect(rows[0]!.attributes('title')).toContain('dotCachedTarget')
    expect(rows[0]!.attributes('aria-label')).toContain(models[0])
    await rows[0]!.trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it.each([
    { response_length: 292, response_observed_shape: 'personal_target' },
    { response_length: 332, response_observed_shape: 'team_business_target' },
  ])('shows a green latest target response with cache disabled (%s)', (shape) => {
    const wrapper = show({ status: { ...status, enabled: false, expected_length: shape.response_length, observations: [observation(shape)] } })
    expect(colors(wrapper)[0]).toBe('green')
    const row = wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`)
    expect(row.attributes('title')).toContain('dotObservedTarget')
    expect(row.attributes('title')).toContain('passiveOnly')
    expect(row.attributes('title')).toContain(`characters{"count":${shape.response_length}}`)
    expect(row.attributes('title')).toContain(new Date('2026-09-20T12:00:00Z').toLocaleString())
    expect(wrapper.text()).not.toContain('characters')
  })

  it.each([
    { response_length: 312, response_shape: 'suspect', response_observed_shape: 'personal_extended' },
    { response_length: 356, response_shape: 'suspect', response_observed_shape: 'team_business_extended' },
  ])('shows valid matching latest extended states in red, ahead of a ready cache (%s)', (shape) => {
    const wrapper = show({ status: { ...status, expected_length: shape.response_length === 312 ? 292 : 332, observations: [observation(shape)] } })
    expect(colors(wrapper)[0]).toBe('red')
    expect(wrapper.text()).toBe(models.join(''))
  })

  it.each([
    { response_shape: 'unknown', response_validation_reason: 'account_type_unknown' },
    { response_shape: 'invalid', response_observed_shape: 'invalid', response_validation_reason: 'invalid_envelope' },
    { response_validation_reason: 'invalid_encoding' },
    { response_validation_reason: 'future_issued_at' },
    { response_shape: 'invalid', response_validation_reason: 'unexpected_shape' },
    { response_shape: 'expired', response_validation_reason: 'expired' },
    { response_shape: 'unknown' },
    { response_validation_reason: 'unrecognized_reason' },
    { response_length: 0, response_shape: 'missing', response_observed_shape: undefined },
  ])('uses gray for an uncertain or expired latest response instead of falling back to green cache (%s)', (shape) => {
    const wrapper = show({ status: { ...status, observations: [observation(shape)] } })
    expect(colors(wrapper)[0]).toBe('gray')
  })

  it('does not expire an observed shape using its observation time as an invented token issue time', () => {
    const wrapper = show({ status: { ...status, observations: [observation({ observed_at: '2020-01-01T00:00:00Z' })] } })
    expect(colors(wrapper)[0]).toBe('green')
  })

  it.each([292, 332])('warns in yellow during a collected cache renewal window despite an abnormal renewal (%s)', async (length) => {
    const cached = { ...status.models[0]!, source: 'collector', token_length: length, cipher_blocks: length === 292 ? 10 : 12, cache_available: true }
    const wrapper = show({ status: { ...status, expected_length: length, models: [cached], observations: [observation({
      request_source: 'collector', response_length: length === 292 ? 312 : 356, response_cipher_blocks: cached.cipher_blocks + 1,
      response_shape: 'extended', response_observed_shape: length === 292 ? 'personal_extended' : 'team_business_extended'
    })] } })
    expect(colors(wrapper)[0]).toBe('red')
    await wrapper.setProps({ now: now + 25 * 60_000 })
    expect(colors(wrapper)[0]).toBe('yellow')
    expect(wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`).attributes('title')).toContain('dotCollectorExpiring')
    await wrapper.setProps({ now: now + 30 * 60_000 - 1, loading: true })
    expect(colors(wrapper)[0]).toBe('yellow')
    expect(wrapper.get('button').attributes('style')).toContain('height: 44px')
    await wrapper.setProps({ now: now + 30 * 60_000 })
    expect(colors(wrapper)[0]).toBe('red')
  })

  it.each(['target', 'extended'])('shows an expired, idle collected cache in gray ahead of its old %s observation', (shape) => {
    const wrapper = show({ status: { ...status, models: [{ ...status.models[0]!, source: 'collector', state: 'expired',
      cache_available: false, expires_at: new Date(now - 1000).toISOString(), remaining_seconds: 0,
      collection_status: 'idle', collection_reason: 'idle' }], observations: [observation({ response_shape: shape,
      response_length: shape === 'target' ? 332 : 356, response_observed_shape: shape === 'target' ? 'team_business_target' : 'team_business_extended' })] } })
    expect(colors(wrapper)[0]).toBe('gray')
    expect(wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`).attributes('title')).toContain('dotExpiredIdle')
  })

  it('preserves passive observation and does not mistake a revoked or excluded cache for a lifecycle warning', () => {
    for (const cache of [
      { ...status.models[0]!, source: 'business' },
      { ...status.models[0]!, source: 'collector', cache_available: false },
      { ...status.models[0]!, source: 'collector', model_allowed: false },
      { ...status.models[0]!, source: 'collector', shape: 'extended', expires_at: undefined },
    ]) {
      const wrapper = show({ now: now + 26 * 60_000, status: { ...status, models: [cache], observations: [observation()] } })
      expect(colors(wrapper)[0]).toBe('green')
    }
    const wrapper = show({ now: now + 26 * 60_000, status: { ...status, enabled: false,
      models: [{ ...status.models[0]!, source: 'collector' }], observations: [observation()] } })
    expect(colors(wrapper)[0]).toBe('green')
  })

  it('keeps a paused but usable collected cache yellow, while an active expired cache follows observations', () => {
    const wrapper = show({ now: now + 26 * 60_000, status: { ...status, models: [{ ...status.models[0]!, source: 'collector',
      cache_available: true, state: 'paused', collector_paused: true, collection_status: 'paused' }] } })
    expect(colors(wrapper)[0]).toBe('yellow')
    const expired = show({ status: { ...status, models: [{ ...status.models[0]!, source: 'collector', state: 'expired',
      cache_available: false, expires_at: new Date(now - 1000).toISOString(), remaining_seconds: 0,
      collection_status: 'pending', collection_reason: 'queued' }], observations: [observation()] } })
    expect(colors(expired)[0]).toBe('green')
  })

  it.each(['business', 'collector'] as const)('uses the latest %s result and identifies its request origin', (request_source) => {
    const wrapper = show({ status: { ...status, observations: [observation({ request_source, response_length: 356, response_shape: 'extended', response_observed_shape: 'team_business_extended', response_cipher_blocks: 13 })] } })
    expect(colors(wrapper)[0]).toBe('red')
    expect(wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`).attributes('title')).toContain(`sources.${request_source}`)
  })

  it.each(['pending', 'collecting', 'backoff', 'paused', 'blocked'] as const)('does not let collection status %s replace a known shape or invent an abnormal missing result', (collection_status) => {
    const wrapper = show({ status: { ...status, models: [
      { ...status.models[0]!, cache_available: false, collection_status },
      { ...status.models[0]!, model: models[1]!, state: 'missing', cache_available: false, collection_status },
    ], observations: [observation({ response_length: 356, response_shape: 'extended', response_observed_shape: 'team_business_extended' })] } })
    expect(colors(wrapper)).toEqual(['red', 'gray'])
  })

  it.each([
    { expected_length: 0, response_length: 356, response_observed_shape: 'team_business_extended', response_cipher_blocks: 13 },
    { expected_length: 292, response_length: 356, response_observed_shape: 'team_business_extended', response_cipher_blocks: 13 },
    { expected_length: 332, response_length: 356, response_observed_shape: 'team_business_extended', response_cipher_blocks: 12 },
  ])('does not classify unknown, mismatching or invalid block counts as red (%s)', (sample) => {
    const wrapper = show({ status: { ...status, expected_length: sample.expected_length, observations: [observation({ ...sample, response_shape: 'extended' })] } })
    expect(colors(wrapper)[0]).toBe('gray')
  })

  it('honors explicit cache availability, including valid cache while collection is paused', async () => {
    const wrapper = show({ status: { ...status, models: [{ ...status.models[0]!, state: 'paused', collector_paused: true, cache_available: true }] } })
    expect(colors(wrapper)[0]).toBe('green')
    await wrapper.setProps({ status: { ...status, models: [{ ...status.models[0]!, cache_available: false }] } })
    expect(colors(wrapper)[0]).toBe('gray')
    await wrapper.setProps({ status: { ...status, models: [{ ...status.models[0]!, cache_available: true, model_allowed: false }] } })
    expect(colors(wrapper)[0]).toBe('gray')
  })

  it('uses gray when a cache expires or is disabled, and green only for a valid fallback cache', async () => {
    const wrapper = show()
    expect(colors(wrapper)[0]).toBe('green')
    await wrapper.setProps({ now: now + 31 * 60_000 })
    expect(colors(wrapper)[0]).toBe('gray')
    await wrapper.setProps({ now, status: { ...status, enabled: false } })
    expect(colors(wrapper)[0]).toBe('gray')
    await wrapper.setProps({ status: { ...status, expected_length: 0 } })
    expect(colors(wrapper)[0]).toBe('gray')
  })

  it('keeps the first load, refresh and failure in the same two-row frame without loading text', async () => {
    const wrapper = show({ status: undefined, models: [], loading: true })
    expect(colors(wrapper)).toEqual(['gray', 'gray'])
    expect(wrapper.text()).toBe(models.join(''))
    expect(wrapper.text()).not.toContain('loading')
    await wrapper.setProps({ status, models, loading: true })
    expect(colors(wrapper)[0]).toBe('green')
    expect(wrapper.get('button').attributes('style')).toContain('height: 44px')
    await wrapper.setProps({ failed: true, loading: false })
    expect(colors(wrapper)).toEqual(['gray', 'gray'])
    expect(wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`).attributes('title')).toContain('columnUnavailable')
    expect(wrapper.text()).toBe(models.join(''))
  })

  it('keeps configured order and limits the cell to five real models with an inline count on the last row', () => {
    const configured = [...models, 'gpt-5.6-terra', 'custom-4', 'custom-5', 'custom-6']
    const wrapper = show({ models: configured })
    expect(wrapper.text()).toBe(`${configured.slice(0, 5).join('')}+1`)
    expect(wrapper.get('[data-testid="codex-turn-state-more"]').attributes('title')).toContain('columnMore{"count":1}')
    expect(wrapper.findAll('[data-testid="codex-turn-state-dot"]')).toHaveLength(5)
    expect(wrapper.get('[data-testid="codex-turn-state-model-custom-5"]').text()).toContain('+1')
    expect(wrapper.get('button').attributes('style')).toContain('height: 104px')
    expect(wrapper.text()).not.toContain('custom-6')
  })

  it('includes off-list actual observations and inheritance in accessible descriptions', () => {
    const wrapper = show({ models: [], status: { ...status, models: [], enabled: false, inherited: true, owner_account_id: 45, observations: [observation({ model: 'outside-model' })] } })
    const row = wrapper.get('[data-testid="codex-turn-state-model-outside-model"]')
    expect(row.attributes('title')).toContain('columnInherited{"id":45}')
    expect(colors(wrapper)[0]).toBe('green')
    expect(wrapper.text()).toContain('outside-model')
  })

  it('keeps configured model order unchanged when later observations arrive with caching disabled', async () => {
    const wrapper = show({ status: { ...status, enabled: false, observations: [observation({ model: models[1]! })] } })
    expect(wrapper.text()).toBe(models.join(''))
    expect(colors(wrapper)).toEqual(['gray', 'green'])
    await wrapper.setProps({ status: { ...status, enabled: false, observations: [observation({ model: models[0]! }), observation({ model: models[1]! })] } })
    expect(wrapper.text()).toBe(models.join(''))
    expect(colors(wrapper)).toEqual(['green', 'green'])
  })

  it('does not create model rows or gray dots for an explicitly empty policy with no observations', () => {
    const wrapper = show({ models: [], status: { ...status, models: [] } })
    expect(wrapper.text()).toBe('—')
    expect(colors(wrapper)).toEqual([])
    expect(wrapper.findAll('[data-testid^="codex-turn-state-model-"]')).toHaveLength(0)
    expect(wrapper.findAll('[data-testid="codex-turn-state-placeholder"]')).toHaveLength(0)
  })

  it('fills unused rows with actual observations, excludes off-list cache-only records and retains the order of existing observations', async () => {
    const cached = [{ ...status.models[0]!, model: 'cache-only' }]
    const wrapper = show({ status: { ...status, models: cached, observations: [observation({ model: 'z-observed' })] } })
    expect(wrapper.text()).toBe(`${models.join('')}z-observed`)
    expect(wrapper.get('button').attributes('style')).toContain('height: 64px')
    await wrapper.setProps({ status: { ...status, models: cached, observations: [
      observation({ model: 'a-new' }), observation({ model: 'b-new' }), observation({ model: 'c-new' }), observation({ model: 'z-observed' })
    ] } })
    expect(wrapper.text()).toBe(`${models.join('')}z-observeda-newb-new+1`)
    expect(wrapper.findAll('[data-testid="codex-turn-state-dot"]')).toHaveLength(5)
    expect(wrapper.text()).not.toContain('cache-only')
    await wrapper.setProps({ loading: true })
    expect(wrapper.text()).toBe(`${models.join('')}z-observeda-newb-new+1`)
    expect(wrapper.get('button').attributes('style')).toContain('height: 104px')
  })

  it('shows a configured third model without adding any unused placeholder rows', () => {
    const wrapper = show({ models: [...models, 'gpt-5.6-terra'] })
    expect(wrapper.text()).toBe(`${models.join('')}gpt-5.6-terra`)
    expect(colors(wrapper)).toEqual(['green', 'gray', 'gray'])
    expect(wrapper.get('button').attributes('style')).toContain('height: 64px')
    expect(wrapper.find('[data-testid="codex-turn-state-more"]').exists()).toBe(false)
  })

  it.each([
    { ...account, type: 'apikey' },
    { ...account, credentials: { auth_mode: 'personalAccessToken' } },
    { ...account, credentials: { openai_auth_mode: 'agent_identity' } },
    { ...account, platform: 'anthropic' }
  ] as AccountListItem[])('shows unsupported accounts without a details action', (unsupported) => {
    const wrapper = show({ account: unsupported })
    expect(wrapper.text()).toBe('—')
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
