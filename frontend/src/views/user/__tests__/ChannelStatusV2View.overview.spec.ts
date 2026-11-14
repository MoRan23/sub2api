import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ChannelStatusV2View from '../ChannelStatusV2View.vue'

const { overviewMounted, detailsMounted } = vi.hoisted(() => ({ overviewMounted: vi.fn(), detailsMounted: vi.fn() }))
vi.mock('@/features/channel-monitor-v2/GroupOverview.vue', () => ({ default: { setup: overviewMounted, template: '<p>Overview loaded</p>' } }))
vi.mock('../ChannelStatusV2Details.vue', () => ({ default: { setup: detailsMounted, template: '<p>Details loaded</p>' } }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('V2 view selection', () => {
  it('defaults to group cards, loads details only on selection, and uses one layout', async () => {
    const wrapper = mount(ChannelStatusV2View, { global: { stubs: { AppLayout: { template: '<main data-layout><slot /></main>' } } } })
    await flushPromises()
    expect(overviewMounted).toHaveBeenCalledTimes(1)
    expect(detailsMounted).not.toHaveBeenCalled()
    expect(wrapper.findAll('[data-layout]')).toHaveLength(1)
    await wrapper.get('#monitor-view-details').trigger('click'); await flushPromises()
    expect(detailsMounted).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Details loaded')
    expect(wrapper.text()).not.toContain('Overview loaded')
    await wrapper.get('#monitor-view-groups').trigger('click'); await flushPromises()
    expect(overviewMounted).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
