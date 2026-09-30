import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCandyTestModal from '../AccountCandyTestModal.vue'
import AccountCandyTestCell from '../AccountCandyTestCell.vue'
import AccountCandyTestResult from '../AccountCandyTestResult.vue'
import { candyTestsEn } from '@/i18n/locales/candyTests'
import type { CandyTestBatch, CandyTestItem } from '@/api/admin/candyTests'
import type { Account } from '@/types'

const api = vi.hoisted(() => ({ options: vi.fn(), create: vi.fn(), getBatch: vi.fn(), cancel: vi.fn(), history: vi.fn() }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const lookup = (key: string): unknown => key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, { candyTests: candyTestsEn })
  return { ...actual, useI18n: () => ({
    t: (key: string, params?: Record<string, string | number>) => String(lookup(key) || key).replace(/\{(\w+)\}/g, (_, name: string) => String(params?.[name] ?? `{${name}}`)),
    te: (key: string) => typeof lookup(key) === 'string',
  }) }
})
vi.mock('@/api/admin/candyTests', async () => ({
  ...await vi.importActual<typeof import('@/api/admin/candyTests')>('@/api/admin/candyTests'),
  candyTestsAPI: api,
}))

function item(overrides: Partial<CandyTestItem> = {}): CandyTestItem {
  return { id: 1, batch_id: 'batch-a', account_id: 42, account_name: 'OpenAI account', model: 'gpt-6-astra', reasoning_effort: 'high', prompt_version: 'pelican-v1', status: 'running', created_at: '2026-09-28T10:00:00Z', started_at: '2026-09-28T10:00:01Z', finished_at: null, cancel_requested: false, ...overrides }
}
function batch(overrides: Partial<CandyTestBatch> = {}): CandyTestBatch {
  return { id: 'batch-a', model: 'gpt-6-astra', reasoning_effort: 'high', prompt_version: 'pelican-v1', created_at: '2026-09-28T10:00:00Z', finished_at: null, total: 1, retained_total: 1, counts: { running: 1 }, items: [item()], page: 1, page_size: 20, ...overrides }
}
const html = '<html><body><svg><circle r="12" /></svg><script>requestAnimationFrame(() => {})</script></body></html>'
function generated(id = 1, accountId = 42): CandyTestItem {
  return item({ id, account_id: accountId, status: 'generated', html, response_text: html, finished_at: '2026-09-28T10:01:00Z' })
}
function mountModal(ids = [42]) {
  return mount(AccountCandyTestModal, {
    props: { show: false, accountIds: ids, accounts: [{ id: 42, platform: 'openai', name: 'OpenAI account' }] as Account[] },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        Select: {
          props: ['modelValue', 'options', 'id', 'ariaLabel', 'disabled'], emits: ['update:modelValue'],
          template: '<select :id="id" :aria-label="ariaLabel" :disabled="disabled" :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select>',
        },
      },
    },
  })
}

describe('Account candy tests', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    api.options.mockResolvedValue({ models: [{ id: 'gpt-6-astra', display_name: 'Astra', reasoning_efforts: ['low', 'high'] }, { id: 'custom-model', display_name: 'Custom', reasoning_efforts: [] }], accounts: [{ account_id: 42, account_name: 'OpenAI account', models: [] }] })
    api.history.mockResolvedValue({ items: [], summary: {} })
    api.create.mockResolvedValue(batch())
    api.getBatch.mockResolvedValue(batch())
    api.cancel.mockResolvedValue(undefined)
  })
  afterEach(() => { vi.useRealTimers() })

  it('submits all frozen cross-page IDs with one model and effort', async () => {
    const wrapper = mountModal([42, 99, 42, 110])
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('#candy-test-effort').setValue('high')
    await wrapper.setProps({ accountIds: [42] })
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(api.create).toHaveBeenCalledWith({ account_ids: [42, 99, 110], model: 'gpt-6-astra', reasoning_effort: 'high', idempotency_key: expect.any(String) })
    expect(api.options).toHaveBeenCalledWith([42, 99, 110], expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('limits unknown models to the default effort', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('#candy-test-effort').setValue('high')
    await wrapper.get('#candy-test-model').setValue('custom-model')
    expect(wrapper.get('#candy-test-effort').findAll('option')).toHaveLength(1)
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(api.create.mock.calls[0][0].reasoning_effort).toBe('')
    wrapper.unmount()
  })

  it('shows per-account catalog failures while keeping other upstream models selectable', async () => {
    api.options.mockResolvedValue({
      models: [{ id: 'upstream-raw-model', display_name: 'Upstream model', reasoning_efforts: [] }],
      accounts: [
        { account_id: 42, account_name: 'Available', models: [{ id: 'upstream-raw-model' }] },
        { account_id: 99, account_name: 'Unavailable', models: [], skip_reason: 'model_catalog_failed' },
        { account_id: 110, account_name: 'Empty catalog', models: [], skip_reason: 'no_supported_models' },
      ],
    })
    const wrapper = mountModal([42, 99, 110])
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('[data-testid="candy-model-failures"]').text()).toContain('Unavailable: The upstream model list could not be fetched')
    expect(wrapper.get('[data-testid="candy-model-failures"]').text()).toContain('Empty catalog: The upstream returned no available models')
    expect(wrapper.get('#candy-test-model').element).toHaveProperty('value', 'upstream-raw-model')
    expect(wrapper.get('[data-testid="candy-start"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('aborts pending catalog reads on close while loading history without waiting for upstream', async () => {
    api.options.mockImplementationOnce(() => new Promise(() => {}))
    api.history.mockResolvedValue({ items: [], summary: { active: item() } })
    const wrapper = mountModal([42, 99])
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.text()).toContain('Fetching upstream models for 2 accounts')
    expect(api.history).toHaveBeenCalledWith(42)
    expect(api.getBatch).toHaveBeenCalledWith('batch-a', 1)
    expect(wrapper.get('[data-testid="candy-start"]').attributes('disabled')).toBeDefined()
    const signal = api.options.mock.calls[0][1] as AbortSignal
    expect(signal.aborted).toBe(false)
    await wrapper.setProps({ show: false })
    expect(signal.aborted).toBe(true)
    expect(api.cancel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('refreshes a failed live catalog without clearing an unchanged model selection', async () => {
    api.options.mockRejectedValueOnce(new Error('upstream unavailable'))
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Could not fetch upstream model lists')
    expect(wrapper.get('[data-testid="candy-start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="candy-refresh"]').trigger('click')
    await flushPromises()
    expect(api.options).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('#candy-test-model').setValue('custom-model')
    await wrapper.get('[data-testid="candy-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('#candy-test-model').element).toHaveProperty('value', 'custom-model')
    wrapper.unmount()
  })

  it('polls every five seconds and closing never cancels the backend job', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(api.getBatch).toHaveBeenCalledTimes(1)
    expect(api.options).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ show: false })
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.getBatch).toHaveBeenCalledTimes(1)
    expect(api.cancel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('restores an active batch from account history on reopening', async () => {
    api.history.mockResolvedValue({ items: [], summary: { active: item() } })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(api.getBatch).toHaveBeenCalledWith('batch-a', 1)
    expect(wrapper.find('[data-testid="candy-item-1"]').exists()).toBe(true)
    expect(api.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('opens batch details below the test list and model controls', async () => {
    api.history.mockResolvedValue({ items: [], summary: { active: item() } })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    const row = wrapper.get('[data-testid="candy-item-1"]')
    await row.findAll('button')[0]!.trigger('click')
    const resultSection = wrapper.get('[data-testid="pelican-result-section"]').element
    expect(row.element.compareDocumentPosition(resultSection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(wrapper.get('#candy-test-model').element.compareDocumentPosition(resultSection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(resultSection.compareDocumentPosition(wrapper.get('[data-testid="pelican-history"]').element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(wrapper.findAllComponents(AccountCandyTestResult)).toHaveLength(1)
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(1)
    wrapper.unmount()
  })

  it('only cancels when explicitly requested', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="candy-cancel-batch"]').trigger('click')
    await flushPromises()
    expect(api.cancel).toHaveBeenCalledWith('batch-a', undefined)
    wrapper.unmount()
  })

  it('reuses the idempotency key after an uncertain create result', async () => {
    api.create.mockRejectedValueOnce(new Error('connection lost'))
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('will not create a duplicate')
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(api.create.mock.calls[0][0].idempotency_key).toBe(api.create.mock.calls[1][0].idempotency_key)
    wrapper.unmount()
  })

  it('shows server failures and skipped accounts without judging them as abnormal', async () => {
    api.create.mockResolvedValue(batch({ total: 2, counts: { failed: 1, skipped: 1 }, items: [item({ status: 'failed', failure_code: 'timeout' }), item({ id: 2, status: 'skipped', failure_code: 'unsupported_model' })], finished_at: '2026-09-28T10:01:00Z' }))
    const wrapper = mountModal([42, 99])
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Test failed')
    expect(wrapper.text()).toContain('Skipped')
    expect(wrapper.text()).not.toContain('Abnormal')
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.getBatch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps latest terminal result visible while another test is running', () => {
    const wrapper = mount(AccountCandyTestCell, { props: { account: { id: 42, platform: 'openai', candy_test: { latest: item({ status: 'generated', finished_at: '2026-09-28T10:01:00Z' }), active: item({ id: 2, status: 'running' }) } } as Account } })
    expect(wrapper.text()).toContain('Generated')
    expect(wrapper.text()).toContain('Testing')
    wrapper.unmount()
  })

  it('uses retained item count for completed batch pagination', async () => {
    api.create.mockResolvedValue(batch({ total: 50, retained_total: 2, counts: { generated: 50 }, items: [item({ status: 'generated' })], finished_at: '2026-09-28T10:01:00Z' }))
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('50 / 50 completed')
    expect(wrapper.text()).not.toContain('Next')
    wrapper.unmount()
  })

  it('ignores a stale options response after closing and opening a different selection', async () => {
    let resolveOld!: (value: unknown) => void
    api.options.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ accountIds: [99], show: true })
    await flushPromises()
    resolveOld({ models: [{ id: 'stale-model', reasoning_efforts: [] }], accounts: [] })
    await flushPromises()
    expect(wrapper.get('#candy-test-model').text()).not.toContain('stale-model')
    await wrapper.get('[data-testid="candy-start"]').trigger('click')
    await flushPromises()
    expect(api.create.mock.calls[0][0].account_ids).toEqual([99])
    expect(api.create.mock.calls[0][0].model).toBe('gpt-6-astra')
    wrapper.unmount()
  })

  it('shows completed unresolved answers as abnormal and keeps the reason and original answer', () => {
    const answer = '|问题|最少数量|\n|第1问|未确定|\n|第2问|不知道|\n|第3问固定|待定|\n|第3问自适应|无法判断|'
    const wrapper = mount(AccountCandyTestResult, { props: { item: item({ status: 'abnormal', failure_code: 'missing_html', answers: {}, response_text: answer }) } })
    expect(wrapper.text()).toContain('Abnormal')
    expect(wrapper.text()).not.toContain('Test failed')
    expect(wrapper.get('[role="status"]').text()).toContain('does not contain a complete HTML document')
    expect(wrapper.get('pre').text()).toBe(answer)
    expect(wrapper.find('table').exists()).toBe(false)
    wrapper.unmount()
  })

  it('renders the raw answer as text without executing it or showing the old answer table', () => {
    const wrapper = mount(AccountCandyTestResult, { props: { item: item({ status: 'failed', failure_code: 'missing_terminal', answers: { q1_fixed: 32, q2_adaptive: 29, q3_fixed: 40, q3_adaptive: 38 }, response_text: '<img src=x onerror="alert(1)">' }) } })
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('pre').text()).toContain('<img')
    expect(wrapper.text()).toContain('Test failed')
    expect(wrapper.text()).not.toContain('Generated')
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.text()).toContain('without a successful completion event')
    wrapper.unmount()
  })

  it('opens the latest animation before the model catalog resolves, with history folded', async () => {
    api.options.mockImplementationOnce(() => new Promise(() => {}))
    api.history.mockResolvedValue({ items: [generated(2), generated(1)] })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.findAllComponents(AccountCandyTestResult)).toHaveLength(1)
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(2)
    expect(wrapper.get('[data-testid="pelican-history"]').attributes('open')).toBeUndefined()
    await wrapper.setProps({ show: false })
    expect(wrapper.find('iframe').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each(['failed', 'abnormal'] as const)('shows the latest %s reason without falling back to a generated result', async (status) => {
    api.history.mockResolvedValue({ items: [item({ id: 2, status, failure_code: 'missing_html' }), generated()] })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(2)
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.text()).toContain('does not contain a complete HTML document')
    wrapper.unmount()
  })

  it('follows new results but preserves manual history selection and iframe identity during polling', async () => {
    api.history.mockResolvedValue({ items: [generated(2), generated(1)] })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    const frame = wrapper.get('iframe').element
    const changes = vi.fn()
    const observer = new MutationObserver(changes)
    observer.observe(frame, { attributes: true, attributeFilter: ['srcdoc'] })
    api.history.mockResolvedValue({ items: [{ ...generated(2) }, generated(1)] })
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.get('iframe').element).toBe(frame)
    expect(changes).not.toHaveBeenCalled()
    observer.disconnect()
    api.history.mockResolvedValue({ items: [generated(3), generated(2), generated(1)] })
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(3)
    await wrapper.get('[data-testid="pelican-history-1"]').trigger('click')
    const oldFrame = wrapper.get('iframe').element
    api.history.mockResolvedValue({ items: [generated(4), generated(3), generated(2)] })
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(1)
    expect(wrapper.get('iframe').element).toBe(oldFrame)
    await wrapper.get('[data-testid="pelican-return-latest"]').trigger('click')
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(4)
    wrapper.unmount()
  })

  it('defaults to the first account and selects the switched account latest result, ignoring stale reads', async () => {
    api.history.mockResolvedValueOnce({ items: [generated(1, 42), generated(2, 42)] })
    const wrapper = mountModal([42, 99])
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(api.history).toHaveBeenCalledWith(42)
    await wrapper.get('[data-testid="pelican-history-2"]').trigger('click')
    let resolveOld!: (value: unknown) => void
    api.history.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    await wrapper.get('select[aria-label="Account history"]').setValue(99)
    expect(wrapper.find('iframe').exists()).toBe(false)
    api.history.mockResolvedValueOnce({ items: [generated(3, 42)] })
    await wrapper.get('select[aria-label="Account history"]').setValue(42)
    await flushPromises()
    resolveOld({ items: [generated(9, 99)] })
    await flushPromises()
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(3)
    api.history.mockResolvedValueOnce({ items: [generated(10, 99)] })
    await wrapper.get('select[aria-label="Account history"]').setValue(99)
    await flushPromises()
    expect(wrapper.getComponent(AccountCandyTestResult).props('item').id).toBe(10)
    expect(wrapper.find('[data-testid="pelican-return-latest"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('refreshes history after the final batch poll', async () => {
    api.history.mockResolvedValue({ items: [], summary: { active: item() } })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    api.getBatch.mockImplementationOnce(async () => {
      api.history.mockResolvedValue({ items: [generated()] })
      return batch({ items: [generated()], counts: { generated: 1 }, finished_at: '2026-09-28T10:01:00Z' })
    })
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(true)
    wrapper.unmount()
  })

  it('does not expose old puzzle history or summaries', async () => {
    const old = item({ status: 'abnormal', prompt_version: 'candy-v1' })
    api.history.mockResolvedValue({ items: [old], summary: { active: old } })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.findComponent(AccountCandyTestResult).exists()).toBe(false)
    expect(api.getBatch).not.toHaveBeenCalled()
    const cell = mount(AccountCandyTestCell, { props: { account: { platform: 'openai', candy_test: { latest: old } } as Account } })
    expect(cell.text()).toContain('Not tested')
    cell.unmount()
    wrapper.unmount()
  })
})
