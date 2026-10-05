import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { OpenAIDaybreakCapabilities } from '@/types'

const { getCapabilities } = vi.hoisted(() => ({ getCapabilities: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getDaybreakCapabilities: getCapabilities } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
import OpenAIDaybreakSettings from '../OpenAIDaybreakSettings.vue'

const available: OpenAIDaybreakCapabilities = {
  checked_at: '2026-10-05T12:00:00Z', blue_available: true, red_available: true,
  models: [
    { model: 'gpt-6-sol', required_tier: 'blue', cyber: 'daybreak_blue' },
    { model: 'gpt-6-astra', required_tier: 'red', cyber: 'daybreak_blue' },
    { model: 'gpt-5.6-cyber', required_tier: 'red', cyber: 'daybreak_red' },
  ], reason: '',
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((finish) => { resolve = finish })
  return { promise, resolve }
}

const wrappers: ReturnType<typeof mount>[] = []
function mountSettings(props = {}) {
  const wrapper = mount(OpenAIDaybreakSettings, {
    props: { accountId: 1, active: true, blue: false, red: false, ...props },
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('OpenAIDaybreakSettings', () => {
  beforeEach(() => getCapabilities.mockReset().mockResolvedValue(available))
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

  it('does not check an unsaved account and keeps both switches off and disabled', async () => {
    const wrapper = mountSettings({ accountId: undefined })
    await flushPromises()
    expect(getCapabilities).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="daybreak-create-hint"]').exists()).toBe(true)
    for (const key of ['blue', 'red']) {
      const toggle = wrapper.get(`[data-testid="daybreak-${key}"]`)
      expect(toggle.attributes('aria-checked')).toBe('false')
      expect(toggle.attributes('disabled')).toBeDefined()
    }
  })

  it('requires evidence and Blue before enabling Red, then turns both off together', async () => {
    const pending = deferred<OpenAIDaybreakCapabilities>()
    getCapabilities.mockReturnValueOnce(pending.promise)
    const wrapper = mountSettings()
    expect(wrapper.get('[data-testid="daybreak-blue"]').attributes('disabled')).toBeDefined()
    pending.resolve(available)
    await flushPromises()
    expect(getCapabilities).toHaveBeenCalledWith(1)
    expect(wrapper.get('[data-testid="daybreak-blue"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="daybreak-blue"]').trigger('click')
    expect(wrapper.emitted('update:blue')).toEqual([[true]])
    await wrapper.setProps({ blue: true })
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="daybreak-red"]').trigger('click')
    expect(wrapper.emitted('update:red')).toEqual([[true]])
    await wrapper.setProps({ red: true })
    await wrapper.get('[data-testid="daybreak-blue"]').trigger('click')
    expect(wrapper.emitted('update:blue')?.at(-1)).toEqual([false])
    expect(wrapper.emitted('update:red')?.at(-1)).toEqual([false])
    expect(wrapper.get('[data-testid="daybreak-models"]').text()).toContain('gpt-6-astra · Blue + Red · daybreak_blue')
  })

  it('allows turning saved switches off while loading or after a capability failure', async () => {
    getCapabilities.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountSettings({ blue: true, red: true })
    expect(wrapper.get('[data-testid="daybreak-blue"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeUndefined()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('checkFailed')
    await wrapper.get('[data-testid="daybreak-blue"]').trigger('click')
    expect(wrapper.emitted('update:blue')).toEqual([[false]])
    expect(wrapper.emitted('update:red')).toEqual([[false]])
    await wrapper.setProps({ blue: false, red: false })
    expect(wrapper.get('[data-testid="daybreak-blue"]').attributes('disabled')).toBeDefined()
  })

  it('does not reuse capability evidence from the previously selected account', async () => {
    const oldRequest = deferred<OpenAIDaybreakCapabilities>()
    const currentRequest = deferred<OpenAIDaybreakCapabilities>()
    getCapabilities.mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(currentRequest.promise)
    const wrapper = mountSettings()
    await wrapper.setProps({ accountId: 2 })
    currentRequest.resolve({ ...available, blue_available: false, red_available: false, models: [], reason: 'no available_access_programs' })
    await flushPromises()
    oldRequest.resolve(available)
    await flushPromises()
    expect(wrapper.get('[data-testid="daybreak-blue"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="daybreak-reason"]').text()).toBe('no available_access_programs')
    expect(wrapper.find('[data-testid="daybreak-models"]').exists()).toBe(false)
  })

  it('clears old evidence on refresh and on close, and rechecks when reopened', async () => {
    const wrapper = mountSettings({ blue: true })
    await flushPromises()
    const refreshed = deferred<OpenAIDaybreakCapabilities>()
    getCapabilities.mockReturnValueOnce(refreshed.promise)
    await wrapper.get('[data-testid="daybreak-refresh"]').trigger('click')
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ active: false })
    refreshed.resolve(available)
    await flushPromises()
    expect(wrapper.find('[data-testid="daybreak-models"]').exists()).toBe(false)
    await wrapper.setProps({ active: true })
    await flushPromises()
    expect(getCapabilities).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeUndefined()
  })

  it('does not enable Red from Blue-only evidence', async () => {
    getCapabilities.mockResolvedValueOnce({ ...available, red_available: false })
    const wrapper = mountSettings({ blue: true })
    await flushPromises()
    expect(wrapper.get('[data-testid="daybreak-red"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="daybreak-red"]').trigger('click')
    expect(wrapper.emitted('update:red')).toBeUndefined()
  })
})
