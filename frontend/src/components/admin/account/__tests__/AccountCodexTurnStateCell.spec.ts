import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateObservation, CodexTurnStateStatus } from '@/api/admin/accounts'
import AccountCodexTurnStateCell from '../AccountCodexTurnStateCell.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : ''), te: () => true
}) }))
const now = Date.parse('2026-09-20T12:00:00Z')
const models = ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra']
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
    expect(wrapper.get('button').classes()).toContain('h-16')
    const rows = wrapper.findAll('[data-testid^="codex-turn-state-model-"]')
    expect(rows).toHaveLength(3)
    expect(rows.every(row => row.classes().includes('h-5'))).toBe(true)
    expect(colors(wrapper)).toEqual(['green', 'gray', 'gray'])
    expect(rows[0]!.attributes('title')).toContain('dotCachedTarget')
    expect(rows[0]!.attributes('aria-label')).toContain(models[0])
    await rows[0]!.trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it.each([
    { response_length: 292, response_observed_shape: 'personal_target' },
    { response_length: 332, response_observed_shape: 'team_business_target' },
  ])('shows a green latest target response with cache disabled (%s)', (shape) => {
    const wrapper = show({ status: { ...status, enabled: false, observations: [observation(shape)] } })
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
    { response_shape: 'invalid', response_observed_shape: 'invalid', response_validation_reason: 'invalid_envelope' },
    { response_validation_reason: 'invalid_encoding' },
    { response_validation_reason: 'future_issued_at' },
  ])('shows explicit latest extended or invalid states in red, ahead of a ready cache (%s)', (shape) => {
    const wrapper = show({ status: { ...status, observations: [observation(shape)] } })
    expect(colors(wrapper)[0]).toBe('red')
    expect(wrapper.text()).toBe(models.join(''))
  })

  it.each([
    { response_shape: 'unknown', response_validation_reason: 'account_type_unknown' },
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

  it('keeps the first load, refresh and failure in the same three-row frame without loading text', async () => {
    const wrapper = show({ status: undefined, models: [], loading: true })
    expect(colors(wrapper)).toEqual(['gray', 'gray', 'gray'])
    expect(wrapper.text()).toBe(models.join(''))
    expect(wrapper.text()).not.toContain('loading')
    await wrapper.setProps({ status, models, loading: true })
    expect(colors(wrapper)[0]).toBe('green')
    expect(wrapper.get('button').classes()).toContain('h-16')
    await wrapper.setProps({ failed: true, loading: false })
    expect(colors(wrapper)).toEqual(['gray', 'gray', 'gray'])
    expect(wrapper.get(`[data-testid="codex-turn-state-model-${models[0]}"]`).attributes('title')).toContain('columnUnavailable')
    expect(wrapper.text()).toBe(models.join(''))
  })

  it('keeps all exact model IDs accessible while limiting the cell to three rows with an inline count', () => {
    const wrapper = show({ models: [...models, 'custom-4', 'custom-5'] })
    expect(wrapper.text()).toBe(`${models.join('')}+2`)
    expect(wrapper.get('[data-testid="codex-turn-state-more"]').attributes('title')).toContain('columnMore{"count":2}')
    expect(wrapper.findAll('[data-testid="codex-turn-state-dot"]')).toHaveLength(3)
  })

  it('includes off-list actual observations and inheritance in accessible descriptions', () => {
    const wrapper = show({ models: [], status: { ...status, models: [], enabled: false, inherited: true, owner_account_id: 45, observations: [observation({ model: 'outside-model' })] } })
    const row = wrapper.get('[data-testid="codex-turn-state-model-outside-model"]')
    expect(row.attributes('title')).toContain('columnInherited{"id":45}')
    expect(colors(wrapper)[0]).toBe('green')
    expect(wrapper.text()).toContain('outside-model')
  })

  it('keeps configured model order unchanged when later observations arrive with caching disabled', async () => {
    const wrapper = show({ status: { ...status, enabled: false, observations: [observation({ model: models[2]! })] } })
    expect(wrapper.text()).toBe(models.join(''))
    expect(colors(wrapper)).toEqual(['gray', 'gray', 'green'])
    await wrapper.setProps({ status: { ...status, enabled: false, observations: [observation({ model: models[0]! }), observation({ model: models[2]! })] } })
    expect(wrapper.text()).toBe(models.join(''))
    expect(colors(wrapper)).toEqual(['green', 'gray', 'green'])
  })

  it('keeps three gray placeholder rows for an explicitly empty policy without inventing model IDs', () => {
    const wrapper = show({ models: [], status: { ...status, models: [] } })
    expect(wrapper.text()).toBe('———')
    expect(colors(wrapper)).toEqual(['gray', 'gray', 'gray'])
    expect(wrapper.findAll('[data-testid="codex-turn-state-placeholder"]')).toHaveLength(3)
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
