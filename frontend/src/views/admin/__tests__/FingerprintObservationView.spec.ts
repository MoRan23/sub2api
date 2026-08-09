import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import type {
  FingerprintObservationEntry,
  FingerprintObservationsResponse,
  FingerprintObservationSessionNode,
} from '@/api/admin/fingerprintObservations'
import FingerprintObservationView from '../FingerprintObservationView.vue'

const { listObservations, updateSettings, showError, showSuccess } = vi.hoisted(() => ({
  listObservations: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    fingerprintObservations: { list: listObservations },
    settings: { updateSettings },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/utils/apiError', () => ({
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

const rootObservation: FingerprintObservationEntry = {
  sequence_id: 10,
  timestamp: '2026-08-08T12:00:00Z',
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
  thread_id: sessionID,
  parent_thread_id: '',
  forked_from_thread_id: '',
  user_agent: 'codex_cli_rs/1.0',
  originator: 'codex_cli_rs',
  openai_beta: 'responses=experimental',
  version: '1.0.0',
  inbound_endpoint: 'POST /v1/responses',
}

const childObservation: FingerprintObservationEntry = {
  ...rootObservation,
  sequence_id: 11,
  timestamp: '2026-08-08T12:01:00Z',
  thread_id: childThreadID,
  parent_thread_id: sessionID,
  forked_from_thread_id: forkedFromThreadID,
  inbound_endpoint: 'POST /v1/responses/compact',
}

const sessionNode: FingerprintObservationSessionNode = {
  user_id: 7,
  username: 'alice',
  email: 'alice@example.com',
  api_key_id: 9,
  api_key_name: 'Codex workstation',
  session_id: sessionID,
  first_observed_at: rootObservation.timestamp,
  last_observed_at: childObservation.timestamp,
  observation_count: 2,
  root_thread: {
    thread_id: sessionID,
    parent_thread_id: '',
    forked_from_thread_id: '',
    relation: 'root',
    first_observed_at: rootObservation.timestamp,
    last_observed_at: rootObservation.timestamp,
    observation_count: 1,
    observations: [rootObservation],
  },
  child_threads: [
    {
      thread_id: childThreadID,
      parent_thread_id: sessionID,
      forked_from_thread_id: forkedFromThreadID,
      relation: 'descendant',
      first_observed_at: childObservation.timestamp,
      last_observed_at: childObservation.timestamp,
      observation_count: 1,
      observations: [childObservation],
    },
  ],
  unthreaded_observations: [],
}

function response(
  overrides: Partial<FingerprintObservationsResponse> = {}
): FingerprintObservationsResponse {
  return {
    enabled: true,
    items: [sessionNode],
    total: 2,
    page: 1,
    page_size: 20,
    pages: 2,
    snapshot_seq: 77,
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
  props: ['total', 'page', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: `
    <nav aria-label="test-pagination">
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
          template:
            '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
        },
        Pagination: PaginationStub,
        AutoRefreshButton: {
          props: ['enabled', 'intervalSeconds', 'countdown', 'intervals'],
          emits: ['update:enabled', 'update:interval'],
          template:
            '<button type="button" aria-label="auto-refresh" @click="$emit(\'update:enabled\', true)">auto</button>',
        },
        Icon: { template: '<span aria-hidden="true"></span>' },
      },
    },
  })
}

describe('FingerprintObservationView', () => {
  beforeEach(() => {
    localStorage.clear()
    listObservations.mockReset()
    updateSettings.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reveals user, API key, thread lineage, and concrete wire observations through the tree', async () => {
    listObservations.mockResolvedValue(response())
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('alice')
    expect(wrapper.find('[title="alice@example.com"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('Codex workstation')
    expect(wrapper.find(`[title="${sessionID}"]`).exists()).toBe(false)

    const userButton = wrapper.get('button[aria-controls="fingerprint-user-0"]')
    expect(userButton.attributes('aria-expanded')).toBe('false')
    await userButton.trigger('click')

    expect(userButton.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('Codex workstation')
    expect(wrapper.find(`[title="${sessionID}"]`).exists()).toBe(false)

    const apiKeyButton = wrapper.get(
      'button[aria-controls="fingerprint-user-0-api-key-0"]'
    )
    expect(apiKeyButton.attributes('aria-expanded')).toBe('false')
    await apiKeyButton.trigger('click')

    expect(apiKeyButton.attributes('aria-expanded')).toBe('true')
    expect(wrapper.find(`[title="${sessionID}"]`).exists()).toBe(true)
    expect(wrapper.text()).not.toContain(childThreadID)

    const sessionButton = wrapper.get(
      'button[aria-controls="fingerprint-user-0-api-key-0-session-0"]'
    )
    expect(sessionButton.attributes('aria-expanded')).toBe('false')
    await sessionButton.trigger('click')

    expect(sessionButton.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain(childThreadID)
    expect(wrapper.find(`[title="${forkedFromThreadID}"]`).exists()).toBe(true)
    expect(wrapper.text()).not.toContain('POST /v1/responses/compact')

    const childThreadButton = wrapper.get(
      'button[aria-controls="fingerprint-user-0-api-key-0-session-0-thread-1"]'
    )
    await childThreadButton.trigger('click')

    expect(childThreadButton.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('OpenAI OAuth')
    expect(wrapper.text()).toContain('outbound-installation')
    expect(wrapper.text()).toContain('codex_cli_rs/1.0')
    expect(wrapper.text()).toContain('POST /v1/responses/compact')

    wrapper.unmount()
  })

  it('groups root sessions by user and then by API key with independent disclosure controls', async () => {
    const sameKeySession = {
      ...sessionNode,
      session_id: '018f5c3c-6e3a-7abf-8def-1234567890b1',
    }
    const secondKeySession = {
      ...sessionNode,
      api_key_id: 10,
      api_key_name: 'Second workstation',
      session_id: '018f5c3c-6e3a-7abf-8def-1234567890b2',
    }
    const secondUserSession = {
      ...sessionNode,
      user_id: 8,
      username: 'bob',
      email: 'bob@example.com',
      api_key_id: 11,
      api_key_name: 'Bob workstation',
      session_id: '018f5c3c-6e3a-7abf-8def-1234567890b3',
    }
    listObservations.mockResolvedValue(
      response({
        items: [sessionNode, sameKeySession, secondKeySession, secondUserSession],
        total: 4,
        pages: 1,
      })
    )
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('button[aria-controls="fingerprint-user-0"]')).toHaveLength(1)
    expect(wrapper.findAll('button[aria-controls="fingerprint-user-1"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('alice')
    expect(wrapper.text()).toContain('bob')
    expect(wrapper.text()).not.toContain('Codex workstation')

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    expect(wrapper.text().match(/Codex workstation/g)).toHaveLength(1)
    expect(wrapper.text()).toContain('Second workstation')
    expect(wrapper.findAll('button[aria-controls="fingerprint-user-0-api-key-0"]')).toHaveLength(1)
    expect(wrapper.findAll('button[aria-controls="fingerprint-user-0-api-key-1"]')).toHaveLength(1)

    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
    expect(wrapper.find(`[title="${sessionID}"]`).exists()).toBe(true)
    expect(wrapper.find(`[title="${sameKeySession.session_id}"]`).exists()).toBe(true)
    expect(wrapper.find(`[title="${secondKeySession.session_id}"]`).exists()).toBe(false)

    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-1"]').trigger('click')
    expect(wrapper.find(`[title="${secondKeySession.session_id}"]`).exists()).toBe(true)

    const controlIDs = wrapper
      .findAll('button[aria-controls]')
      .map((button) => button.attributes('aria-controls'))
    expect(controlIDs.every((id) => !id.includes('undefined'))).toBe(true)
    expect(new Set(controlIDs).size).toBe(controlIDs.length)

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    expect(wrapper.text()).not.toContain('Codex workstation')
    expect(wrapper.find(`[title="${sessionID}"]`).exists()).toBe(false)

    wrapper.unmount()
  })

  it('pins later pages to page-one snapshot and manual refresh starts a new page-one snapshot', async () => {
    const pageTwoSession = { ...sessionNode, username: 'page-two-user' }
    const refreshedSession = { ...sessionNode, username: 'fresh-page-one-user' }
    listObservations
      .mockResolvedValueOnce(response())
      .mockResolvedValueOnce(response({ items: [pageTwoSession], page: 2 }))
      .mockResolvedValueOnce(
        response({ items: [refreshedSession], page: 1, snapshot_seq: 91 })
      )

    const wrapper = mountView()
    await flushPromises()

    expect(listObservations).toHaveBeenNthCalledWith(
      1,
      { page: 1, page_size: 20, snapshot_seq: 0 },
      { signal: expect.any(AbortSignal) }
    )

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    await flushPromises()
    expect(listObservations).toHaveBeenNthCalledWith(
      2,
      { page: 2, page_size: 20, snapshot_seq: 77 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('page-two-user')
    expect(wrapper.text()).toContain('visible-page-2')

    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')
    await flushPromises()
    expect(listObservations).toHaveBeenNthCalledWith(
      3,
      { page: 1, page_size: 20, snapshot_seq: 0 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.text()).toContain('fresh-page-one-user')
    expect(wrapper.text()).toContain('visible-page-1')

    wrapper.unmount()
  })

  it('starts a new page-one snapshot when page size changes', async () => {
    listObservations
      .mockResolvedValueOnce(response())
      .mockResolvedValueOnce(response({ page_size: 50, pages: 1 }))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="set-page-size-50"]').trigger('click')
    await flushPromises()

    expect(listObservations).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 50, snapshot_seq: 0 },
      { signal: expect.any(AbortSignal) }
    )

    wrapper.unmount()
  })

  it('auto-refreshes page one with a new snapshot and pauses on later pages', async () => {
    vi.useFakeTimers()
    localStorage.setItem(
      'admin-fingerprint-observation-auto-refresh',
      JSON.stringify({ enabled: true, interval_seconds: 5 })
    )
    listObservations.mockResolvedValue(response())
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(listObservations).toHaveBeenCalledTimes(2)
    expect(listObservations).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 20, snapshot_seq: 0 },
      { signal: expect.any(AbortSignal) }
    )

    listObservations.mockResolvedValueOnce(response({ page: 2 }))
    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('visible-page-2')

    await vi.advanceTimersByTimeAsync(10_000)
    await flushPromises()
    expect(listObservations).toHaveBeenCalledTimes(3)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(listObservations).toHaveBeenCalledTimes(3)
  })

  it('aborts and ignores a stale page response when manual refresh wins', async () => {
    const stalePage = deferred<FingerprintObservationsResponse>()
    const freshSession = { ...sessionNode, username: 'manual-refresh-winner' }
    listObservations
      .mockResolvedValueOnce(response())
      .mockReturnValueOnce(stalePage.promise)
      .mockResolvedValueOnce(response({ items: [freshSession], snapshot_seq: 88 }))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    const staleSignal = listObservations.mock.calls[1][1].signal as AbortSignal
    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')
    await flushPromises()

    expect(staleSignal.aborted).toBe(true)
    expect(wrapper.text()).toContain('manual-refresh-winner')

    stalePage.resolve(response({ items: [{ ...sessionNode, username: 'stale-page' }], page: 2 }))
    await flushPromises()
    expect(wrapper.text()).toContain('manual-refresh-winner')
    expect(wrapper.text()).not.toContain('stale-page')
    expect(wrapper.text()).toContain('visible-page-1')

    wrapper.unmount()
  })

  it('clears visible data immediately on disable and stale reads cannot repopulate it', async () => {
    const stalePage = deferred<FingerprintObservationsResponse>()
    listObservations
      .mockResolvedValueOnce(response())
      .mockReturnValueOnce(stalePage.promise)
    updateSettings.mockResolvedValue({ installation_observation_enabled: false })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="go-page-2"]').trigger('click')
    const staleSignal = listObservations.mock.calls[1][1].signal as AbortSignal
    await wrapper.get('[role="switch"]').trigger('click')

    expect(wrapper.text()).not.toContain(sessionID)
    expect(wrapper.find('[aria-label="test-pagination"]').exists()).toBe(false)
    expect(staleSignal.aborted).toBe(true)

    stalePage.resolve(response({ items: [{ ...sessionNode, username: 'stale-after-disable' }], page: 2 }))
    await flushPromises()

    expect(updateSettings).toHaveBeenCalledWith({ installation_observation_enabled: false })
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.text()).not.toContain('stale-after-disable')
    expect(showSuccess).toHaveBeenCalledOnce()

    wrapper.unmount()
  })

  it('restores the visible snapshot when disabling fails', async () => {
    listObservations.mockResolvedValue(response())
    updateSettings.mockRejectedValue(new Error('write failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-controls="fingerprint-user-0"]').trigger('click')
    await wrapper.get('button[aria-controls="fingerprint-user-0-api-key-0"]').trigger('click')
    await wrapper
      .get('button[aria-controls="fingerprint-user-0-api-key-0-session-0"]')
      .trigger('click')
    expect(wrapper.text()).toContain(childThreadID)

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(
      wrapper.get('button[aria-controls="fingerprint-user-0"]').attributes('aria-expanded')
    ).toBe('true')
    expect(
      wrapper
        .get('button[aria-controls="fingerprint-user-0-api-key-0"]')
        .attributes('aria-expanded')
    ).toBe('true')
    expect(wrapper.text()).toContain(sessionID)
    expect(wrapper.text()).toContain(childThreadID)
    expect(showError).toHaveBeenCalledWith('request failed')

    wrapper.unmount()
  })

  it('loads a fresh page-one snapshot after enabling observation', async () => {
    listObservations
      .mockResolvedValueOnce(
        response({ enabled: false, items: [], total: 0, pages: 1, snapshot_seq: 0 })
      )
      .mockResolvedValueOnce(response())
    updateSettings.mockResolvedValue({ installation_observation_enabled: true })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(listObservations).toHaveBeenNthCalledWith(
      2,
      { page: 1, page_size: 20, snapshot_seq: 0 },
      { signal: expect.any(AbortSignal) }
    )
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.text()).toContain('alice')
    expect(
      wrapper.get('button[aria-controls="fingerprint-user-0"]').attributes('aria-expanded')
    ).toBe('false')
    expect(wrapper.text()).not.toContain('Codex workstation')
    expect(wrapper.text()).not.toContain(sessionID)
    expect(showSuccess).toHaveBeenCalledOnce()

    wrapper.unmount()
  })

  it('ignores a toggle response that arrives after the view is disposed', async () => {
    const staleToggle = deferred<{ installation_observation_enabled: boolean }>()
    listObservations.mockResolvedValue(response())
    updateSettings.mockReturnValue(staleToggle.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[role="switch"]').trigger('click')
    wrapper.unmount()
    staleToggle.resolve({ installation_observation_enabled: false })
    await flushPromises()

    expect(listObservations).toHaveBeenCalledOnce()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })
})
