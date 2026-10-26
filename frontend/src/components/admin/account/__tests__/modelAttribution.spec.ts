import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import Select from '@/components/common/Select.vue'
import AccountAttributionCell from '../AccountAttributionCell.vue'
import AttributionHistory from '../AttributionHistory.vue'
import AttributionModal from '../AttributionModal.vue'
import AttributionResult from '../AttributionResult.vue'
import AttributionExpectedModels from '../AttributionExpectedModels.vue'
import AttributionPolicyFields from '../AttributionPolicyFields.vue'
import AttributionProbeModelField from '../AttributionProbeModelField.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import ModelAttributionView from '@/views/admin/ModelAttributionView.vue'
import type { AttributionConfig, AttributionJob, AttributionPage } from '@/api/admin/modelAttribution'
import type { AccountListItem } from '@/types'

const api = vi.hoisted(() => ({ config: vi.fn(), save: vi.fn(), models: vi.fn(), probeModels: vi.fn(), connection: vi.fn(), create: vi.fn(), history: vi.fn(), job: vi.fn(), cancel: vi.fn() }))
vi.mock('@/api/admin/modelAttribution', () => ({ attributionAPI: api }))
vi.mock('@/api/admin/groups', async () => ({ ...await vi.importActual<typeof import('@/api/admin/groups')>('@/api/admin/groups'), getAllIncludingInactive: vi.fn().mockResolvedValue([{ id: 10, name: 'Pro group' }, { id: 20, name: 'Other group' }]) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
const policy = { model: 'gpt-6-astra', high_models: ['gpt-6-astra'], low_models: ['gpt-6-luna'] }
const detector = { provider: 'lm_fingerprint_detector', protocol: 1, revision: 'd53d3f5b158249a87a895e4fc7aaa8a940dfd6d6', algorithm: 'shared-detector-v1', bank_built_at: '2026-10-03T00:00:00Z', reference_sha256: 'b'.repeat(64), ranker_sha256: 'c'.repeat(64), calibration_sha256: 'd'.repeat(64) }
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

beforeEach(() => { vi.clearAllMocks(); api.config.mockResolvedValue(config()); api.connection.mockResolvedValue({ models: ['gpt-6-astra'], detector }); api.models.mockResolvedValue(['gpt-6-astra', 'gpt-6.1-sol']); api.probeModels.mockImplementation(async (ids: number[]) => ({ models: ['gpt-6.1-sol'], accounts: ids.map(account_id => ({ account_id, account_name: `Account ${account_id}`, status: 'success', models: ['gpt-6.1-sol'] })) })); api.history.mockResolvedValue(page([])); api.job.mockImplementation(async (id: number) => job(id)) })
afterEach(() => { vi.useRealTimers() })

describe('attribution account UI', () => {
  it.each([
    { mode: 'replace', status: 'passed' },
    { mode: 'replace', status: 'mismatch' },
    { mode: 'schedule', status: 'passed' },
    { mode: 'schedule', status: 'mismatch' }
  ] as const)('keeps the $status verdict visible when $mode mode only records a manual result', ({ mode, status }) => {
    const current = job(9, status)
    current.snapshot.version = 4
    current.snapshot.result_mode = mode
    current.snapshot.manual_result_updates_allowed = false
    current.reason = 'manual_auto_attribution_disabled'
    current.result.action = 'record_only'
    current.result.pass_streak = 0
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.text()).toContain(`attribution.status.${status}`)
    expect(wrapper.text()).toContain('attribution.reasons.manual_auto_attribution_disabled')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.record_only')
    expect(wrapper.get('[data-testid="attribution-result-mode"]').text()).toBe(`attribution.resultModes.${mode}`)
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('0')
    expect(wrapper.text()).not.toContain('attribution.actions.high')
    expect(wrapper.text()).not.toContain('attribution.actions.low')
    expect(wrapper.text()).not.toContain('attribution.actions.schedule_paused')
    expect(wrapper.find('[data-testid="attribution-scheduling-change"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('shows why a legacy manual task only records its result without inferring snapshot permission', () => {
    const current = job()
    current.reason = 'manual_auto_attribution_unrecorded'
    current.result.action = 'record_only'
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.text()).toContain('attribution.reasons.manual_auto_attribution_unrecorded')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.record_only')
    expect(wrapper.text()).toContain('attribution.status.passed')
    wrapper.unmount()
  })
  it('labels record-only results in history alongside their actual verdicts', async () => {
    const passed = job(1, 'passed')
    passed.result.action = 'record_only'
    const mismatch = job(2, 'mismatch')
    mismatch.snapshot.version = 4
    mismatch.snapshot.result_mode = 'schedule'
    mismatch.result.action = 'record_only'
    api.history.mockResolvedValue(page([passed, mismatch]))
    const wrapper = mount(AttributionHistory, { props: { accountId: 42 } })
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('attribution.status.passed')
    expect(rows[1].text()).toContain('attribution.status.mismatch')
    for (const row of rows) expect(row.text()).toContain('attribution.actions.record_only')
    wrapper.unmount()
  })
  it.each(['schedule_paused', 'schedule_resumed', 'schedule_already_paused', 'schedule_manual_pause', 'schedule_ineligible'])('shows the frozen scheduling mode and %s action', (action) => {
    const current = job(9, action === 'schedule_paused' ? 'mismatch' : 'passed')
    current.snapshot.version = 4
    current.snapshot.result_mode = 'schedule'
    current.result.action = action
    current.result.scheduling_before = false
    current.result.scheduling_after = action === 'schedule_resumed'
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.get('[data-testid="attribution-result-mode"]').text()).toBe('attribution.resultModes.schedule')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe(`attribution.actions.${action}`)
    expect(wrapper.get('[data-testid="attribution-scheduling-change"]').text()).toBe(`attribution.schedulingPaused → attribution.${action === 'schedule_resumed' ? 'schedulingEnabled' : 'schedulingPaused'}`)
    expect(wrapper.text()).not.toContain('attribution.high')
    wrapper.unmount()
  })
  it('keeps legacy allowlist history separate from scheduling confirmation and stale results', async () => {
    const current = job()
    current.snapshot.version = 4
    current.snapshot.result_mode = 'schedule'
    current.result.action = 'awaiting_confirmation'
    current.result.pass_streak = 1
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.schedulingActions.awaiting_confirmation')
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('1')
    expect(wrapper.find('[data-testid="attribution-scheduling-change"]').exists()).toBe(false)
    await wrapper.setProps({ job: { ...current, reason: 'scheduling_changed', result: { ...current.result, action: 'stale_policy' } } })
    expect(wrapper.text()).toContain('attribution.reasons.scheduling_changed')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.schedulingActions.stale_policy')
    const legacy = job()
    legacy.result.action = 'awaiting_confirmation'
    await wrapper.setProps({ job: legacy })
    expect(wrapper.get('[data-testid="attribution-result-mode"]').text()).toBe('attribution.resultModes.replace')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.awaiting_confirmation')
    expect(wrapper.text()).toContain('attribution.high')
    await wrapper.setProps({ job: { ...legacy, snapshot: { ...legacy.snapshot, version: 3, result_mode: 'schedule' } } })
    expect(wrapper.get('[data-testid="attribution-result-mode"]').text()).toBe('attribution.resultModes.replace')
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.awaiting_confirmation')
    expect(wrapper.text()).toContain('attribution.high')
    wrapper.unmount()
  })
  it('shows only current attribution-owned pauses in the account cell', async () => {
    const account = { platform: 'openai', type: 'oauth', schedulable: false, model_attribution: { latest: job(), scheduling_paused_by_attribution: true } } as AccountListItem
    const wrapper = mount(AccountAttributionCell, { props: { account } })
    expect(wrapper.get('[data-testid="attribution-scheduling-paused"]').text()).toBe('attribution.pausedByAttribution')
    await wrapper.setProps({ account: { ...account, schedulable: true } })
    expect(wrapper.find('[data-testid="attribution-scheduling-paused"]').exists()).toBe(false)
    await wrapper.setProps({ account: { ...account, model_attribution: { latest: job(), scheduling_paused_by_attribution: false } } })
    expect(wrapper.find('[data-testid="attribution-scheduling-paused"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('shows durable retry progress and preserves the completed verdict when a policy changed', async () => {
    const waiting = job(3, 'retry_wait')
    waiting.snapshot.version = 2
    waiting.result = { duration_ms: 900000, action: 'none', completed_probes: 2, retries: 7 }
    waiting.next_attempt_at = '2026-10-14T08:05:00Z'
    const wrapper = mount(AttributionResult, { props: { job: waiting } })
    expect(wrapper.text()).toContain('attribution.status.retry_wait')
    expect(wrapper.get('[data-testid="attribution-completed-probes"]').text()).toBe('2 / 3')
    expect(wrapper.text()).toContain('attribution.nextAttempt')
    expect(wrapper.text()).toContain('attribution.retries')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('cancel')).toEqual([[3]])
    await wrapper.setProps({ cancelling: true })
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    const completed = job(3, 'passed')
    completed.reason = 'policy_changed'
    completed.result.action = 'stale_policy'
    await wrapper.setProps({ job: completed })
    expect(wrapper.text()).toContain('attribution.status.passed')
    expect(wrapper.text()).toContain('attribution.reasons.policy_changed')
    expect(wrapper.text()).toContain('attribution.actions.stale_policy')
    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('attribution.nextAttempt')
    wrapper.unmount()
  })
  it('keeps waiting jobs active in account summaries', () => {
    const waiting = job(2, 'retry_wait')
    waiting.snapshot.version = 2
    waiting.result = { duration_ms: 0, action: 'none', completed_probes: 2, retries: 4 }
    waiting.next_attempt_at = '2026-10-14T08:05:00Z'
    const wrapper = mount(AccountAttributionCell, { props: { account: { platform: 'openai', type: 'oauth', model_attribution: { active: waiting, latest: job() } } as AccountListItem } })
    expect(wrapper.get('[data-testid="attribution-active"]').text()).toBe('attribution.status.retry_wait')
    expect(wrapper.text()).toContain('attribution.probeProgress')
    expect(wrapper.text()).toContain('attribution.nextAttempt')
    expect(wrapper.get('[data-testid="attribution-latest"]').text()).toBe('attribution.status.passed')
    wrapper.unmount()
  })
  it('polls waiting details and cancels them once without showing stale running state', async () => {
    vi.useFakeTimers()
    const waiting = job(3, 'retry_wait')
    waiting.snapshot.version = 2
    waiting.result = { duration_ms: 0, action: 'none', completed_probes: 2, retries: 4 }
    waiting.next_attempt_at = '2026-10-14T08:05:00Z'
    api.history.mockResolvedValue({ ...page([waiting]), retry_wait: 1 })
    api.job.mockResolvedValue(waiting)
    const wrapper = mount(AttributionHistory, { props: { accountId: 42 } })
    await flushPromises()
    expect(wrapper.text()).toContain('attribution.waitingCount')
    await wrapper.findAll('tbody button').find(button => button.text() === 'attribution.details')!.trigger('click')
    await flushPromises()
    const updated = { ...waiting, result: { ...waiting.result, retries: 5 } }
    api.job.mockResolvedValue(updated)
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(api.job).toHaveBeenCalledTimes(2)
    expect(wrapper.findComponent(AttributionResult).props('job').result.retries).toBe(5)
    let resolveCancel!: (value: AttributionJob) => void
    api.cancel.mockReturnValueOnce(new Promise(resolve => { resolveCancel = resolve }))
    const cancel = wrapper.findAll('tbody button').find(button => button.text() === 'attribution.cancel')!
    await cancel.trigger('click')
    expect(cancel.attributes('disabled')).toBeDefined()
    await cancel.trigger('click')
    expect(api.cancel).toHaveBeenCalledTimes(1)
    expect(api.cancel).toHaveBeenCalledWith(3)
    const cancelled = { ...updated, status: 'cancelled' as const, reason: 'cancelled', next_attempt_at: undefined }
    api.history.mockResolvedValue(page([cancelled]))
    resolveCancel(cancelled); await flushPromises()
    expect(wrapper.get('[data-testid="attribution-result"]').text()).toContain('attribution.status.cancelled')
    expect(wrapper.findAll('button').some(button => button.text() === 'attribution.cancel')).toBe(false)
    expect(wrapper.text()).not.toContain('attribution.waitingCount')
    expect(wrapper.text()).not.toContain('attribution.nextAttempt')
    wrapper.unmount()
  })
  it.each(['passed', 'mismatch'] as const)('does not display zero-value progress for legacy %s history', async (status) => {
    const legacy = job(1, status)
    legacy.result.completed_probes = 0
    const wrapper = mount(AttributionResult, { props: { job: legacy } })
    expect(wrapper.text()).toContain(`attribution.status.${status}`)
    expect(wrapper.find('[data-testid="attribution-completed-probes"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('attribution.completedProbes')
    await wrapper.setProps({ job: { ...legacy, snapshot: { ...legacy.snapshot, version: 2 }, result: { ...legacy.result, completed_probes: 3 } } })
    expect(wrapper.get('[data-testid="attribution-completed-probes"]').text()).toBe('3 / 3')
    wrapper.unmount()
  })
  it('keeps a failed cancellation available for retry', async () => {
    api.history.mockResolvedValue(page([job(3, 'running')]))
    api.cancel.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(AttributionHistory, { props: { accountId: 42 } })
    await flushPromises()
    await wrapper.findAll('tbody button').find(button => button.text() === 'attribution.cancel')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('attribution.error')
    expect(wrapper.findAll('tbody button').find(button => button.text() === 'attribution.cancel')!.attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('attribution.status.running')
    wrapper.unmount()
  })
  it('discards an in-flight detail read when cancellation completes', async () => {
    const running = job(3, 'running')
    const cancelled = { ...running, status: 'cancelled' as const, reason: 'cancelled' }
    api.history.mockResolvedValue(page([running]))
    let resolveDetails!: (value: AttributionJob) => void
    api.job.mockReturnValueOnce(new Promise(resolve => { resolveDetails = resolve }))
    api.cancel.mockResolvedValueOnce(cancelled)
    const wrapper = mount(AttributionHistory, { props: { accountId: 42 } })
    await flushPromises()
    await wrapper.findAll('tbody button').find(button => button.text() === 'attribution.details')!.trigger('click')
    api.history.mockResolvedValue(page([cancelled]))
    await wrapper.findAll('tbody button').find(button => button.text() === 'attribution.cancel')!.trigger('click')
    await flushPromises()
    resolveDetails(running); await flushPromises()
    expect(wrapper.find('[data-testid="attribution-result"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('attribution.status.running')
    expect(wrapper.text()).toContain('attribution.status.cancelled')
    wrapper.unmount()
  })
  it('shows frozen expectations and falls back to the historical probe for old results', async () => {
    const current = job()
    current.snapshot.policy.expected_models = ['gpt-6-astra', 'gpt-6.1-sol']
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.get('[data-testid="result-expected-models"]').text()).toBe('gpt-6-astra, gpt-6.1-sol')
    const legacy = job(); legacy.snapshot.policy.model = 'historical-probe'
    await wrapper.setProps({ job: legacy })
    expect(wrapper.get('[data-testid="result-expected-models"]').text()).toBe('historical-probe')
    wrapper.unmount()
  })
  it('distinguishes inherited, explicit empty and custom batch expectations', async () => {
    api.config.mockResolvedValue({ ...config(), base_url: 'http://detector.invalid' })
    api.create.mockResolvedValue([job(3, 'running')])
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42, 43] }, global: { stubs } })
    await flushPromises()
    const expected = wrapper.findComponent(AttributionExpectedModels)
    expect(expected.props('candidates')).toEqual(['gpt-6-astra', 'gpt-6.1-sol'])
    expect(expected.get('select').element.value).toBe('inherit')
    await expected.get('select').setValue('custom')
    expect(expected.get('[data-testid="expected-models-follow-probe"]').text()).toBe('attribution.expectedFollowProbe')
    await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenLastCalledWith([42, 43], undefined, [])
    expected.findComponent(ModelWhitelistSelector).vm.$emit('update:modelValue', ['gpt-6-astra', 'gpt-6.1-sol'])
    await flushPromises(); await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenLastCalledWith([42, 43], undefined, ['gpt-6-astra', 'gpt-6.1-sol'])
    expected.findComponent(ModelWhitelistSelector).vm.$emit('update:modelValue', ['gpt-*'])
    await flushPromises(); api.create.mockClear()
    await wrapper.get('[data-testid="attribution-run"]').trigger('click')
    expect(api.create).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('attribution.invalidExpectedModels')
    await expected.get('select').setValue('inherit')
    await wrapper.get('[data-testid="attribution-run"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenLastCalledWith([42, 43], undefined, undefined)
    expect(api.save).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('shows null confidence as unavailable and distinguishes legacy history', async () => {
    const current = job()
    current.snapshot.detector = detector
    current.result.analysis!.probability = null
    current.result.analysis!.probability_status = 'unavailable'
    current.result.analysis!.results = [{ model: 'gpt-6-astra', probability: null, score: 1.23 }]
    const wrapper = mount(AttributionResult, { props: { job: current } })
    expect(wrapper.text()).toContain('LM Fingerpoint Detector')
    expect(wrapper.text()).toContain('d53d3f5b1582')
    expect(wrapper.text()).toContain('attribution.unavailable')
    expect(wrapper.text()).not.toContain('0.00%')
    expect(wrapper.text()).toContain('1.2300')
    await wrapper.setProps({ job: job() })
    expect(wrapper.text()).toContain('ModelTrace')
    expect(wrapper.text()).toContain('80.00%')
    wrapper.unmount()
  })
  it.each(['manual', 'scheduled', 'initial'] as const)('shows an immediately applied first valid verdict for %s tests with its actual streak', async (source) => {
    const first = job()
    first.source = source
    first.result.action = 'high'
    first.result.pass_streak = 1
    const wrapper = mount(AttributionResult, { props: { job: first } })
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.high')
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('1')
    expect(wrapper.text()).not.toContain('attribution.actions.awaiting_confirmation')
    await wrapper.setProps({ job: { ...first, status: 'mismatch', result: { ...first.result, action: 'low', pass_streak: 0 } } })
    expect(wrapper.get('[data-testid="attribution-result-action"]').text()).toBe('attribution.actions.low')
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('0')
    wrapper.unmount()
  })
  it('shows later upgrades awaiting confirmation until the second consecutive pass and preserves legacy history', async () => {
    const first = job()
    first.result.action = 'awaiting_confirmation'
    first.result.pass_streak = 1
    const wrapper = mount(AttributionResult, { props: { job: first } })
    expect(wrapper.text()).toContain('attribution.status.passed')
    expect(wrapper.text()).toContain('attribution.actions.awaiting_confirmation')
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('1')
    expect(wrapper.text()).not.toContain('attribution.actions.high')
    await wrapper.setProps({ job: { ...first, result: { ...first.result, action: 'high', pass_streak: 2 } } })
    expect(wrapper.text()).toContain('attribution.actions.high')
    expect(wrapper.get('[data-testid="attribution-pass-streak"]').text()).toBe('2')
    await wrapper.setProps({ job: job() })
    expect(wrapper.find('[data-testid="attribution-pass-streak"]').exists()).toBe(false)
    wrapper.unmount()
  })
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
  it('shows Engine accounts as manual-only until automatic attribution is explicitly enabled', async () => {
    const account = { id: 43, platform: 'openai', type: 'apikey', extra: { openai_api_key_mode: 'codex_engine', openai_passthrough: true }, model_attribution: { latest: job() } } as AccountListItem
    const wrapper = mount(AccountAttributionCell, { props: { account } })
    expect(wrapper.get('[data-testid="attribution-latest"]').text()).toContain('attribution.status.passed')
    expect(wrapper.text()).toContain('attribution.manualOnly')
    expect(wrapper.text()).toContain('gpt-6-astra · 80.0%')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
    await wrapper.setProps({ account: { ...account, extra: { openai_api_key_mode: 'codex_engine', openai_api_key_auto_attribution_enabled: true } } })
    expect(wrapper.text()).not.toContain('attribution.manualOnly')
    await wrapper.setProps({ account: { ...account, extra: { openai_api_key_mode: 'codex_engine', openai_api_key_auto_attribution_enabled: false } } })
    expect(wrapper.text()).toContain('attribution.manualOnly')
    await wrapper.setProps({ account: { ...account, extra: { openai_api_key_mode: 'generic' } } })
    expect(wrapper.text()).toContain('attribution.manualOnly')
    await wrapper.setProps({ account: { ...account, extra: { openai_api_key_mode: 'generic', openai_api_key_auto_attribution_enabled: true } } })
    expect(wrapper.text()).not.toContain('attribution.manualOnly')
    await wrapper.setProps({ account: { ...account, type: 'oauth', extra: {} } })
    expect(wrapper.text()).not.toContain('attribution.manualOnly')
    expect(api.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it.each([
    { platform: 'anthropic', type: 'apikey' },
    { platform: 'openai', type: 'setup-token' }
  ])('does not enable attribution based only on an Engine mode marker: $platform / $type', (account) => {
    const wrapper = mount(AccountAttributionCell, { props: { account: { ...account, extra: { openai_api_key_mode: 'codex_engine' } } as AccountListItem } })
    expect(wrapper.find('[data-testid="attribution-cell"]').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
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
    expect(api.create).toHaveBeenCalledTimes(1); expect(api.create).toHaveBeenCalledWith([42, 43], undefined, undefined)
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
    expect(api.create).toHaveBeenCalledWith([42], 'gpt-6.1-sol', undefined)
    expect(api.save).not.toHaveBeenCalled()
    expect(saved.default.model).toBe('gpt-6-astra')
    wrapper.unmount()
  })
  it('uses the account catalog union for probes and keeps detector candidates only for expectations', async () => {
    api.config.mockResolvedValue({ ...config(), base_url: 'http://detector.invalid' })
    api.models.mockResolvedValue(['detector-only'])
    api.probeModels.mockResolvedValue({ models: ['local-only', 'shared-target'], accounts: [
      { account_id: 42, account_name: 'First', status: 'success', models: ['shared-target'] },
      { account_id: 43, account_name: 'Second', status: 'partial', models: ['local-only', 'shared-target'] }
    ] })
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42, 43] }, global: { stubs } })
    await flushPromises()
    const options = wrapper.findComponent(Select).props('options') as { value: string; label: string }[]
    expect(options.map(option => option.value)).toEqual(['', 'local-only', 'shared-target'])
    expect(wrapper.findComponent(AttributionExpectedModels).props('candidates')).toEqual(['detector-only'])
    expect(wrapper.get('[data-testid="probe-catalog-coverage"]').text()).toContain('First')
    expect(wrapper.get('[data-testid="probe-catalog-coverage"]').text()).toContain('attribution.catalogStatus.partial')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'unlisted-but-valid'); await flushPromises()
    expect(wrapper.get('[data-testid="probe-catalog-coverage"]').text()).toContain('attribution.catalogModelUnlisted')
    expect(wrapper.get('[data-testid="attribution-run"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
  it('discards catalogs belonging to a previously selected batch', async () => {
    api.config.mockResolvedValue({ ...config(), base_url: 'http://detector.invalid' })
    let resolveOld!: (value: unknown) => void
    api.probeModels.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mount(AttributionModal, { props: { show: true, accountIds: [42] }, global: { stubs } })
    await flushPromises()
    api.probeModels.mockResolvedValueOnce({ models: ['current'], accounts: [{ account_id: 43, account_name: 'Current', status: 'success', models: ['current'] }] })
    await wrapper.setProps({ accountIds: [43] }); await flushPromises()
    resolveOld({ models: ['stale'], accounts: [] }); await flushPromises()
    expect(wrapper.findComponent(Select).props('options')).toEqual([{ value: '', label: 'attribution.manualModelDefault' }, { value: 'current', label: 'current' }])
    wrapper.unmount()
  })
  it('supports a typed model when loading candidates fails and rejects invalid model IDs', async () => {
    api.config.mockResolvedValue({ ...config(), base_url: 'http://modeltrace.invalid' })
    api.probeModels.mockRejectedValue(new Error('offline'))
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
    expect(api.create).toHaveBeenCalledWith([42], 'custom-model', undefined)
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
  it('uses independent account catalog sources for global, group and OAuth initial probes', async () => {
    const c = config()
    c.groups = [{ group_id: 10, enabled: true, ...structuredClone(policy) }]
    api.config.mockResolvedValue(c)
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    const sources = wrapper.findAllComponents(AttributionProbeModelField)
    expect(sources).toHaveLength(3)
    expect(sources[0].props('groupId')).toBeUndefined()
    expect(sources[1].props('oauthOnly')).toBe(true)
    expect(sources[2].props('groupId')).toBe(10)
    expect(api.probeModels).not.toHaveBeenCalled()
    expect(wrapper.find('datalist#attribution-models').exists()).toBe(false)
    wrapper.unmount()
  })
  it('saves global, independent group and initial expected models separately', async () => {
    const c = config()
    c.base_url = 'http://detector.invalid'
    c.groups = [{ group_id: 10, enabled: true, ...structuredClone(policy) }]
    api.config.mockResolvedValue(c)
    api.connection.mockResolvedValue({ models: ['gpt-6-astra', 'gpt-6.1-sol'], detector })
    api.save.mockImplementation(async (value: AttributionConfig) => ({ ...value, version: 2 }))
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === 'attribution.connect')!.trigger('click'); await flushPromises()
    const policies = wrapper.findAllComponents(AttributionPolicyFields)
    expect(policies[0].props('candidates')).toEqual(['gpt-6-astra', 'gpt-6.1-sol'])
    policies[0].findComponent(AttributionExpectedModels).vm.$emit('update:modelValue', ['gpt-6-astra', 'gpt-6.1-sol'])
    policies[1].findComponent(AttributionExpectedModels).vm.$emit('update:modelValue', ['gpt-6.1-sol'])
    const initial = wrapper.get('[data-testid="initial-expected-models"]')
    await initial.get('select').setValue('custom')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenLastCalledWith(expect.objectContaining({
      default: expect.objectContaining({ expected_models: ['gpt-6-astra', 'gpt-6.1-sol'] }),
      groups: [expect.objectContaining({ expected_models: ['gpt-6.1-sol'] })],
      new_account_tests: expect.objectContaining({ attribution_expected_models: [] })
    }))
    await initial.get('select').setValue('inherit')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenLastCalledWith(expect.objectContaining({ new_account_tests: expect.objectContaining({ attribution_expected_models: null }) }))
    wrapper.unmount()
  })
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
    expect(wrapper.findAll('[data-testid="whitelist"]')).toHaveLength(3)
    await wrapper.get('select[aria-label="attribution.group"]').setValue(10)
    await wrapper.findAll('button').find(b => b.text() === 'attribution.addGroup')!.trigger('click')
    expect(wrapper.text()).toContain('Pro group'); expect(wrapper.text()).toContain('attribution.inherit')
    await wrapper.get('[data-testid="attribution-url"]').setValue('http://localhost:5000')
    await wrapper.get('[data-testid="attribution-enable"]').setValue(true)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(b => b.text() === 'attribution.connect')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="detector-version"]').text()).toContain('d53d3f5b1582')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ version: 1, enabled: true, detector, groups: [expect.objectContaining({ group_id: 10, enabled: false })] }))
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
  it('discards a connection response when its URL has changed', async () => {
    let resolve!: (value: unknown) => void
    api.connection.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const wrapper = mount(ModelAttributionView, { global: { stubs } }); await flushPromises()
    await wrapper.get('[data-testid="attribution-url"]').setValue('http://first:8080')
    await wrapper.findAll('button').find(b => b.text() === 'attribution.connect')!.trigger('click')
    await wrapper.get('[data-testid="attribution-url"]').setValue('http://second:8080')
    resolve({ models: ['gpt-6-astra'], detector }); await flushPromises()
    expect(wrapper.find('[data-testid="detector-version"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="detector-connection"]').text()).toBe('attribution.connectionRequired')
    wrapper.unmount()
  })
})
