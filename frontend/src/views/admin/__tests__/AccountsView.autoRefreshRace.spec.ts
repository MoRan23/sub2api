import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import AccountsView from '../AccountsView.vue'

const { listAccounts, listWithEtag, getById, getBatchTodayStats, getProbeSettings, getAllProxies, getAllGroups } = vi.hoisted(() => ({
  listAccounts: vi.fn(), listWithEtag: vi.fn(), getById: vi.fn(),
  getBatchTodayStats: vi.fn(), getProbeSettings: vi.fn(), getAllProxies: vi.fn(), getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts, listWithEtag, getById,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings: getProbeSettings
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: '<div><div v-for="row in data" :key="row.id" data-test="account-row">{{ row.name }}<slot name="cell-actions" :row="row" /></div></div>'
})
const PaginationStub = defineComponent({
  props: { page: Number },
  emits: ['update:page'],
  template: '<div><span data-test="page">{{ page }}</span><button @click="$emit(\'update:page\', 1)">First page</button><button @click="$emit(\'update:page\', 2)">Next page</button></div>'
})
const FiltersStub = defineComponent({
  props: { searchQuery: String },
  emits: ['update:searchQuery'],
  template: '<button @click="$emit(\'update:searchQuery\', \'filtered\')">Filter accounts</button>'
})
const EditStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  emits: ['updated', 'close'],
  template: '<button v-if="show" @click="$emit(\'updated\', { ...account, name: \'Edited account\', updated_at: \'2026-09-19T01:00:00Z\' }); $emit(\'close\')">Save account</button>'
})

function account(id: number, name: string) {
  return {
    id, name, platform: 'openai', type: 'oauth', status: 'active', schedulable: true,
    concurrency: 2, priority: 1, group_ids: [], extra: {}, credentials: {},
    updated_at: '2026-09-19T00:00:00Z'
  }
}
function page(id = 1, name = 'First account') {
  return { items: [account(id, name)], total: 2, page: id, page_size: 20, pages: 2 }
}
function autoResult(id = 1, name = 'Stale account') {
  return { notModified: false, etag: 'stale-etag', data: page(id, name) }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(yes => { resolve = yes })
  return { promise, resolve }
}
let wrapper: VueWrapper | undefined
function mountView() {
  wrapper = mount(AccountsView, {
    attachTo: document.body,
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      DataTable: DataTableStub, Pagination: PaginationStub, AccountTableFilters: FiltersStub,
      AccountTableActions: { template: '<div><slot name="after" /></div>' },
      EditAccountModal: EditStub, AccountBulkActionsBar: true, ConfirmDialog: true,
      AccountActionMenu: true, ImportDataModal: true, ReAuthAccountModal: true,
      AccountTestModal: true, AccountStatsModal: true, ScheduledTestsPanel: true,
      SyncFromCrsModal: true, TempUnschedStatusModal: true, ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true, CreateAccountModal: true, BulkEditAccountModal: true,
      AccountDailyFixedRootsModal: true, AccountCodexTurnStateModal: true,
      PlatformTypeBadge: true, AccountCapacityCell: true, AccountStatusIndicator: true,
      AccountTodayStatsCell: true, AccountGroupsCell: true, AccountUsageCell: true,
      UpstreamBillingRateCell: true, HelpTooltip: true, Icon: true, Teleport: true
    } }
  })
  return wrapper
}
async function clickButton(label: string) {
  const button = wrapper!.findAll('button').find(candidate => candidate.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

describe('AccountsView automatic refresh request ownership', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.clear()
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats', 'usage', 'scheduler_score', 'daily_fixed_roots', 'codex_turn_state']))
    listAccounts.mockReset().mockResolvedValue(page())
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'fresh-etag', data: null })
    getById.mockReset().mockResolvedValue(account(1, 'First account'))
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getProbeSettings.mockReset().mockResolvedValue({ enabled: false })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([])
  })
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('rejects an old page response and keeps the newer automatic request in flight', async () => {
    const oldRequest = deferred<ReturnType<typeof autoResult>>()
    const currentRequest = deferred<ReturnType<typeof autoResult>>()
    listWithEtag.mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(currentRequest.promise)
    mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(listWithEtag).toHaveBeenCalledTimes(1)
    const oldSignal = listWithEtag.mock.calls[0]![3].signal as AbortSignal

    listAccounts.mockResolvedValueOnce(page(2, 'Second account'))
    await clickButton('Next page')
    expect(oldSignal.aborted).toBe(true)
    expect(wrapper!.get('[data-test="account-row"]').text()).toContain('Second account')
    await vi.advanceTimersByTimeAsync(6000)
    expect(listWithEtag).toHaveBeenCalledTimes(2)
    expect(listWithEtag.mock.calls[1]![0]).toBe(2)
    expect(listWithEtag.mock.calls[1]![3].etag).toBeNull()

    oldRequest.resolve(autoResult())
    await flushPromises()
    expect(wrapper!.get('[data-test="account-row"]').text()).toContain('Second account')
    expect(wrapper!.get('[data-test="page"]').text()).toBe('2')
    await vi.advanceTimersByTimeAsync(12000)
    expect(listWithEtag).toHaveBeenCalledTimes(2)

    currentRequest.resolve(autoResult(2, 'Second account'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(listWithEtag).toHaveBeenCalledTimes(3)
  })

  it('invalidates a refresh immediately when a filter starts its debounce', async () => {
    const pending = deferred<ReturnType<typeof autoResult>>()
    listWithEtag.mockReturnValueOnce(pending.promise)
    mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    const signal = listWithEtag.mock.calls[0]![3].signal as AbortSignal
    listAccounts.mockResolvedValueOnce(page(2, 'Filtered account'))
    await clickButton('Filter accounts')
    expect(signal.aborted).toBe(true)
    pending.resolve(autoResult())
    await flushPromises()
    expect(wrapper!.get('[data-test="account-row"]').text()).toContain('First account')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(wrapper!.get('[data-test="account-row"]').text()).toContain('Filtered account')
    expect(listAccounts.mock.calls.at(-1)![2]).toEqual(expect.objectContaining({ search: 'filtered' }))
  })

  it('does not restore an older account or its ETag after an edit on the same page', async () => {
    const pending = deferred<ReturnType<typeof autoResult>>()
    listWithEtag.mockReturnValueOnce(pending.promise)
    mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    const signal = listWithEtag.mock.calls[0]![3].signal as AbortSignal
    await clickButton('common.edit')
    await clickButton('Save account')
    expect(signal.aborted).toBe(true)
    pending.resolve(autoResult())
    await flushPromises()
    expect(wrapper!.get('[data-test="account-row"]').text()).toContain('Edited account')
    await vi.advanceTimersByTimeAsync(16000)
    expect(listWithEtag).toHaveBeenCalledTimes(2)
    expect(listWithEtag.mock.calls[1]![3].etag).toBeNull()
  })

  it('cancels an outstanding automatic refresh on unmount', async () => {
    const pending = deferred<ReturnType<typeof autoResult>>()
    listWithEtag.mockReturnValueOnce(pending.promise)
    mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    const signal = listWithEtag.mock.calls[0]![3].signal as AbortSignal
    wrapper!.unmount()
    wrapper = undefined
    expect(signal.aborted).toBe(true)
    pending.resolve(autoResult())
    await flushPromises()
  })
})
