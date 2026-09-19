import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ProxySelector from '../ProxySelector.vue'
import type { Proxy, ProxyTestResult } from '@/types'

const testProxy = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { testProxy } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => { testProxy.mockReset() })

function mountSelector(noProxyLabel?: string) {
  return mount(ProxySelector, {
    props: {
      modelValue: null,
      noProxyLabel,
      proxies: [1, 2].map(id => ({
        id, name: `Proxy ${id}`, host: 'localhost', port: 8080, protocol: 'http'
      } as Proxy))
    },
    global: { stubs: { Icon: true } }
  })
}

describe('ProxySelector overlapping tests and null option', () => {
  it.each([
    ['individual then batch', ['.test-btn', '.batch-test-btn']],
    ['batch then individual', ['.batch-test-btn', '.test-btn']]
  ])('deduplicates %s clicks before disabled buttons render', async (_name, selectors) => {
    const completions = new Map<number, (result: ProxyTestResult) => void>()
    testProxy.mockImplementation((id: number) => new Promise<ProxyTestResult>(resolve => {
      completions.set(id, resolve)
    }))
    const wrapper = mountSelector()
    await wrapper.get('.select-trigger').trigger('click')

    // Dispatch both clicks in one render tick so deduplication cannot rely on disabled DOM alone.
    await Promise.all(selectors.map(selector => wrapper.get(selector).trigger('click')))

    expect(testProxy.mock.calls.map(([id]) => id)).toEqual([1, 2])
    expect(wrapper.findAll('.test-btn').every(button => button.attributes('disabled') !== undefined)).toBe(true)
    expect(wrapper.get('.batch-test-btn').attributes('disabled')).toBeDefined()

    completions.get(1)!({ success: true, message: 'Connected', country: 'US', latency_ms: 12 })
    completions.get(2)!({ success: true, message: 'Connected', country: 'GB', latency_ms: 34 })
    await flushPromises()

    expect(wrapper.text()).toContain('US')
    expect(wrapper.text()).toContain('GB')
    expect(wrapper.findAll('.test-btn').every(button => button.attributes('disabled') === undefined)).toBe(true)
    expect(wrapper.get('.batch-test-btn').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it.each([
    [undefined, 'admin.accounts.noProxy'],
    ['Only learn from normal requests', 'Only learn from normal requests']
  ])('uses the null label %s in both the selected value and option', async (noProxyLabel, expectedLabel) => {
    const wrapper = mountSelector(noProxyLabel)
    expect(wrapper.get('.select-trigger').text()).toBe(expectedLabel)

    await wrapper.setProps({ modelValue: 1 })
    expect(wrapper.get('.select-trigger').text()).toContain('Proxy 1')
    await wrapper.get('.select-trigger').trigger('click')
    const nullOption = wrapper.findAll('.select-option')[0]
    expect(nullOption.text()).toBe(expectedLabel)
    await nullOption.trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[null]])
    expect(wrapper.get('.select-trigger').classes()).not.toContain('select-trigger-open')
    await wrapper.setProps({ modelValue: null })
    expect(wrapper.get('.select-trigger').text()).toBe(expectedLabel)
    expect(testProxy).not.toHaveBeenCalled()
  })
})
