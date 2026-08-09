import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AutoRefreshButton from '../AutoRefreshButton.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

describe('AutoRefreshButton', () => {
  it('renders an explicit paused state without a spinning icon', () => {
    const wrapper = mount(AutoRefreshButton, {
      props: {
        enabled: true,
        paused: true,
        intervalSeconds: 5,
        countdown: 4,
        intervals: [5, 10],
      },
    })

    expect(wrapper.get('button').text()).toContain('common.autoRefresh.paused')
    expect(wrapper.get('button').attributes('title')).toBe('common.autoRefresh.paused')
    expect(wrapper.get('svg').classes()).not.toContain('animate-spin')
  })

  it('keeps the existing countdown and spinning state when active', () => {
    const wrapper = mount(AutoRefreshButton, {
      props: {
        enabled: true,
        intervalSeconds: 5,
        countdown: 4,
        intervals: [5, 10],
      },
    })

    expect(wrapper.get('button').text()).toContain('common.autoRefresh.countdown')
    expect(wrapper.get('svg').classes()).toContain('animate-spin')
  })
})
