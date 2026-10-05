import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import Select from '@/components/common/Select.vue'
import AccountAttributionCell from '../AccountAttributionCell.vue'
import AttributionHistory from '../AttributionHistory.vue'
import AttributionModal from '../AttributionModal.vue'
import ModelAttributionView from '@/views/admin/ModelAttributionView.vue'
import type { AttributionConfig, AttributionJob, AttributionPage } from '@/api/admin/modelAttribution'
import type { AccountListItem } from '@/types'

const api = vi.hoisted(() => ({ config: vi.fn(), save: vi.fn(), models: vi.fn(), create: vi.fn(), history: vi.fn(), job: vi.fn() }))
vi.mock('@/api/admin/modelAttribution', () => ({ attributionAPI: api }))
vi.mock('@/api/admin/groups', async () => ({ ...await vi.importActual<typeof import('@/api/admin/groups')>('@/api/admin/groups'), getAllIncludingInactive: vi.fn().mockResolvedValue([{ id: 10, name: 'Pro group' }, { id: 20, name: 'Other group' }]) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
const policy = { model: 'gpt-6-astra', high_models: ['gpt-6-astra'], low_models: ['gpt-6-luna'] }
function config(): AttributionConfig { return { version: 1, enabled: false, base_url: '', default: structuredClone(policy), groups: [], group_priority: [], new_account_tests: { attribution: true, pelican: true, attribution_model: 'gpt-6-astra', pelican_model: 'gpt-6.1-sol' } } }
function job(id = 1, status: AttributionJob['status'] = 'passed'): AttributionJob {
  return { id, status, account_id: 42, account_name: 'Synthetic account', source: 'manual', snapshot: { config_version: 2, group_id: 0, policy: structuredClone(policy) }, result: { duration_ms: 1200, action: 'high', analysis: { prediction: 'gpt-6-astra', probability: 0.8, used_outputs: 3, results: [{ model: 'gpt-6-astra', probability: 0.8 }, { model: 'gpt-6-luna', probability: 0.2 }] } }, created_at: '2026-10-01T00:00:00Z', ...(status === 'running' ? {} : { finished_at: '2026-10-01T00:00:01Z' }) }
}
function page(items: AttributionJob[]): AttributionPage { return { items, total: items.length, page: 1, page_size: 20, queued: 0, running: 0 } }
const stubs = {
  AppLayout: defineComponent({ template: '<main><slot /></main>' }),
  ModelWhitelistSelector: defineComponent({ props: ['modelValue'], template: '<div data-testid="whitelist">{{ modelValue }}</div>' }),
  BaseDialog: defineComponent({ props: ['show'], template: '<div v-if="show"><slot /></div>' }),
  RouterLink: defineComponent({ template: '<a><slot /></a>' })
}

beforeEach(() => { vi.clearAllMocks(); api.config.mockResolvedValue(config()); api.models.mockResolvedValue(['gpt-6-astra', 'gpt-6.1-sol']); api.history.mockResolvedValue(page([])); api.job.mockImplementation(async (id: number) => job(id)) })
afterEach(() => { vi.useRealTimers() })

describe('attribution account UI', () => {
  it('separates the active task from the latest ended result and refreshes both', async () => {
    const account = { id: 42, platform: 'openai', type: 'oauth', model_attribution: { latest: job(), active: job(2, 'running') } } as AccountListItem
    const wrapper = mount(AccountAttributionCell, { props: { account } })
    expect(wrapper.get('[data-testid="attribution-active"]').text()).toBe('attribution.status.running')
    expect(wrapper.get('[data-testid="attribution-latest"]').text()).toBe('attribution.status.passed')
    expect(wrapper.text()).toContain('gpt-6-astra · 80.0%')
    await wrapper.setProps({ account: { ...account, model_attribution: { latest: job(2, 'mismatch') } } })
    expect(wrapper.find('[data-testid="attribution-active"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="attribution-latest"]').text()).toBe('attribution.status.mismatch')
    await wrapper.get('button').trigger('click'); expect(wrapper.emitted('open')).toHaveLength(1)
    wrapper.unmount()
  })
  it('supports manual API Key detection and displays its latest result', async () => {
    const wrapper = mount(AccountAttributionCell, { props: { account: { id: 43, platform: 'openai', type: 'apikey', model_attribution: { latest: job() } } as AccountListItem } })
    expect(wrapper.text()).toContain('attribution.manualOnly')
    expect(wrapper.text()).toContain('gpt-6-astra · 80.0%')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
    expect(api.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('shows skip reasons and excludes other platforms', () => {
    const wrapper = mount(AccountAttributionCell, { props: { account: { platform: 'openai', type: 'oauth', model_attribution: { skip_reason: 'shadow_account' } } as AccountListItem } })
    expect(wrapper.text()).toContain('attribution.reasons.shadow_account'); wrapper.unmount()
    const other = mount(AccountAttributionCell, { props: { account: { platform: 'anthropic', type: 'apikey' } as AccountListItem } })
    expect(other.find('button').exists()).toBe(false); other.unmount()
  })
  it('queues the selected batch once and shows skipped accounts', async () => {
    api.config.mockResolvedValue({ ...config(), enabled: true, base_url: 'http://modeltrace.invalid' })
    api.create.mockResolvedValue([{ ...job(3, 'skipped'), reason: 'shadow_account' }])
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42, 43] }, global: { stubs } })
    await flushPromises(); await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenCalledTimes(1); expect(api.create).toHaveBeenCalledWith([42, 43], undefined)
    expect(wrapper.text()).toContain('attribution.reasons.shadow_account'); expect(wrapper.emitted('updated')).toHaveLength(1)
    wrapper.unmount()
  })
  it('allows manual model selection while automatic detection is disabled without changing configuration', async () => {
    const saved = { ...config(), base_url: 'http://modeltrace.invalid' }
    api.config.mockResolvedValue(saved)
    api.create.mockResolvedValue([job(3, 'running')])
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42] }, global: { stubs } })
    await flushPromises()
    expect(wrapper.findComponent(Select).props('options')).toContainEqual({ value: 'gpt-6.1-sol', label: 'gpt-6.1-sol' })
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'gpt-6.1-sol')
    await flushPromises()
    expect(wrapper.get('[data-testid="attribution-run"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenCalledWith([42], 'gpt-6.1-sol')
    expect(api.save).not.toHaveBeenCalled()
    expect(saved.default.model).toBe('gpt-6-astra')
    wrapper.unmount()
  })
  it('supports a typed model when loading candidates fails and rejects invalid model IDs', async () => {
    api.config.mockResolvedValue({ ...config(), base_url: 'http://modeltrace.invalid' })
    api.models.mockRejectedValue(new Error('offline'))
    api.create.mockResolvedValue([job(3, 'running')])
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42] }, global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('attribution.modelsUnavailable')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'gpt-*')
    await flushPromises(); await wrapper.get('[data-testid="attribution-run"]').trigger('click')
    expect(api.create).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('attribution.invalidManualModel')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'custom-model')
    await flushPromises(); await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenCalledWith([42], 'custom-model')
    wrapper.unmount()
  })
  it('requires the service address, not the automatic detection switch', async () => {
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42] }, global: { stubs } })
    await flushPromises()
    expect(wrapper.get('[data-testid="attribution-run"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('attribution.reasons.service_unconfigured')
    expect(api.models).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('refreshes history without changing a selected old result', async () => {
    vi.useFakeTimers(); api.history.mockResolvedValue(page([job(1)]))
    const wrapper = mount(AttributionHistory, { props: { accountId: 42 } })
    await flushPromises(); await wrapper.get('tbody button').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="attribution-result"]').text()).toContain('#1')
    api.history.mockResolvedValue(page([job(2), job(1)]))
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.get('[data-testid="attribution-result"]').text()).toContain('#1')
    await wrapper.setProps({ accountId: 43 }); await flushPromises()
    expect(api.history).toHaveBeenLastCalledWith(43, 1)
    expect(wrapper.find('[data-testid="attribution-result"]').exists()).toBe(false)
    wrapper.unmount(); const count = api.history.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000); expect(api.history).toHaveBeenCalledTimes(count)
  })
})

describe('attribution configuration', () => {
  it('saves group priority globally without changing independent policies', async () => {
    api.save.mockImplementation(async (c: AttributionConfig) => ({ ...c, version: 2 }))
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    const priority = wrapper.get('[data-testid="global-group-priority"]')
    await priority.get('select').setValue(10)
    await priority.get('[data-testid="priority-add"]').trigger('click')
    await priority.get('select').setValue(20)
    await priority.get('[data-testid="priority-add"]').trigger('click')
    await priority.findAll('[data-testid="priority-up"]')[1].trigger('click')
    expect(priority.findAll('[data-testid="priority-row"]')[0].text()).toContain('Other group')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ group_priority: [20, 10], groups: [] }))
    wrapper.unmount()
  })
  it('saves independent initial test switches and models without enabling periodic detection', async () => {
    api.save.mockImplementation(async (c: AttributionConfig) => ({ ...c, version: 2 }))
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    expect((wrapper.get('[data-testid="initial-attribution-model"]').element as HTMLInputElement).value).toBe('gpt-6-astra')
    expect((wrapper.get('[data-testid="initial-pelican-model"]').element as HTMLInputElement).value).toBe('gpt-6.1-sol')
    expect(wrapper.text()).toContain('attribution.newAccount.requiresEnabled')
    await wrapper.get('[data-testid="initial-attribution"]').setValue(false)
    await wrapper.get('[data-testid="initial-pelican"]').setValue(false)
    await wrapper.get('[data-testid="initial-attribution-model"]').setValue('gpt-6-sol')
    expect((wrapper.get('[data-testid="initial-pelican-model"]').element as HTMLInputElement).value).toBe('gpt-6.1-sol')
    await wrapper.get('[data-testid="initial-pelican-model"]').setValue('gpt-6.1-custom')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, default: policy, new_account_tests: { attribution: false, pelican: false, attribution_model: 'gpt-6-sol', pelican_model: 'gpt-6.1-custom' } }))
    wrapper.unmount()
  })
  it.each(['initial-attribution-model', 'initial-pelican-model'])('rejects invalid %s before saving', async (field) => {
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    await wrapper.get(`[data-testid="${field}"]`).setValue('gpt-*')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('attribution.newAccount.invalid')
    wrapper.unmount()
  })
  it('starts disabled and blocks enabling without a service and both allowlists', async () => {
    const c = config(); c.default.high_models = []; api.config.mockResolvedValue(c)
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    expect((wrapper.get('[data-testid="attribution-enable"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-testid="attribution-enable"]').setValue(true)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).not.toHaveBeenCalled(); expect(wrapper.get('[role="alert"]').text()).toContain('attribution.invalid'); wrapper.unmount()
  })
  it('uses the existing whitelist selector, supports group inheritance and versioned saves', async () => {
    api.save.mockImplementation(async (c: AttributionConfig) => ({ ...c, version: c.version + 1 }))
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    expect(wrapper.findAll('[data-testid="whitelist"]')).toHaveLength(2)
    await wrapper.get('select[aria-label="attribution.group"]').setValue(10)
    await wrapper.findAll('button').find(b => b.text() === 'attribution.addGroup')!.trigger('click')
    expect(wrapper.text()).toContain('Pro group'); expect(wrapper.text()).toContain('attribution.inherit')
    await wrapper.get('[data-testid="attribution-url"]').setValue('http://localhost:5000')
    await wrapper.get('[data-testid="attribution-enable"]').setValue(true)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ version: 1, enabled: true, groups: [expect.objectContaining({ group_id: 10, enabled: false })] }))
    expect(wrapper.text()).toContain('attribution.saved'); wrapper.unmount()
  })
  it('keeps unsaved edits and reports optimistic version conflicts', async () => {
    api.save.mockRejectedValue({ status: 409, message: 'Configuration changed' })
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    await wrapper.get('[data-testid="attribution-url"]').setValue('http://localhost:5000')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('attribution.conflict')
    expect((wrapper.get('[data-testid="attribution-url"]').element as HTMLInputElement).value).toBe('http://localhost:5000'); wrapper.unmount()
  })
})
