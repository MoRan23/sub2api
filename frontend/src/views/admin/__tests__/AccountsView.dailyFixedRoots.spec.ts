import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/vue'
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
  useI18n: () => ({ t: (key: string) => key })
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
const server = setupServer(
  http.get('*/api/v1/admin/accounts', () => success({
    items: accounts, total: accounts.length, page: 1, page_size: 20, pages: 1
  })),
  http.get('*/api/v1/admin/openai/daily-session-pools', ({ request }) => {
    poolRequests.push(new URL(request.url).searchParams.get('account_ids') ?? '')
    return success(poolResponse)
  }),
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
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
  vi.restoreAllMocks()
})

describe('AccountsView daily fixed root HTTP contract', () => {
  it('renders the three stream roots, sync root, date and generation in the real account table', async () => {
    const { renderErrors } = renderAccounts()
    await screen.findByText(pool.sync_session_id)
    const cell = rootCell(accounts[0]!.name)
    for (const id of pool.stream_session_ids) expect(cell.textContent).toContain(id)
    expect(cell.textContent).toContain(pool.business_date)
    expect(cell.textContent).toContain(pool.generation)
    expect(rootCell(accounts[1]!.name).textContent?.trim()).toBe('-')
    expect(rootCell(accounts[2]!.name).textContent?.trim()).toBe('-')
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
