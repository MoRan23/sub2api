import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import ChannelStatusV1View from '../ChannelStatusV1View.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn() }))
vi.mock('@/api/channelMonitor', () => mocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: { channel_monitor_enabled: true }, showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
enableAutoUnmount(afterEach)
afterEach(() => { vi.useRealTimers(); localStorage.clear() })

function detail(availability: number) {
  return { id: 1, models: [{ model: 'model', availability_15d: availability, availability_30d: availability }] }
}
beforeEach(() => {
  vi.useFakeTimers()
  vi.resetAllMocks()
  localStorage.clear()
  localStorage.setItem('channel-status-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 30 }))
  mocks.list.mockImplementation(async () => ({ items: [{ id: 1, name: 'Monitor', primary_model: 'model', primary_status: 'operational', availability_7d: 99 }] }))
  mocks.status.mockResolvedValue(detail(90))
})

function mountView() {
  return shallowMount(ChannelStatusV1View, {
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      MonitorHero: { props: ['loading'], emits: ['update:window', 'refresh'], template: `<div :data-loading="loading">
        <button class="fifteen" @click="$emit('update:window', '15d')">15d</button>
        <button class="thirty" @click="$emit('update:window', '30d')">30d</button>
        <button class="refresh" @click="$emit('refresh')">Refresh</button>
      </div>` },
      MonitorCardGrid: false,
      MonitorCard: { props: ['availabilityValue'], template: '<output>{{ availabilityValue }}</output>' }
    } }
  })
}

describe('channel monitor automatic detail refresh', () => {
  it('uses the selected batch window and never requests details from a current server', async () => {
    mocks.list.mockImplementation(async () => ({ items: [{ id: 1, name: 'Monitor', primary_model: 'model', primary_status: 'operational', availability_7d: 99, availability_15d: 95, availability_30d: 91 }] }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.fifteen').trigger('click')
    await flushPromises()
    expect(mocks.list).toHaveBeenLastCalledWith({ window: '15d', signal: expect.any(AbortSignal) })
    expect(wrapper.get('output').text()).toBe('95')
    await wrapper.get('.thirty').trigger('click')
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('91')
    expect(mocks.status).not.toHaveBeenCalled()
  })

  it('bounds legacy fanout to four and cancels it when the window changes', async () => {
    mocks.list.mockResolvedValue({ items: Array.from({ length: 20 }, (_, index) => ({ id: index + 1, primary_model: 'model', primary_status: 'operational', availability_7d: 99 })) })
    const resolves: Array<() => void> = []
    mocks.status.mockImplementation((id: number) => new Promise(resolve => resolves.push(() => resolve({ id, ...detail(98) }))))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.fifteen').trigger('click')
    await flushPromises()
    expect(mocks.status).toHaveBeenCalledTimes(4)
    const signals = mocks.status.mock.calls.map(call => call[1].signal as AbortSignal)
    await wrapper.get('.thirty').trigger('click')
    await flushPromises()
    expect(signals.every(signal => signal.aborted)).toBe(true)
    expect(mocks.status).toHaveBeenCalledTimes(8)
    wrapper.unmount()
    for (const resolve of resolves) resolve()
    await flushPromises()
    expect(mocks.status).toHaveBeenCalledTimes(8)
  })

  it('reuses an in-flight legacy refresh for the same selected window', async () => {
    let finish: (value: unknown) => void = () => {}
    mocks.status.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.fifteen').trigger('click')
    await flushPromises()
    await wrapper.get('.refresh').trigger('click')
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.status).toHaveBeenCalledTimes(1)
    finish(detail(97))
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('97')
  })

  it.each(['fifteen', 'thirty'])('refreshes the cached %s-day availability on timer ticks', async (tab) => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.' + tab).trigger('click')
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('90')
    mocks.status.mockResolvedValue(detail(98))
    await vi.advanceTimersByTimeAsync(30000)
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('98')
    expect(mocks.status).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-loading]').attributes('data-loading')).toBe('false')
  })

  it('keeps the seven-day view on the list endpoint without extra detail requests', async () => {
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30000)
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.status).not.toHaveBeenCalled()
    expect(wrapper.get('output').text()).toBe('99')
  })

  it('still refreshes cached availability on manual reload', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.fifteen').trigger('click')
    await flushPromises()
    mocks.status.mockResolvedValue(detail(98))
    await wrapper.get('.refresh').trigger('click')
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('98')
  })
})
