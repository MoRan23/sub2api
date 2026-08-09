import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import Pagination from '../Pagination.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const SelectStub = defineComponent({
  name: 'TestSelect',
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<button type="button">select</button>',
})

function mountPagination(pageSizeOptions?: number[]) {
  return mount(Pagination, {
    props: {
      total: 200,
      page: 1,
      pageSize: 20,
      ...(pageSizeOptions ? { pageSizeOptions } : {}),
    },
    global: {
      stubs: {
        Select: SelectStub,
        Icon: { template: '<span />' },
      },
    },
  })
}

describe('Pagination page size options', () => {
  afterEach(() => {
    delete window.__APP_CONFIG__
  })

  it('uses the globally configured options when no component override is supplied', () => {
    window.__APP_CONFIG__ = { table_page_size_options: [15, 30] }
    const wrapper = mountPagination()

    expect(wrapper.getComponent(SelectStub).props('options')).toEqual([
      { value: 15, label: '15' },
      { value: 30, label: '30' },
    ])
  })

  it('uses only the component-specific options when supplied', () => {
    window.__APP_CONFIG__ = { table_page_size_options: [10, 25, 200] }
    const wrapper = mountPagination([20, 50, 100])

    expect(wrapper.getComponent(SelectStub).props('options')).toEqual([
      { value: 20, label: '20' },
      { value: 50, label: '50' },
      { value: 100, label: '100' },
    ])
  })
})
