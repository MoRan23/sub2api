import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCandyTestCell from '../AccountCandyTestCell.vue'
import PelicanHTMLThumbnail from '../PelicanHTMLThumbnail.vue'
import { createPelicanThumbnailLayout, pelicanThumbnailLayoutKey } from '../pelicanThumbnailLayout'
import { formatDateTime } from '@/utils/format'
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
function mountCell(latest: CandyTestItem, layout = createPelicanThumbnailLayout()) {
  const host = document.createElement('table')
  host.innerHTML = '<tbody><tr><td><div data-natural-height></div></td><td data-preview></td></tr></tbody>'
  document.body.append(host)
  const wrapper = mount(AccountCandyTestCell, {
    props: { account: account(latest) }, attachTo: host.querySelector('[data-preview]')!,
    global: { provide: { [pelicanThumbnailLayoutKey as symbol]: layout } },
  })
  return { wrapper, dispose: () => { wrapper.unmount(); host.remove() } }
}

describe('account row pelican preview', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    height = 120
    vi.stubGlobal('ResizeObserver', class { constructor(callback: ResizeObserverCallback) { resize = callback } observe() {} disconnect = disconnectResize })
    vi.stubGlobal('IntersectionObserver', class { constructor(callback: IntersectionObserverCallback) { intersect = callback } observe() {} disconnect = disconnectIntersection })
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(function() { return this.tagName === 'TD' ? height + 8 : 0 })
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function() { return parseFloat(this.style.width) || 240 })
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function() {
      return { top: 0, bottom: this.hasAttribute('data-natural-height') ? height + 8 : 0 } as DOMRect
    })
  })
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('loads only visible results and fits the timestamp within the shared row height', async () => {
    const latest = result(101)
    api.history.mockResolvedValue({ items: [{ ...latest, html: source }] })
    const { wrapper, dispose } = mountCell(latest)
    await flushPromises()
    expect(api.history).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="pelican-cell"]').element.style.height).toBe('128px')
    expect(wrapper.get('[data-testid="pelican-cell-content"]').classes()).toContain('absolute')
    setVisible(true)
    await flushPromises()
    expect(api.history).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    const frame = wrapper.get('iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    expect(frame.attributes('tabindex')).toBe('-1')
    expect(frame.attributes('scrolling')).toBe('no')
    expect(frame.element.style.transform).toBe(`scale(${100 / 600})`)
    expect(frame.element.style.width).toBe('960px')
    expect(frame.element.style.height).toBe('600px')
    expect(frame.element.style.left).toBe('0px')
    expect(wrapper.get('[data-testid="pelican-cell-content"]').element.style.height).toBe('120px')
    expect(wrapper.get('[data-testid="pelican-last-test"]').element.style.height).toBe('20px')
    height = 60
    resize([], {} as ResizeObserver)
    await flushPromises()
    expect(wrapper.get('iframe').element).toBe(frame.element)
    expect(frame.element.style.transform).toBe(`scale(${40 / 600})`)
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

  it('uses the tallest natural row, expands shorter previews, and shrinks after resizing or removal', async () => {
    const layout = createPelicanThumbnailLayout()
    const short = mountCell({ ...result(201), html: source }, layout)
    const resizeShort = resize
    setVisible(true)
    height = 180
    const tall = mountCell({ ...result(202), html: source }, layout)
    const resizeTall = resize
    setVisible(true)
    height = 80
    const otherList = mountCell({ ...result(203), html: source })
    setVisible(true)
    await flushPromises()
    height = 120
    resizeShort([], {} as ResizeObserver)
    height = 180
    resizeTall([], {} as ResizeObserver)
    await flushPromises()
    const shortFrame = short.wrapper.get('iframe').element
    const tallFrame = tall.wrapper.get('iframe').element
    expect(shortFrame.style.transform).toBe(`scale(${160 / 600})`)
    expect(tallFrame.style.transform).toBe(shortFrame.style.transform)
    expect(shortFrame.style.top).toBe('0px')
    expect(tallFrame.style.top).toBe('0px')
    expect(short.wrapper.get('[data-testid="pelican-cell"]').element.style.height).toBe('188px')
    expect(short.wrapper.get('[data-testid="pelican-cell"]').element.style.width).toBe('288px')
    expect(otherList.wrapper.get('iframe').element.style.transform).toBe('scale(0.1)')

    height = 60
    resizeShort([], {} as ResizeObserver)
    await flushPromises()
    expect(shortFrame.style.transform).toBe(`scale(${160 / 600})`)
    expect(tallFrame.style.transform).toBe(shortFrame.style.transform)
    expect(tallFrame.style.top).toBe('0px')
    expect(tall.wrapper.get('[data-testid="pelican-cell-content"]').element.style.height).toBe('180px')
    expect(short.wrapper.get('iframe').element).toBe(shortFrame)
    expect(tall.wrapper.get('iframe').element).toBe(tallFrame)

    height = 100
    resizeTall([], {} as ResizeObserver)
    await flushPromises()
    expect(tallFrame.style.transform).toBe(`scale(${80 / 600})`)
    expect(shortFrame.style.transform).toBe(tallFrame.style.transform)
    expect(short.wrapper.get('[data-testid="pelican-cell"]').element.style.height).toBe('108px')
    tall.dispose()
    await flushPromises()
    expect(shortFrame.style.transform).toBe(`scale(${40 / 600})`)
    expect(short.wrapper.get('iframe').element).toBe(shortFrame)
    short.dispose()
    otherList.dispose()
  })

  it('includes a tall status-only row without reserving preview space for that row', async () => {
    const layout = createPelicanThumbnailLayout()
    height = 60
    const generated = mountCell({ ...result(204), html: source }, layout)
    setVisible(true)
    height = 140
    const failed = mountCell(result(205, 'failed'), layout)
    setVisible(true)
    await flushPromises()
    expect(generated.wrapper.get('iframe').element.style.transform).toBe('scale(0.2)')
    expect(failed.wrapper.get('[data-testid="pelican-cell"]').element.style.height).toBe('0px')
    expect(failed.wrapper.find('iframe').exists()).toBe(false)
    failed.dispose()
    await flushPromises()
    expect(generated.wrapper.get('iframe').element.style.transform).toBe(`scale(${40 / 600})`)
    generated.dispose()
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

  it('keeps the last completed time and animation while the next test queues and runs', async () => {
    const latest = { ...result(206), html: source }
    const { wrapper, dispose } = mountCell(latest)
    setVisible(true)
    await flushPromises()
    const frame = wrapper.get('iframe').element
    for (const status of ['queued', 'running'] as const) {
      const active = { ...result(207, status), created_at: '2026-09-30T01:00:00Z', started_at: status === 'running' ? '2026-09-30T01:01:00Z' : null, finished_at: null }
      await wrapper.setProps({ account: { ...account(latest), candy_test: { latest, active } } })
      await flushPromises()
      expect(wrapper.text()).toContain(`candyTests.${status}`)
      expect(wrapper.get('time').attributes('datetime')).toBe(latest.finished_at)
      expect(wrapper.get('time').text()).toBe(formatDateTime(latest.finished_at))
      expect(wrapper.get('iframe').element).toBe(frame)
    }
    const completed = { ...result(207), html: source, finished_at: '2026-09-30T01:05:00Z' }
    await wrapper.setProps({ account: account(completed) })
    await flushPromises()
    expect(wrapper.get('time').attributes('datetime')).toBe(completed.finished_at)
    expect(wrapper.text()).not.toContain('candyTests.running')
    dispose()
  })

  it('does not invent a last test time for an account with only a queued test', async () => {
    const active = { ...result(208, 'queued'), finished_at: null }
    const { wrapper, dispose } = mountCell(active)
    await wrapper.setProps({ account: { ...account(active), candy_test: { active } } })
    setVisible(true)
    await flushPromises()
    expect(wrapper.text()).toContain('candyTests.queued')
    expect(wrapper.find('time').exists()).toBe(false)
    expect(api.history).not.toHaveBeenCalled()
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
    expect(wrapper.get('time').attributes('datetime')).toBe(result(108, status).finished_at)
    dispose()
  })

  it('keeps the unscaled overlay on the rendered page when resizing without restarting the frame', async () => {
    const wrapper = mount(PelicanHTMLThumbnail, { props: { html: source, width: 120, height: 200 }, slots: { overlay: '<span>排队中</span>' } })
    const frame = wrapper.get('iframe').element
    const overlay = wrapper.get('[data-testid="pelican-preview-overlay"]').element
    expect(frame.style.transform).toBe('scale(0.125)')
    expect(frame.style.top).toBe('62.5px')
    expect(parseFloat(frame.style.borderRadius) * 0.125).toBe(4)
    expect(overlay.style.width).toBe('120px')
    expect(overlay.style.height).toBe('75px')
    expect(overlay.style.top).toBe('62.5px')
    expect(overlay.style.transform).toBe('')
    await wrapper.setProps({ width: 240 })
    expect(wrapper.get('iframe').element).toBe(frame)
    expect(frame.style.transform).toBe('scale(0.25)')
    expect(parseFloat(frame.style.borderRadius) * 0.25).toBe(4)
    expect(overlay.style.width).toBe('240px')
    expect(overlay.style.height).toBe('150px')
    expect(overlay.style.top).toBe('25px')
    expect(overlay.style.transform).toBe('')
    wrapper.unmount()
  })
})
