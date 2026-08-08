import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

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
const threadID = '018f5c3c-6e3a-7abd-8def-1234567890ac'

const observation = {
  timestamp: '2026-08-08T12:00:00Z',
  account_id: 42,
  account_name: 'OpenAI OAuth',
  pinned: true,
  client_reported_installation_id: 'client-installation',
  outbound_installation_id: 'outbound-installation',
  session_id: sessionID,
  thread_id: threadID,
  user_agent: 'codex_cli_rs/1.0',
  originator: 'codex_cli_rs',
  openai_beta: 'responses=experimental',
  version: '1.0.0',
  inbound_endpoint: 'POST /v1/responses',
}

const DataTableStub = defineComponent({
  name: 'DataTable',
  props: {
    columns: { type: Array, default: () => [] },
    data: { type: Array, default: () => [] },
    loading: Boolean,
    rowKey: String,
  },
  template: '<div data-testid="fingerprint-table">{{ JSON.stringify(data) }}</div>',
})

function mountView() {
  return mount(FingerprintObservationView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<section><slot name="filters" /><slot name="table" /></section>',
        },
        DataTable: DataTableStub,
        AutoRefreshButton: {
          props: ['enabled', 'intervalSeconds', 'countdown', 'intervals'],
          emits: ['update:enabled', 'update:interval'],
          template: '<button data-testid="auto-refresh" @click="$emit(\'update:enabled\', true)">auto</button>',
        },
        Icon: true,
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

  it('renders the existing fingerprint columns plus independent UUID fields', async () => {
    listObservations.mockResolvedValue({ enabled: true, entries: [observation] })
    const wrapper = mountView()
    await flushPromises()

    const table = wrapper.findComponent(DataTableStub)
    const columnKeys = (table.props('columns') as Array<{ key: string }>).map((column) => column.key)
    expect(columnKeys).toEqual([
      'timestamp',
      'account',
      'mode',
      'installation',
      'session_id',
      'thread_id',
      'identity',
      'inbound_endpoint',
    ])
    expect(table.text()).toContain(sessionID)
    expect(table.text()).toContain(threadID)

    wrapper.unmount()
  })

  it('persists the page toggle and clears rows after disabling observation', async () => {
    listObservations
      .mockResolvedValueOnce({ enabled: true, entries: [observation] })
      .mockResolvedValueOnce({ enabled: false, entries: [] })
    updateSettings.mockResolvedValue({ installation_observation_enabled: false })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(updateSettings).toHaveBeenCalledWith({ installation_observation_enabled: false })
    expect(wrapper.findComponent(DataTableStub).props('data')).toEqual([])
    expect(showSuccess).toHaveBeenCalledOnce()

    wrapper.unmount()
  })

  it('restores the toggle state and reports an update failure', async () => {
    listObservations.mockResolvedValue({ enabled: false, entries: [] })
    updateSettings.mockRejectedValue(new Error('write failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(showError).toHaveBeenCalledWith('request failed')

    wrapper.unmount()
  })

  it('ignores an older poll and performs a fresh read after toggling', async () => {
    let resolveStalePoll: ((value: { enabled: boolean; entries: (typeof observation)[] }) => void) | undefined
    const stalePoll = new Promise<{ enabled: boolean; entries: (typeof observation)[] }>((resolve) => {
      resolveStalePoll = resolve
    })
    listObservations
      .mockResolvedValueOnce({ enabled: true, entries: [observation] })
      .mockReturnValueOnce(stalePoll)
      .mockResolvedValueOnce({ enabled: false, entries: [] })
    updateSettings.mockResolvedValue({ installation_observation_enabled: false })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')
    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ installation_observation_enabled: false })

    resolveStalePoll?.({ enabled: true, entries: [observation] })
    await flushPromises()

    expect(listObservations).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.findComponent(DataTableStub).props('data')).toEqual([])
    expect(showSuccess).toHaveBeenCalledOnce()

    wrapper.unmount()
  })

  it('polls every five seconds, pauses while hidden, and stops after unmount', async () => {
    vi.useFakeTimers()
    localStorage.setItem(
      'admin-fingerprint-observation-auto-refresh',
      JSON.stringify({ enabled: true, interval_seconds: 5 }),
    )
    listObservations.mockResolvedValue({ enabled: true, entries: [] })
    const wrapper = mountView()
    await flushPromises()
    expect(listObservations).toHaveBeenCalledTimes(1)

    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(listObservations).toHaveBeenCalledTimes(1)

    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(listObservations).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(listObservations).toHaveBeenCalledTimes(2)
  })
})
