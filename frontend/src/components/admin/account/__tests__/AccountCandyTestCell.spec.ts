import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCandyTestCell from '../AccountCandyTestCell.vue'
import PelicanHTMLThumbnail from '../PelicanHTMLThumbnail.vue'
import type { Account } from '@/types'
import type { CandyTestItem } from '@/api/admin/candyTests'

const api = vi.hoisted(() => ({ history: vi.fn() }))
vi.mock('@/api/admin/candyTests', async () => ({ ...await vi.importActual<typeof import('@/api/admin/candyTests')>('@/api/admin/candyTests'), candyTestsAPI: api }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const source = '<html><body><svg></svg><script>requestAnimationFrame(() => {})</script></body></html>'
function result(id: number, status: CandyTestItem['status'] = 'generated'): CandyTestItem {
  return { id, batch_id: 'batch', account_id: 42, account_name: 'Fixture', model: 'fixture-model', reasoning_effort: '', prompt_version: 'pelican-v1', status, created_at: '', started_at: null, finished_at: '2026-09-30T00:00:00Z', cancel_requested: false }
}
function account(latest: CandyTestItem): Account { return { id: 42, name: 'Fixture', platform: 'openai', candy_test: { latest } } as Account }

let resize: ResizeObserverCallback
let intersect: IntersectionObserverCallback
let height: number
const disconnectResize = vi.fn()
const disconnectIntersection = vi.fn()
function setVisible(visible: boolean) { intersect([{ isIntersecting: visible } as IntersectionObserverEntry], {} as IntersectionObserver) }
function mountCell(latest: CandyTestItem) {
  const host = document.createElement('table')
  host.innerHTML = '<tbody><tr><td></td></tr></tbody>'
  document.body.append(host)
  const wrapper = mount(AccountCandyTestCell, { props: { account: account(latest) }, attachTo: host.querySelector('td')! })
  return { wrapper, dispose: () => { wrapper.unmount(); host.remove() } }
}

describe('account row pelican preview', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    height = 120
    vi.stubGlobal('ResizeObserver', class { constructor(callback: ResizeObserverCallback) { resize = callback } observe() {} disconnect = disconnectResize })
    vi.stubGlobal('IntersectionObserver', class { constructor(callback: IntersectionObserverCallback) { intersect = callback } observe() {} disconnect = disconnectIntersection })
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(function() { return this.tagName === 'TD' ? height : 0 })
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(240)
  })
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('loads only visible generated results and scales to the cell without contributing height', async () => {
    const latest = result(101)
    api.history.mockResolvedValue({ items: [{ ...latest, html: source }] })
    const { wrapper, dispose } = mountCell(latest)
    expect(api.history).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="pelican-cell"]').classes()).toContain('h-0')
    expect(wrapper.get('[data-testid="pelican-cell-content"]').classes()).toContain('absolute')
    setVisible(true)
    await flushPromises()
    expect(api.history).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    const frame = wrapper.get('iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    expect(frame.attributes('tabindex')).toBe('-1')
    expect(frame.attributes('scrolling')).toBe('no')
    expect(frame.element.style.transform).toBe('scale(0.2)')
    expect(frame.element.style.width).toBe('960px')
    expect(frame.element.style.height).toBe('600px')
    expect(frame.element.style.left).toBe('24px')
    expect(wrapper.get('[data-testid="pelican-cell-content"]').element.style.height).toBe('120px')
    height = 60
    resize([], {} as ResizeObserver)
    await flushPromises()
    expect(wrapper.get('iframe').element).toBe(frame.element)
    expect(frame.element.style.transform).toBe('scale(0.1)')
    await wrapper.setProps({ account: account({ ...latest }) })
    await flushPromises()
    expect(api.history).toHaveBeenCalledTimes(1)
    expect(wrapper.get('iframe').element).toBe(frame.element)
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
    dispose()
    expect(disconnectResize).toHaveBeenCalled()
    expect(disconnectIntersection).toHaveBeenCalled()
  })

  it('unmounts hidden animation and uses cached HTML after virtual row remount', async () => {
    const latest = result(102)
    api.history.mockResolvedValue({ items: [{ ...latest, html: source }] })
    const first = mountCell(latest)
    setVisible(true)
    await flushPromises()
    setVisible(false)
    await flushPromises()
    expect(first.wrapper.find('iframe').exists()).toBe(false)
    first.dispose()
    const second = mountCell(latest)
    setVisible(true)
    await flushPromises()
    expect(second.wrapper.find('iframe').exists()).toBe(true)
    expect(api.history).toHaveBeenCalledTimes(1)
    second.dispose()
  })

  it('replaces a new result and ignores an older request still in flight', async () => {
    let resolveOld!: (value: unknown) => void
    api.history.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { wrapper, dispose } = mountCell(result(103))
    setVisible(true)
    await flushPromises()
    const oldSignal = api.history.mock.calls[0][1] as AbortSignal
    api.history.mockResolvedValueOnce({ items: [{ ...result(104), html: source.replace('<svg>', '<svg id="new">') }] })
    await wrapper.setProps({ account: account(result(104)) })
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOld({ items: [{ ...result(103), html: source }] })
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('id="new"')
    await wrapper.setProps({ account: account(result(105, 'failed')) })
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.text()).toContain('candyTests.failed')
    dispose()
  })

  it('never falls back to an older HTML when history is newer than the summary', async () => {
    api.history.mockResolvedValue({ items: [result(107, 'abnormal'), { ...result(106), html: source }] })
    const { wrapper, dispose } = mountCell(result(106))
    setVisible(true)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.text()).toContain('candyTests.previewUnavailable')
    dispose()
  })

  it.each(['failed', 'abnormal', 'cancelled', 'skipped'] as const)('does not fetch an animation for a latest %s result', async status => {
    const { wrapper, dispose } = mountCell(result(108, status))
    setVisible(true)
    await flushPromises()
    expect(api.history).not.toHaveBeenCalled()
    expect(wrapper.find('iframe').exists()).toBe(false)
    dispose()
  })

  it('limits both dimensions with the same scale and does not restart on a width change', async () => {
    const wrapper = mount(PelicanHTMLThumbnail, { props: { html: source, width: 120, height: 200 } })
    const frame = wrapper.get('iframe').element
    expect(frame.style.transform).toBe('scale(0.125)')
    expect(frame.style.top).toBe('62.5px')
    await wrapper.setProps({ width: 240 })
    expect(wrapper.get('iframe').element).toBe(frame)
    expect(frame.style.transform).toBe('scale(0.25)')
    wrapper.unmount()
  })
})
