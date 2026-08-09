import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import type {
  FingerprintObservationAPIKeySummary,
  FingerprintObservationChildrenResponse,
  FingerprintObservationEntry,
  FingerprintObservationSessionSummary,
  FingerprintObservationThreadSummary,
  FingerprintObservationUserSummary,
  FingerprintObservationsResponse,
} from '@/api/admin/fingerprintObservations'
import FingerprintObservationView from '../FingerprintObservationView.vue'

const {
  listUsers,
  listAPIKeys,
  listSessions,
  listThreads,
  listEntries,
  updateSettings,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  listUsers: vi.fn(),
  listAPIKeys: vi.fn(),
  listSessions: vi.fn(),
  listThreads: vi.fn(),
  listEntries: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    fingerprintObservations: {
      list: listUsers,
      listAPIKeys,
      listSessions,
      listThreads,
      listEntries,
    },
    settings: { updateSettings },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorCode: (error: unknown) =>
    typeof error === 'object' && error !== null && 'reason' in error
      ? String((error as { reason: unknown }).reason)
      : undefined,
  extractApiErrorMessage: () => 'request failed',
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const sessionID = '018f5c3c-6e3a-7abc-8def-1234567890ab'
const childThreadID = '018f5c3c-6e3a-7abd-8def-1234567890ac'
const forkedFromThreadID = '018f5c3c-6e3a-7abe-8def-1234567890ad'

const userSummary: FingerprintObservationUserSummary = {
  node_id: 'user-node-7',
  unattributed: false,
  user_id: 7,
  username: 'alice',
  email: 'alice@example.com',
  api_key_count: 1,
  session_count: 1,
  thread_count: 2,
  observation_count: 2,
  unattributed_observation_count: 0,
  first_observed_at: '2026-08-08T12:00:00Z',
  last_observed_at: '2026-08-08T12:01:00Z',
}

const apiKeySummary: FingerprintObservationAPIKeySummary = {
  node_id: 'api-key-node-9',
  user_id: 7,
  unattributed: false,
  api_key_id: 9,
  api_key_name: 'Codex workstation',
  session_count: 1,
  thread_count: 2,
  observation_count: 2,
  unattributed_observation_count: 0,
  first_observed_at: '2026-08-08T12:00:00Z',
  last_observed_at: '2026-08-08T12:01:00Z',
}

const sessionSummary: FingerprintObservationSessionSummary = {
  node_id: 'session-node-1',
  user_id: 7,
  api_key_id: 9,
  session_id: sessionID,
  unattributed: false,
  thread_count: 2,
  child_thread_count: 1,
  has_root_thread: true,
  has_unthreaded: false,
  unthreaded_observation_count: 0,
  observation_count: 2,
  first_observed_at: '2026-08-08T12:00:00Z',
  last_observed_at: '2026-08-08T12:01:00Z',
}

const rootThread: FingerprintObservationThreadSummary = {
  node_id: 'thread-node-root',
  session_id: sessionID,
  thread_id: sessionID,
  parent_thread_id: '',
  forked_from_thread_id: '',
  relation: 'root',
  unthreaded: false,
  observation_count: 1,
  first_observed_at: '2026-08-08T12:00:00Z',
  last_observed_at: '2026-08-08T12:00:00Z',
}

const childThread: FingerprintObservationThreadSummary = {
  node_id: 'thread-node-child',
  session_id: sessionID,
  thread_id: childThreadID,
  parent_thread_id: sessionID,
  forked_from_thread_id: forkedFromThreadID,
  relation: 'descendant',
  unthreaded: false,
  observation_count: 1,
  first_observed_at: '2026-08-08T12:01:00Z',
  last_observed_at: '2026-08-08T12:01:00Z',
}

const childObservation: FingerprintObservationEntry = {
  sequence_id: 11,
  timestamp: '2026-08-08T12:01:00Z',
  user_id: 7,
  username: 'alice',
  email: 'alice@example.com',
  api_key_id: 9,
  api_key_name: 'Codex workstation',
  account_id: 42,
  account_name: 'OpenAI OAuth',
  pinned: true,
  client_reported_installation_id: 'client-installation',
  outbound_installation_id: 'outbound-installation',
  session_id: sessionID,
  thread_id: childThreadID,
  parent_thread_id: sessionID,
  forked_from_thread_id: forkedFromThreadID,
  user_agent: 'codex_cli_rs/1.0',
  originator: 'codex_cli_rs',
  openai_beta: 'responses=experimental',
  version: '1.0.0',
  inbound_endpoint: 'POST /v1/responses/compact',
}

function topResponse(overrides: Partial<FingerprintObservationsResponse> = {}): FingerprintObservationsResponse {
  return {
    enabled: true,
    snapshot_token: 'snapshot-one',
    items: [userSummary],
    total: 1,
    page: 1,
    page_size: 20,
    pages: 1,
    ...overrides,
  }
}

function childResponse<T>(
  items: T[],
  overrides: Partial<FingerprintObservationChildrenResponse<T>> = {}
): FingerprintObservationChildrenResponse<T> {
  return {
    items,
    total: items.length,
    next_cursor: '',
    ...overrides,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const PaginationStub = defineComponent({
  name: 'Pagination',
  props: ['total', 'page', 'pageSize', 'pageSizeOptions'],
  emits: ['update:page', 'update:pageSize'],
  template: `
    <nav aria-label="test-pagination" :data-page-size-options="pageSizeOptions.join(',')">
      <span>visible-page-{{ page }}</span>
      <button type="button" aria-label="go-page-2" @click="$emit('update:page', 2)">page 2</button>
      <button type="button" aria-label="set-page-size-50" @click="$emit('update:pageSize', 50)">50 per page</button>
    </nav>
  `,
})

function mountView() {
  return mount(FingerprintObservationView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
        },
        Pagination: PaginationStub,
        AutoRefreshButton: {
          props: ['enabled', 'paused', 'intervalSeconds', 'countdown', 'intervals'],
          emits: ['update:enabled', 'update:interval'],
          template:
            '<button type="button" aria-label="auto-refresh" :data-paused="paused ? \'true\' : \'false\'">{{ paused ? \'common.autoRefresh.paused\' : \'auto\' }}</button>',
        },
        Icon: { template: '<span aria-hidden="true"></span>' },
      },
    },
  })
}

async function expandToChildEntry(wrapper: ReturnType<typeof mountView>) {
  await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
  await flushPromises()
  await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
  await flushPromises()
  await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0-session-0"]').trigger('click')
  await flushPromises()
  await wrapper
    .get('button[aria-controls="fingerprint-user-0-api-key-0-session-0-thread-1"]')
    .trigger('click')
  await flushPromises()
}

describe('FingerprintObservationView', () => {
  beforeEach(() => {
    localStorage.clear()
    for (const mock of [
      listUsers,
      listAPIKeys,
      listSessions,
      listThreads,
      listEntries,
      updateSettings,
      showError,
      showSuccess,
    ]) {
      mock.mockReset()
    }
    listUsers.mockResolvedValue(topResponse())
    listAPIKeys.mockResolvedValue(childResponse([apiKeySummary]))
    listSessions.mockResolvedValue(childResponse([sessionSummary]))
    listThreads.mockResolvedValue(childResponse([rootThread, childThread]))
    listEntries.mockResolvedValue(childResponse([childObservation]))
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('loads each hierarchy level only when expanded and reuses the snapshot cache', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listUsers).toHaveBeenCalledWith(
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('alice')
    expect(wrapper.text()).not.toContain('Codex workstation')
    expect(listAPIKeys).not.toHaveBeenCalled()

    await expandToChildEntry(wrapper)

    expect(listAPIKeys).toHaveBeenCalledWith(
      { snapshot_token: 'snapshot-one', parent_node_id: 'user-node-7', limit: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(listSessions).toHaveBeenCalledWith(
      { snapshot_token: 'snapshot-one', parent_node_id: 'api-key-node-9', limit: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(listThreads).toHaveBeenCalledWith(
      { snapshot_token: 'snapshot-one', parent_node_id: 'session-node-1', limit: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(listEntries).toHaveBeenCalledWith(
      { snapshot_token: 'snapshot-one', parent_node_id: 'thread-node-child', limit: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain(forkedFromThreadID)
    expect(wrapper.text()).toContain('OpenAI OAuth')
    expect(wrapper.text()).toContain('outbound-installation')
    expect(wrapper.text()).toContain('POST /v1/responses/compact')

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()
    expect(listAPIKeys).toHaveBeenCalledTimes(1)

    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
    await flushPromises()
    expect(listSessions).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  it('loads child cursors on demand and retries without discarding already loaded items', async () => {
    const secondAPIKey = {
      ...apiKeySummary,
      node_id: 'api-key-node-10',
      api_key_id: 10,
      api_key_name: 'Second workstation',
    }
    listAPIKeys
      .mockResolvedValueOnce(childResponse([apiKeySummary], { total: 2, next_cursor: 'keys-cursor-2' }))
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce(childResponse([secondAPIKey], { total: 2 }))

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Codex workstation')
    const loadMore = wrapper
      .findAll('button')
      .find((button) => button.text() === 'admin.fingerprintObservation.loadMore')!
    await loadMore.trigger('click')
    await flushPromises()

    expect(listAPIKeys).toHaveBeenNthCalledWith(
      2,
      {
        snapshot_token: 'snapshot-one',
        parent_node_id: 'user-node-7',
        cursor: 'keys-cursor-2',
        limit: 20,
      },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('Codex workstation')
    expect(wrapper.text()).toContain('request failed')

    const retry = wrapper
      .findAll('button')
      .find((button) => button.text() === 'admin.fingerprintObservation.retry')!
    await retry.trigger('click')
    await flushPromises()

    expect(listAPIKeys).toHaveBeenNthCalledWith(
      3,
      {
        snapshot_token: 'snapshot-one',
        parent_node_id: 'user-node-7',
        cursor: 'keys-cursor-2',
        limit: 20,
      },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('Codex workstation')
    expect(wrapper.text()).toContain('Second workstation')

    wrapper.unmount()
  })

  it('aborts an in-flight child request on collapse and ignores its late response', async () => {
    const staleKeys = deferred<FingerprintObservationChildrenResponse<FingerprintObservationAPIKeySummary>>()
    listAPIKeys.mockReturnValueOnce(staleKeys.promise).mockResolvedValueOnce(childResponse([apiKeySummary]))
    const wrapper = mountView()
    await flushPromises()

    const userButton = wrapper.get('button[aria-controls="fingerprint-user-0"]')
    await userButton.trigger('click')
    const firstSignal = listAPIKeys.mock.calls[0][1].signal as AbortSignal
    await userButton.trigger('click')
    expect(firstSignal.aborted).toBe(true)

    staleKeys.resolve(childResponse([{ ...apiKeySummary, api_key_name: 'stale key' }]))
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale key')

    await userButton.trigger('click')
    await flushPromises()
    expect(listAPIKeys).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('Codex workstation')

    wrapper.unmount()
  })

  it('labels synthetic empty session and thread branches as unattributed', async () => {
    const unattributedSession: FingerprintObservationSessionSummary = {
      ...sessionSummary,
      node_id: 'session-node-unattributed',
      session_id: '',
      unattributed: true,
      thread_count: 0,
      child_thread_count: 0,
      has_root_thread: false,
      has_unthreaded: true,
      unthreaded_observation_count: 1,
      observation_count: 1,
    }
    const unthreaded: FingerprintObservationThreadSummary = {
      ...rootThread,
      node_id: 'thread-node-unthreaded',
      session_id: '',
      thread_id: '',
      relation: 'unthreaded',
      unthreaded: true,
    }
    listSessions.mockResolvedValueOnce(childResponse([unattributedSession]))
    listThreads.mockResolvedValueOnce(childResponse([unthreaded]))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()
    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.fingerprintObservation.unattributedSession')
    expect(wrapper.text()).not.toContain('admin.fingerprintObservation.rootSession')

    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0-session-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.fingerprintObservation.unthreaded')

    wrapper.unmount()
  })

  it('pages by users with a stable snapshot token and clears lower-level caches', async () => {
    const pageOneUsers = Array.from({ length: 20 }, (_, index) => ({
      ...userSummary,
      node_id: `page-one-user-${index + 1}`,
      user_id: index + 1,
      username: `page-one-${index + 1}`,
    }))
    const pageTwoUsers = Array.from({ length: 5 }, (_, index) => ({
      ...userSummary,
      node_id: `page-two-user-${index + 21}`,
      user_id: index + 21,
      username: `page-two-${index + 21}`,
    }))
    listUsers
      .mockResolvedValueOnce(topResponse({ items: pageOneUsers, total: 25, pages: 2 }))
      .mockResolvedValueOnce(topResponse({ items: pageTwoUsers, total: 25, page: 2, pages: 2 }))

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[aria-label="test-pagination"]').attributes('data-page-size-options')).toBe('20,50,100')

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()
    expect(listAPIKeys).toHaveBeenCalledTimes(1)

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    await flushPromises()

    expect(listUsers).toHaveBeenNthCalledWith(
      2,
      { page: 2, page_size: 20, snapshot_token: 'snapshot-one' },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('page-two-21')
    expect(wrapper.text()).not.toContain('Codex workstation')
    expect(wrapper.text()).toContain('visible-page-2')

    wrapper.unmount()
  })

  it('manual refresh supersedes a stale page request and creates a new snapshot', async () => {
    const stalePage = deferred<FingerprintObservationsResponse>()
    listUsers
      .mockResolvedValueOnce(topResponse({ total: 25, pages: 2 }))
      .mockReturnValueOnce(stalePage.promise)
      .mockResolvedValueOnce(
        topResponse({
          snapshot_token: 'snapshot-two',
          items: [{ ...userSummary, node_id: 'fresh-user', username: 'fresh winner' }],
        })
      )
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    const staleSignal = listUsers.mock.calls[1][1].signal as AbortSignal
    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')
    await flushPromises()

    expect(staleSignal.aborted).toBe(true)
    expect(listUsers).toHaveBeenNthCalledWith(
      3,
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('fresh winner')

    stalePage.resolve(topResponse({ page: 2, items: [{ ...userSummary, username: 'stale loser' }] }))
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale loser')

    wrapper.unmount()
  })

  it('keeps disclosures disabled during a root refresh and resumes auto-refresh cleanly', async () => {
    vi.useFakeTimers()
    localStorage.setItem(
      'admin-fingerprint-observation-auto-refresh',
      JSON.stringify({ enabled: true, interval_seconds: 5 })
    )
    const pendingRefresh = deferred<FingerprintObservationsResponse>()
    listUsers
      .mockResolvedValueOnce(topResponse())
      .mockReturnValueOnce(pendingRefresh.promise)
      .mockResolvedValueOnce(
        topResponse({
          snapshot_token: 'snapshot-after-auto-refresh',
          items: [{ ...userSummary, node_id: 'auto-user', username: 'auto refreshed user' }],
        })
      )
    const wrapper = mountView()
    await flushPromises()

    const staleUserButton = wrapper.get('button[aria-controls="fingerprint-user-0"]')
    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')

    expect(staleUserButton.attributes('disabled')).toBeDefined()
    expect(wrapper.get('button[aria-label="auto-refresh"]').attributes('data-paused')).toBe('true')
    await staleUserButton.trigger('click')
    expect(staleUserButton.attributes('aria-expanded')).toBe('false')
    expect(listAPIKeys).not.toHaveBeenCalled()

    pendingRefresh.resolve(
      topResponse({
        snapshot_token: 'snapshot-refreshed',
        items: [{ ...userSummary, node_id: 'refreshed-user', username: 'refreshed user' }],
      })
    )
    await flushPromises()

    const refreshedUserButton = wrapper.get('button[aria-controls="fingerprint-user-0"]')
    expect(wrapper.text()).toContain('refreshed user')
    expect(refreshedUserButton.attributes('aria-expanded')).toBe('false')
    expect(refreshedUserButton.attributes('disabled')).toBeUndefined()
    expect(wrapper.get('button[aria-label="auto-refresh"]').attributes('data-paused')).toBe('false')

    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(listUsers).toHaveBeenCalledTimes(3)

    wrapper.unmount()
  })

  it('recovers an expired paged snapshot with a tokenless page-one request', async () => {
    listUsers
      .mockResolvedValueOnce(topResponse({ total: 25, pages: 2 }))
      .mockRejectedValueOnce({ reason: 'fingerprint_snapshot_expired' })
      .mockResolvedValueOnce(
        topResponse({
          snapshot_token: 'snapshot-recovered',
          items: [{ ...userSummary, node_id: 'recovered-user', username: 'recovered user' }],
        })
      )
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    await flushPromises()

    expect(listUsers).toHaveBeenNthCalledWith(
      2,
      { page: 2, page_size: 20, snapshot_token: 'snapshot-one' },
      { signal: expect.any(AbortSignal) }
    )
    expect(listUsers).toHaveBeenNthCalledWith(
      3,
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('recovered user')
    expect(showError).toHaveBeenCalledOnce()
    expect(showError).toHaveBeenCalledWith('admin.fingerprintObservation.snapshotExpired')

    wrapper.unmount()
  })

  it('aborts sibling child reads and prevents stale refill when a child snapshot expires', async () => {
    const secondAPIKey: FingerprintObservationAPIKeySummary = {
      ...apiKeySummary,
      node_id: 'api-key-node-10',
      api_key_id: 10,
      api_key_name: 'Second workstation',
    }
    const staleSessions = deferred<
      FingerprintObservationChildrenResponse<FingerprintObservationSessionSummary>
    >()
    listUsers
      .mockResolvedValueOnce(topResponse())
      .mockResolvedValueOnce(
        topResponse({
          snapshot_token: 'snapshot-recovered',
          items: [{ ...userSummary, node_id: 'recovered-user', username: 'recovered user' }],
        })
      )
    listAPIKeys.mockResolvedValueOnce(childResponse([apiKeySummary, secondAPIKey]))
    listSessions
      .mockReturnValueOnce(staleSessions.promise)
      .mockRejectedValueOnce({ reason: 'fingerprint_snapshot_expired' })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()
    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
    const staleSignal = listSessions.mock.calls[0][1].signal as AbortSignal
    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-1"]').trigger('click')
    await flushPromises()

    expect(staleSignal.aborted).toBe(true)
    expect(listUsers).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('recovered user')
    expect(showError).toHaveBeenCalledOnce()

    staleSessions.resolve(
      childResponse([
        {
          ...sessionSummary,
          node_id: 'stale-session-node',
          session_id: 'stale-session-must-not-render',
        },
      ])
    )
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale-session-must-not-render')

    wrapper.unmount()
  })

  it('starts a fresh page-one snapshot when the page size changes', async () => {
    listUsers.mockResolvedValueOnce(topResponse()).mockResolvedValueOnce(topResponse({ page_size: 50 }))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="set-page-size-50"]').trigger('click')
    await flushPromises()

    expect(listUsers).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 50 },
      { signal: expect.any(AbortSignal) }
    )

    wrapper.unmount()
  })

  it('restores an allowed persisted page size on initial load', async () => {
    localStorage.setItem('table-page-size', '50')
    listUsers.mockResolvedValueOnce(topResponse({ page_size: 50 }))
    const wrapper = mountView()
    await flushPromises()

    expect(listUsers).toHaveBeenCalledWith(
      { page: 1, page_size: 50 },
      { signal: expect.any(AbortSignal) }
    )

    wrapper.unmount()
  })

  it('falls back to 20 when the persisted page size is not allowed on this page', async () => {
    localStorage.setItem('table-page-size', '10')
    const wrapper = mountView()
    await flushPromises()

    expect(listUsers).toHaveBeenCalledWith(
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )

    wrapper.unmount()
  })

  it('pauses auto-refresh while a node is expanded and resumes after collapse', async () => {
    vi.useFakeTimers()
    localStorage.setItem(
      'admin-fingerprint-observation-auto-refresh',
      JSON.stringify({ enabled: true, interval_seconds: 5 })
    )
    const wrapper = mountView()
    await flushPromises()

    const userButton = wrapper.get('button[aria-controls="fingerprint-user-0"]')
    await userButton.trigger('click')
    await flushPromises()
    expect(wrapper.get('button[aria-label="auto-refresh"]').attributes('data-paused')).toBe('true')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(listUsers).toHaveBeenCalledTimes(1)

    await userButton.trigger('click')
    expect(wrapper.get('button[aria-label="auto-refresh"]').attributes('data-paused')).toBe('false')
    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(listUsers).toHaveBeenCalledTimes(2)
    expect(listUsers).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 20 },
      { signal: expect.any(AbortSignal) }
    )

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(listUsers).toHaveBeenCalledTimes(2)
  })

  it('clears all visible and cached data only after disabling succeeds', async () => {
    updateSettings.mockResolvedValue({ installation_observation_enabled: false })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Codex workstation')

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(updateSettings).toHaveBeenCalledWith({ installation_observation_enabled: false })
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.text()).not.toContain('alice')
    expect(wrapper.find('[aria-label="test-pagination"]').exists()).toBe(false)
    expect(showSuccess).toHaveBeenCalledOnce()

    wrapper.unmount()
  })

  it('keeps the current snapshot and disclosure state when disabling fails', async () => {
    updateSettings.mockRejectedValue(new Error('write failed'))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await flushPromises()

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.text()).toContain('alice')
    expect(wrapper.text()).toContain('Codex workstation')
    expect(showError).toHaveBeenCalledWith('request failed')

    wrapper.unmount()
  })
})
