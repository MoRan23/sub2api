import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import PelicanHTMLPreview from '../PelicanHTMLPreview.vue'
import { PELICAN_PREVIEW_CSP, pelicanPreviewDocument } from '../pelicanPreview'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const html = '<!doctype html><html><body><svg><animate attributeName="cx" /></svg><script>requestAnimationFrame(() => {})</script></body></html>'
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('Pelican HTML preview', () => {
  it('passes the parent nonce only to inline scripts inside the sandbox', () => {
    const source = pelicanPreviewDocument(html + '<script src="https://example.invalid/script.js"></script>', 'fixture-nonce')
    expect(source).toContain('<script nonce="fixture-nonce">')
    expect(source).toContain("querySelectorAll('script:not([src])')")
    expect(source.match(/<script\b/g)).toHaveLength(1)
    expect(source.match(/<\/script>/g)).toHaveLength(1)
    expect(source.indexOf('Content-Security-Policy')).toBeLessThan(source.indexOf('<script'))
    expect(source).toContain('\\u003cscript')
  })
  it('isolates scripts and preserves local SVG and JavaScript behind the first CSP', async () => {
    const wrapper = mount(PelicanHTMLPreview, { props: { html, itemId: 7 } })
    const frame = wrapper.get('iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    const srcdoc = frame.attributes('srcdoc')!
    expect(srcdoc).toContain(PELICAN_PREVIEW_CSP)
    expect(srcdoc.indexOf('Content-Security-Policy')).toBeLessThan(srcdoc.indexOf('<script>'))
    expect(srcdoc).toContain("connect-src 'none'")
    expect(srcdoc).toContain("default-src 'none'")
    expect(srcdoc).toContain("base-uri 'none'")
    expect(srcdoc).toContain("form-action 'none'")
    expect(srcdoc.endsWith(html)).toBe(true)
    expect(wrapper.find('svg').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pelican-source"] pre').text()).toBe(html)
    expect(wrapper.get('[data-testid="pelican-source"]').attributes('open')).toBeUndefined()
    await wrapper.get('[data-testid="pelican-enlarge"]').trigger('click')
    expect(wrapper.get('iframe').element).toBe(frame.element)
    expect(wrapper.get('iframe').classes()).toContain('h-[80vh]')
    await wrapper.get('[data-testid="pelican-toggle"]').trigger('click')
    expect(wrapper.find('iframe').exists()).toBe(false)
    await wrapper.get('[data-testid="pelican-toggle"]').trigger('click')
    expect(wrapper.get('iframe').element).not.toBe(frame.element)
    wrapper.unmount()
  })

  it('downloads the original HTML and releases its object URL', async () => {
    const createObjectURL = vi.fn<(blob: Blob) => string>(() => 'blob:pelican-fixture')
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function(this: HTMLAnchorElement) {
      expect(this.download).toBe('pelican-7.html')
      expect(this.href).toBe('blob:pelican-fixture')
    })
    const wrapper = mount(PelicanHTMLPreview, { props: { html, itemId: 7 } })
    await wrapper.get('[data-testid="pelican-download"]').trigger('click')
    expect(click).toHaveBeenCalledOnce()
    const blob = createObjectURL.mock.calls[0]?.[0] as unknown as Blob
    expect(blob.type).toBe('text/html;charset=utf-8')
    const contents = await new Promise(resolve => { const reader = new FileReader(); reader.onload = () => resolve(reader.result); reader.readAsText(blob) })
    expect(contents).toBe(html)
    wrapper.unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:pelican-fixture')
  })
})
