import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamGroupSyncButton from '../UpstreamGroupSyncButton.vue'

const mocks = vi.hoisted(() => ({ preview: vi.fn(), showError: vi.fn(), showInfo: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { previewUpstreamGroup: mocks.preview } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function deferred() {
  let resolve!: (value: { name?: string; notes?: string }) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<{ name?: string; notes?: string }>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}

function mountButton() {
  return mount(UpstreamGroupSyncButton, {
    props: { source: { account_id: 1 }, name: 'local', notes: 'local notes' },
    global: { stubs: { Icon: true } }
  })
}

describe('UpstreamGroupSyncButton preview ownership', () => {
  beforeEach(() => vi.clearAllMocks())

  it('keeps fields edited while the preview is in flight', async () => {
    const request = deferred()
    mocks.preview.mockReturnValueOnce(request.promise)
    const wrapper = mountButton()
    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ name: 'edited by administrator' })
    request.resolve({ name: 'upstream', notes: 'upstream notes' })
    await flushPromises()
    expect(wrapper.emitted('update:name')).toBeUndefined()
    expect(wrapper.emitted('update:notes')).toEqual([['upstream notes']])
    wrapper.unmount()
  })

  it('cancels the previous source and keeps the replacement request busy', async () => {
    const first = deferred()
    const second = deferred()
    mocks.preview.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const wrapper = mountButton()
    await wrapper.get('button').trigger('click')
    const firstSignal = mocks.preview.mock.calls[0][1] as AbortSignal
    await wrapper.setProps({ source: { account_id: 2 } })
    expect(firstSignal.aborted).toBe(true)
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    await wrapper.get('button').trigger('click')
    first.resolve({ name: 'stale', notes: 'stale' })
    await flushPromises()
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('update:name')).toBeUndefined()
    second.resolve({ name: 'fresh' })
    await flushPromises()
    expect(wrapper.emitted('update:name')).toEqual([['fresh']])
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores an error after the source becomes unavailable', async () => {
    const request = deferred()
    mocks.preview.mockReturnValueOnce(request.promise)
    const wrapper = mountButton()
    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ source: null })
    request.reject(new Error('cancelled'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('aborts on unmount and does not publish the late result', async () => {
    const request = deferred()
    mocks.preview.mockReturnValueOnce(request.promise)
    const wrapper = mountButton()
    await wrapper.get('button').trigger('click')
    const signal = mocks.preview.mock.calls[0][1] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    request.resolve({ name: 'late' })
    await flushPromises()
    expect(wrapper.emitted('update:name')).toBeUndefined()
  })
})
