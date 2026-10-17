import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import AttributionProbeModelField from '../AttributionProbeModelField.vue'
import type { AttributionProbeModels } from '@/api/admin/modelAttribution'

const mocks = vi.hoisted(() => ({ list: vi.fn(), probeModels: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ list: mocks.list }))
vi.mock('@/api/admin/modelAttribution', () => ({ attributionAPI: { probeModels: mocks.probeModels } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const account = (id: number, type = 'oauth') => ({ id, type, platform: 'openai', name: `Account ${id}` })
const catalog = (id: number, models: string[], status = 'success'): AttributionProbeModels => ({ models, accounts: [{ account_id: id, account_name: `Account ${id}`, models, status: status as 'success' | 'partial' | 'error' }] })

beforeEach(() => {
  vi.clearAllMocks()
  mocks.list.mockImplementation(async (_page, _size, filters) => ({ items: [account(filters.type === 'oauth' ? 1 : 2, filters.type)] }))
  mocks.probeModels.mockImplementation(async ([id]) => catalog(id, [`upstream-${id}`, `configured-${id}`]))
})

describe('attribution probe source', () => {
  it('loads accounts on demand, keeps the model unchanged and offers account catalog IDs', async () => {
    const wrapper = mount(AttributionProbeModelField, { props: { modelValue: 'saved-unlisted-model' } })
    expect(mocks.list).not.toHaveBeenCalled()
    expect(mocks.probeModels).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="probe-catalog-source"] button').trigger('click'); await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(wrapper.findComponent(Select).props('options')).toEqual([{ value: 1, label: 'Account 1 · #1' }, { value: 2, label: 'Account 2 · #2' }])
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 2); await flushPromises()
    expect(mocks.probeModels).toHaveBeenLastCalledWith([2], expect.any(AbortSignal))
    expect(wrapper.findAll('datalist option').map(option => option.attributes('value'))).toEqual(['upstream-2', 'configured-2'])
    expect(wrapper.get('input').element.value).toBe('saved-unlisted-model')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.get('input').setValue('custom-target')
    expect(wrapper.emitted('update:modelValue')).toEqual([['custom-target']])
    wrapper.unmount()
  })

  it('limits group searches and initial-test sources without persisting source selection', async () => {
    const wrapper = mount(AttributionProbeModelField, { props: { modelValue: 'probe', groupId: 17 } })
    await wrapper.get('[data-testid="probe-catalog-source"] button').trigger('click'); await flushPromises()
    expect(mocks.list).toHaveBeenCalledWith(1, 20, { platform: 'openai', type: 'oauth', group: '17', search: '' }, { signal: expect.any(AbortSignal) })
    expect(mocks.list).toHaveBeenCalledWith(1, 20, { platform: 'openai', type: 'apikey', group: '17', search: '' }, { signal: expect.any(AbortSignal) })
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 1); await flushPromises()
    await wrapper.setProps({ groupId: undefined, oauthOnly: true })
    expect(wrapper.findAll('datalist option')).toHaveLength(0)
    mocks.list.mockClear()
    wrapper.findComponent(Select).vm.$emit('search', 'needle'); await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(mocks.list).toHaveBeenLastCalledWith(1, 20, { platform: 'openai', type: 'oauth', search: 'needle' }, { signal: expect.any(AbortSignal) })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores stale catalogs after switching account and preserves typed values on failure', async () => {
    let resolveFirst!: (value: AttributionProbeModels) => void
    mocks.probeModels.mockReturnValueOnce(new Promise(resolve => { resolveFirst = resolve }))
    const wrapper = mount(AttributionProbeModelField, { props: { modelValue: 'typed-model' } })
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 1); await flushPromises()
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 2); await flushPromises()
    resolveFirst(catalog(1, ['stale'])); await flushPromises()
    expect(wrapper.findAll('datalist option').map(option => option.attributes('value'))).toEqual(['upstream-2', 'configured-2'])
    mocks.probeModels.mockResolvedValueOnce(catalog(3, ['local-only'], 'partial'))
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 3); await flushPromises()
    expect(wrapper.text()).toContain('attribution.modelsUnavailable')
    expect(wrapper.get('datalist option').attributes('value')).toBe('local-only')
    mocks.probeModels.mockRejectedValueOnce(new Error('offline'))
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 4); await flushPromises()
    expect(wrapper.text()).toContain('attribution.modelsUnavailable')
    expect(wrapper.findAll('datalist option')).toHaveLength(0)
    expect(wrapper.get('input').element.value).toBe('typed-model')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores search results from an old group and can retry a failed search', async () => {
    let resolveOld!: (value: { items: ReturnType<typeof account>[] }) => void
    mocks.list.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mount(AttributionProbeModelField, { props: { modelValue: 'probe', groupId: 17, oauthOnly: true } })
    wrapper.findComponent(Select).vm.$emit('search', 'old'); await flushPromises()
    await wrapper.setProps({ groupId: 18 })
    mocks.list.mockRejectedValueOnce(new Error('offline'))
    wrapper.findComponent(Select).vm.$emit('search', 'new'); await flushPromises()
    expect(wrapper.text()).toContain('attribution.catalogAccountsUnavailable')
    wrapper.findComponent(Select).vm.$emit('search', 'retry'); await flushPromises()
    resolveOld({ items: [account(99)] }); await flushPromises()
    expect(wrapper.findComponent(Select).props('options')).toEqual([{ value: 1, label: 'Account 1 · #1' }])
    expect(wrapper.text()).not.toContain('attribution.catalogAccountsUnavailable')
    wrapper.unmount()
  })
})
