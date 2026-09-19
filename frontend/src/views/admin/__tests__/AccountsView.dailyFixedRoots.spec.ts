import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/vue'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { nextTick } from 'vue'
import { adminAPI } from '@/api/admin'
import fixture from '@/__tests__/fixtures/oauthDailySessionPools.json'
import AccountsView from '../AccountsView.vue'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token' }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, te: () => true })
}))

const pool = fixture.items['42']
const baseAccount = {
  platform: 'openai', type: 'oauth', status: 'active', schedulable: true,
  concurrency: 1, priority: 0, group_ids: [], credentials: {}, extra: {},
  created_at: '2026-09-14T00:00:00Z', updated_at: '2026-09-14T00:00:00Z', last_used_at: '2026-09-14T00:00:00Z'
}
const accounts = [
  { ...baseAccount, id: 42, name: 'OAuth with daily roots' },
  { ...baseAccount, id: 43, name: 'OAuth without a pool' },
  { ...baseAccount, id: 44, type: 'apikey', name: 'API key account' }
]
const success = (data: unknown) => HttpResponse.json({ code: 0, data })
const poolRequests: string[] = []
let poolResponse: unknown
const turnStateRequests: string[] = []
let turnStateResponse = { items: {} as Record<string, unknown>, models: [] as string[] }
const server = setupServer(
  http.get('*/api/v1/admin/accounts', () => success({
    items: accounts, total: accounts.length, page: 1, page_size: 20, pages: 1
  })),
  http.get('*/api/v1/admin/openai/daily-session-pools', ({ request }) => {
    poolRequests.push(new URL(request.url).searchParams.get('account_ids') ?? '')
    return success(poolResponse)
  }),
  http.get('*/api/v1/admin/accounts/codex-turn-state', ({ request }) => {
    turnStateRequests.push(new URL(request.url).searchParams.get('account_ids') ?? '')
    return success(turnStateResponse)
  }),
  http.get('*/api/v1/admin/accounts/:id/codex-turn-state', ({ params }) => success(turnStateResponse.items[String(params.id)])),
  http.get('*/api/v1/admin/proxies/all', () => success([])),
  http.get('*/api/v1/admin/groups/all', () => success([])),
  http.get('*/api/v1/admin/accounts/upstream-billing-probe/settings', () => success({ enabled: false }))
)

function renderAccounts() {
  const renderErrors = vi.fn()
  const poolRequest = vi.spyOn(adminAPI.accounts, 'listDailySessionPools')
  render(AccountsView, {
    global: {
      config: { errorHandler: renderErrors },
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        HelpTooltip: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true
      }
    }
  })
  return { renderErrors, poolRequest }
}

function rootCell(accountName: string) {
  const row = screen.getByText(accountName).closest('tr')!
  const headers = screen.getAllByRole('columnheader')
  const index = headers.findIndex(header => header.textContent?.includes('admin.accounts.columns.dailyFixedRoots'))
  expect(index).toBeGreaterThanOrEqual(0)
  return within(row).getAllByRole('cell')[index]!
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats', 'usage', 'scheduler_score']))
  localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
  poolRequests.length = 0
  poolResponse = fixture
  turnStateRequests.length = 0
  turnStateResponse = { items: {}, models: [] }
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
  vi.restoreAllMocks()
})

describe('AccountsView daily fixed root HTTP contract', () => {
  it('shows the turn-state column by default, batches supported rows, and opens existing status details', async () => {
    turnStateResponse = { models: ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'], items: { '42': {
      account_id: 42, owner_account_id: 42, inherited: false, enabled: true,
      account_type: 'auto', resolved_account_type: 'team_business', expected_length: 332,
      models: [{ model: 'gpt-6-astra', state: 'ready', shape: 'target', source: 'business', token_length: 332, cipher_blocks: 12, remaining_seconds: 1800 }]
    } } }
    const { renderErrors } = renderAccounts()
    const cell = await screen.findByTestId('account-codex-turn-state-42')
    await waitFor(() => expect(within(cell).getAllByTestId('codex-turn-state-dot')[0]!.getAttribute('data-state')).toBe('green'))
    expect(screen.getByRole('columnheader', { name: 'admin.accounts.columns.codexTurnState' })).toBeTruthy()
    expect(cell.textContent).toContain('gpt-5.6-sol')
    expect(cell.textContent).toContain('gpt-5.6-terra')
    expect(within(cell).queryByTestId('codex-turn-state-more')).toBeNull()
    expect(turnStateRequests).toEqual(['42,43'])
    expect(within(screen.getByTestId('account-codex-turn-state-43')).getAllByTestId('codex-turn-state-dot').every(dot => dot.getAttribute('data-state') === 'gray')).toBe(true)
    expect(within(screen.getByTestId('account-codex-turn-state-43')).getByTestId('codex-turn-state-model-gpt-6-astra').getAttribute('title')).toContain('columnUnavailable')
    expect(screen.getByTestId('account-codex-turn-state-44').textContent).toBe('—')
    await fireEvent.click(within(cell).getByRole('button'))
    const dialog = await screen.findByRole('dialog', { name: 'admin.accounts.codexTurnState.statusTitle' })
    expect(await within(dialog).findByText('gpt-6-astra')).toBeTruthy()
    expect(renderErrors).not.toHaveBeenCalled()
  })

  it('makes no turn-state request for a saved hidden column and loads it when shown', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats', 'usage', 'scheduler_score', 'codex_turn_state']))
    renderAccounts()
    await screen.findByText(accounts[0]!.name)
    expect(turnStateRequests).toEqual([])
    expect(screen.queryByRole('columnheader', { name: 'admin.accounts.columns.codexTurnState' })).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'admin.accounts.moreActions' }))
    await fireEvent.click(screen.getByRole('button', { name: 'admin.accounts.columns.codexTurnState' }))
    await waitFor(() => expect(turnStateRequests).toEqual(['42,43']))
    expect(screen.getByRole('columnheader', { name: 'admin.accounts.columns.codexTurnState' })).toBeTruthy()
  })

  it('carries independent batch summaries into the disabled-cache column and dialog despite a legacy observation flag', async () => {
    turnStateResponse = { models: ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'], items: { '42': {
      account_id: 42, owner_account_id: 42, inherited: false, enabled: false, expected_length: 332, models: [],
      observation_enabled: false, observation_scope: 'instance', observations: [
        { model: 'gpt-6-astra', observed_at: '2026-09-20T12:00:00Z', outbound_length: 0, response_length: 332, response_shape: 'target', response_observed_shape: 'team_business_target' },
        { model: 'outside-list', observed_at: '2026-09-20T12:00:01Z', outbound_length: 0, response_length: 356, response_shape: 'suspect', response_observed_shape: 'team_business_extended' },
      ]
    } } }
    const { renderErrors } = renderAccounts()
    const cell = await screen.findByTestId('account-codex-turn-state-42')
    await waitFor(() => expect(within(cell).getAllByTestId('codex-turn-state-dot').map(dot => dot.getAttribute('data-state'))).toEqual(['green', 'gray', 'gray', 'red']))
    expect(within(cell).getByTestId('codex-turn-state-model-gpt-6-astra').getAttribute('title')).toContain('passiveOnly')
    expect(within(cell).queryByTestId('codex-turn-state-more')).toBeNull()
    expect(cell.textContent).toContain('outside-list')
    expect(cell.textContent).not.toContain('passiveOnly')
    expect(cell.textContent).not.toContain('observationEmpty')
    expect(within(cell).queryByTestId('codex-turn-state-cache-summary')).toBeNull()
    await fireEvent.click(within(cell).getByRole('button'))
    const dialog = await screen.findByRole('dialog', { name: 'admin.accounts.codexTurnState.statusTitle' })
    expect(await within(dialog).findByTestId('codex-turn-state-observation-outside-list')).toBeTruthy()
    expect(within(dialog).getByTestId('codex-turn-state-cache-section').textContent).toContain('disabled')
    expect(renderErrors).not.toHaveBeenCalled()
  })

  it('shows a compact summary and reveals the labelled roots only after opening details', async () => {
    const { renderErrors } = renderAccounts()
    await screen.findByRole('button', { name: 'admin.accounts.dailyFixedRoots.viewDetails' })
    const cell = rootCell(accounts[0]!.name)
    expect(cell.textContent).toContain(pool.business_date)
    expect(cell.textContent).toContain('admin.accounts.dailyFixedRoots.summary')
    const fullIDs = [...pool.stream_session_ids, pool.sync_session_id, pool.generation]
    for (const id of fullIDs) expect(screen.queryByText(id)).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(rootCell(accounts[1]!.name).textContent?.trim()).toBe('-')
    expect(rootCell(accounts[2]!.name).textContent?.trim()).toBe('-')

    await fireEvent.click(within(cell).getByRole('button', { name: 'admin.accounts.dailyFixedRoots.viewDetails' }))
    const dialog = await screen.findByRole('dialog', { name: 'admin.accounts.dailyFixedRoots.title' })
    expect(within(dialog).getByText(accounts[0]!.name)).toBeTruthy()
    expect(within(dialog).getByText(pool.business_date)).toBeTruthy()
    pool.stream_session_ids.forEach((id, index) => {
      const item = within(dialog).getByText(`admin.accounts.dailyFixedRoots.streamRoot ${index}`).parentElement!
      expect(within(item).getByText(id)).toBeTruthy()
    })
    const syncItem = within(dialog).getByText('admin.accounts.dailyFixedRoots.syncRoot').parentElement!
    expect(within(syncItem).getByText(pool.sync_session_id)).toBeTruthy()
    expect(within(dialog).getByText(pool.generation)).toBeTruthy()
    for (const id of fullIDs) expect(cell.textContent).not.toContain(id)

    await fireEvent.click(within(dialog).getByRole('button', { name: 'common.close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    for (const id of fullIDs) expect(screen.queryByText(id)).toBeNull()
    expect(poolRequests).toEqual(['42,43'])
    expect(renderErrors).not.toHaveBeenCalled()
  })

  it.each([
    ['disabled', { ...fixture, enabled: false }],
    ['no current pool', { ...fixture, items: {} }],
    ['legacy PascalCase response', { ...fixture, items: { '42': {
      AccountID: pool.account_id, BusinessDate: pool.business_date, Generation: pool.generation,
      StreamSessionIDs: pool.stream_session_ids, SyncSessionID: pool.sync_session_id
    } } }],
    ['missing stream roots', { ...fixture, items: { '42': { sync_session_id: pool.sync_session_id } } }],
    ['null stream roots', { ...fixture, items: { '42': { ...pool, stream_session_ids: null } } }]
  ])('keeps the account table visible when %s', async (_state, response) => {
    poolResponse = response
    const { renderErrors, poolRequest } = renderAccounts()
    await waitFor(() => expect(poolRequests).toEqual(['42,43']))
    // Follow the real HTTP response through the render cycle before checking for errors.
    await poolRequest.mock.results[0]!.value
    await nextTick()
    expect(renderErrors).not.toHaveBeenCalled()
    for (const account of accounts) expect(rootCell(account.name).textContent?.trim()).toBe('-')
  })
})
