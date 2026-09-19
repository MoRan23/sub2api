import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { AccountListItem } from '@/types'
import type { CodexTurnStateStatus } from '@/api/admin/accounts'
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

describe('AccountCodexTurnStateCell', () => {
  it('shows three model summaries and opens existing details without exposing runtime secrets', async () => {
    const wrapper = show()
    models.forEach(model => expect(wrapper.text()).toContain(model))
    expect(wrapper.text()).toContain('characters{"count":332}')
    expect(wrapper.text()).toContain('columnRemaining{"minutes":30}')
    expect(wrapper.text()).toContain('states.ready')
    expect(wrapper.text()).toContain('columnNoCache')
    expect(wrapper.text()).not.toContain('response_source')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it('uses the shared clock to expire a formerly ready state without a row timer', async () => {
    const wrapper = show()
    await wrapper.setProps({ now: now + 31 * 60_000 })
    expect(wrapper.text()).toContain('states.expired')
    expect(wrapper.text()).not.toContain('states.ready')
    expect(wrapper.text()).not.toContain('columnRemaining')
  })

  it('limits rows to three models while keeping the total visible for the details action', () => {
    const wrapper = show({ models: [...models, 'custom-4', 'custom-5'] })
    expect(wrapper.text()).not.toContain('custom-4')
    expect(wrapper.text()).toContain('columnMore{"count":2}')
  })

  it('distinguishes disabled caches, unknown subscriptions, and inherited configuration', () => {
    const disabled = show({ status: { ...status, enabled: false } })
    expect(disabled.text()).toContain('codexTurnState.disabled')
    expect(disabled.text()).not.toContain('states.ready')
    const inherited = show({ status: { ...status, inherited: true, owner_account_id: 45, expected_length: 0, resolved_account_type: '' } })
    expect(inherited.text()).toContain('columnInherited{"id":45}')
    expect(inherited.text()).toContain('columnUnknownPlan')
  })

  it.each([
    { ...account, type: 'apikey' },
    { ...account, credentials: { auth_mode: 'personalAccessToken' } },
    { ...account, credentials: { openai_auth_mode: 'agent_identity' } },
    { ...account, platform: 'anthropic' }
  ] as AccountListItem[])('shows unsupported accounts without a status request or details action', (unsupported) => {
    const wrapper = show({ account: unsupported })
    expect(wrapper.text()).toBe('—')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('does not turn an unavailable or missing response into a disabled account', () => {
    const wrapper = show({ status: undefined, failed: true })
    expect(wrapper.text()).toContain('columnUnavailable')
    expect(wrapper.text()).not.toContain('codexTurnState.disabled')
  })

  it('shows an explicitly empty model policy as observation only and retains excluded historical models', () => {
    const empty = show({ models: [], status: { ...status, models: [] } })
    expect(empty.text()).toContain('columnEmptyList')
    expect(empty.text()).not.toContain('codexTurnState.disabled')
    expect(empty.text()).not.toContain('columnUnavailable')
    const history = show({ models: [], status: { ...status, models: [{ ...status.models[0]!, model_allowed: false, state: 'model_excluded' }] } })
    expect(history.text()).toContain(models[0]!)
    expect(history.text()).toContain('states.model_excluded')
    expect(history.text()).not.toContain('columnRemaining')
  })
})
