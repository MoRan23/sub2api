import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const copyToClipboard = vi.fn().mockResolvedValue(true)

const messages: Record<string, string> = {
  'keys.endpoints.title': 'API 端点',
  'keys.endpoints.default': '默认',
  'keys.endpoints.copied': '已复制',
  'keys.endpoints.copiedHint': '已复制到剪贴板',
  'keys.endpoints.clickToCopy': '点击可复制此端点',
  'keys.endpoints.speedTest': '测速',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard,
  }),
}))

import EndpointPopover from '../EndpointPopover.vue'

describe('EndpointPopover', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('将说明提示渲染到 URL 上方而不是旧的 title 图标上', () => {
    const wrapper = mount(EndpointPopover, {
      props: {
        apiBaseUrl: 'https://default.example.com/v1',
        customEndpoints: [
          {
            name: '备用线路',
            endpoint: 'https://backup.example.com/v1',
            description: '自定义说明',
          },
        ],
      },
    })

    expect(wrapper.text()).toContain('自定义说明')
    expect(wrapper.text()).toContain('点击可复制此端点')
    expect(wrapper.find('[role="button"]').attributes('title')).toBeUndefined()
    expect(wrapper.find('[title="自定义说明"]').exists()).toBe(false)
  })

  it('点击 URL 后会复制并切换为已复制提示', async () => {
    const wrapper = mount(EndpointPopover, {
      props: {
        apiBaseUrl: 'https://default.example.com/v1',
        customEndpoints: [],
      },
    })

    await wrapper.find('[role="button"]').trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('https://default.example.com/v1', '已复制')
    expect(wrapper.text()).toContain('已复制到剪贴板')
    expect(wrapper.find('button[aria-label="已复制到剪贴板"]').exists()).toBe(true)
  })

  it('keeps long endpoints inside a shrinkable responsive row', () => {
    const longEndpoint = `https://gateway.example.com/${'very-long-path/'.repeat(12)}v1`
    const wrapper = mount(EndpointPopover, {
      props: {
        apiBaseUrl: longEndpoint,
        customEndpoints: [],
      },
    })

    const list = wrapper.get('[data-test="endpoint-list"]')
    const item = wrapper.get('[data-test="endpoint-item"]')
    const url = wrapper.get('[data-test="endpoint-url"]')
    const tooltip = wrapper.get('[data-test="endpoint-tooltip"]')

    expect(list.classes()).toEqual(expect.arrayContaining(['min-w-0', 'max-w-full', 'flex-wrap']))
    expect(item.classes()).toEqual(
      expect.arrayContaining(['relative', 'w-full', 'min-w-0', 'max-w-full', 'sm:w-auto'])
    )
    expect(url.classes()).toEqual(expect.arrayContaining(['min-w-0', 'flex-1', 'truncate']))
    expect(tooltip.classes()).toEqual(
      expect.arrayContaining(['right-0', 'max-w-[min(24rem,100%)]'])
    )
    expect(tooltip.classes()).not.toEqual(expect.arrayContaining(['sm:right-auto', 'sm:-translate-x-1/2']))
    expect(tooltip.element.parentElement?.classList.contains('relative')).toBe(false)
    expect(url.text()).toBe(longEndpoint)
    expect(item.findAll('button, a').every((control) => control.classes().includes('shrink-0'))).toBe(true)
  })
})
