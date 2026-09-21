import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { CodexTelemetryEntry, CodexTelemetryObservationsResponse } from '@/api/admin/codexTelemetry'
import zh from '@/i18n/locales/zh/admin/fingerprintObservation'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
import CodexTelemetryObservations from '../components/CodexTelemetryObservations.vue'

const { list } = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { codexTelemetry: { list } } }))
vi.mock('@/utils/apiError', () => ({ extractApiErrorMessage: (_error: unknown, fallback: string) => fallback }))

const entry: CodexTelemetryEntry = {
  id: 1, created_at: '2026-09-16T10:00:00Z', updated_at: '2026-09-16T10:00:01Z',
  account_id: 42, account_name: 'OAuth account', type: 'analytics', status: 'sent',
  event_names: ['codex.thread.started', 'codex.turn.completed'], contains_simulated: true,
  attempt_id: 12, attempt_count: 2, turn_count: 1, session_id: 'root-session-uuid', thread_id: 'actual-thread-uuid',
  turn_id: 'actual-turn-uuid', parent_thread_id: 'parent-thread-uuid', parent_turn_id: 'parent-turn-uuid',
  root_turn_id: 'root-turn-uuid', model: 'gpt-5', user_agent: 'codex-cli/1.0', originator: 'codex-cli',
  version: '1.0', http_status: 200, error: '',
}

function response(overrides: Partial<CodexTelemetryObservationsResponse> = {}): CodexTelemetryObservationsResponse {
  return {
    configured_enabled: true, effective_enabled: true, forced_off_reason: '', queue_depth: 0,
    counters: { attempts: 12, queued: 10, sent: 8, failed: 1, dropped: 1, cancelled: 0, skipped: 0 },
    items: [entry], total: 1, page: 1, page_size: 20, ...overrides,
  }
}

const mounted: ReturnType<typeof mount>[] = []
type RuntimeMessages = { [key: string]: RuntimeMessages | ((context: { named: (key: string) => unknown }) => string) }
function runtimeMessages(messages: Record<string, unknown>): RuntimeMessages {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string'
      ? (context: { named: (key: string) => unknown }) => value.replace(/\{(\w+)\}/g, (_match, name: string) => String(context.named(name)))
      : runtimeMessages(value as Record<string, unknown>),
  ]))
}
function mountPanel(locale = 'zh') {
  const wrapper = mount(CodexTelemetryObservations, {
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: {
        zh: runtimeMessages({ admin: zh, common: { status: '状态', filter: '筛选', retry: '重试', loading: '加载中' } }),
        en: runtimeMessages({ admin: en, common: { status: 'Status', filter: 'Filter', retry: 'Retry', loading: 'Loading' } }),
      } })],
      stubs: {
        Icon: true,
        Pagination: {
          props: ['page', 'pageSize', 'total'], emits: ['update:page', 'update:pageSize'],
          template: '<nav><span>{{ page }}</span><button data-next @click="$emit(\'update:page\', 2)">Next</button><button data-size @click="$emit(\'update:pageSize\', 50)">50</button></nav>',
        },
      },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

async function expand(wrapper: ReturnType<typeof mountPanel>, index = 0) {
  const details = wrapper.findAll('details')[index]!
  ;(details.element as HTMLDetailsElement).open = true
  await details.trigger('toggle')
}

describe('CodexTelemetryObservations', () => {
  beforeEach(() => { list.mockReset(); list.mockResolvedValue(response()) })
  afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount() })

  it('shows compact delivery summaries and reveals actual lineage only on expansion', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.text()).toContain('包含模拟事件')
    expect(wrapper.text()).toContain('发送成功')
    expect(wrapper.text()).toContain('OAuth account')
    expect(wrapper.text()).not.toContain(entry.session_id)
    expect(wrapper.text()).not.toContain(entry.event_names[0])
    await expand(wrapper)
    const fields = new Map(wrapper.findAll('dl > div').map((field) => [field.get('dt').text(), field.get('dd').text()]))
    expect(fields.get('会话')).toBe(entry.session_id)
    expect(fields.get('父线程')).toBe(entry.parent_thread_id)
    expect(fields.get('父 Turn')).toBe(entry.parent_turn_id)
    expect(fields.get('根 Turn')).toBe(entry.root_turn_id)
    expect(wrapper.text()).toContain(entry.event_names[0])
    expect(wrapper.emitted('stateChanged')?.at(-1)).toEqual([{ loading: false, paused: true }])
  })

  it('labels metrics as aggregated and never shows a single-turn identity even if one is supplied', async () => {
    list.mockResolvedValue(response({ items: [{ ...entry, type: 'metrics', turn_count: 3, event_names: ['codex_thread_initialized'], is_worktree: null }] }))
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.text()).toContain('多个 turn（3）')
    await expand(wrapper)
    expect(wrapper.text()).toContain('此批次不归属于某一个 session')
    expect(wrapper.text()).not.toContain(entry.session_id)
    expect(wrapper.text()).not.toContain(entry.turn_id)
    expect(wrapper.text()).not.toContain('工作树状态')
  })

  it.each([
    ['zh', null, '工作树状态', '未知'],
    ['zh', undefined, '工作树状态', '未采集'],
    ['en', null, 'Worktree status', 'Unknown'],
    ['en', undefined, 'Worktree status', 'Not collected'],
  ] as const)('distinguishes unknown worktree from legacy missing values (%s, %s)', async (locale, worktree, label, expected) => {
    list.mockResolvedValue(response({ items: [{ ...entry, event_names: ['codex_thread_initialized', 'codex_turn_event'], ...(worktree === undefined ? {} : { is_worktree: worktree }) }] }))
    const wrapper = mountPanel(locale)
    await flushPromises()
    expect(wrapper.text()).not.toContain(label)
    await expand(wrapper)
    const field = wrapper.findAll('dl > div').find(field => field.get('dt').text() === label)
    expect(field?.get('dd').text()).toBe(expected)
    expect(wrapper.text()).not.toContain('admin.fingerprintObservation')
  })

  it('does not attach a worktree state to analytics without thread initialization', async () => {
    list.mockResolvedValue(response({ items: [{ ...entry, is_worktree: null }] }))
    const wrapper = mountPanel()
    await flushPromises()
    await expand(wrapper)
    expect(wrapper.text()).not.toContain('工作树状态')
  })

  it('shows configured and effective state separately when the environment forces it off', async () => {
    list.mockResolvedValue(response({ effective_enabled: false, forced_off_reason: 'CODEX_TELEMETRY_ENABLED=false' }))
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.text()).toContain('配置：已开启')
    expect(wrapper.text()).toContain('遥测：已关闭')
    expect(wrapper.text()).toContain('CODEX_TELEMETRY_ENABLED=false')
    expect(wrapper.text()).toContain('OAuth account')
  })

  it('loads retained records when disabled and renders a normal empty state', async () => {
    list.mockResolvedValue(response({ configured_enabled: false, effective_enabled: false, items: [], total: 0 }))
    const wrapper = mountPanel()
    await flushPromises()
    expect(list).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('配置：已关闭')
    expect(wrapper.text()).toContain('暂无遥测发送记录')
  })

  it('filters independently and keeps filters when changing pages and page size', async () => {
    list.mockImplementation(async (params) => response({ page: params.page, page_size: params.page_size, total: 70 }))
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.get('input').setValue('42')
    const selects = wrapper.findAll('select')
    await selects[0]!.setValue('failed')
    await selects[1]!.setValue('metrics')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ account_id: 42, status: 'failed', type: 'metrics', page: 1, page_size: 20 }, { signal: expect.any(AbortSignal) })
    await wrapper.get('[data-next]').trigger('click')
    await flushPromises()
    expect(list.mock.lastCall?.[0]).toEqual({ account_id: 42, status: 'failed', type: 'metrics', page: 2, page_size: 20 })
    expect(wrapper.emitted('stateChanged')?.at(-1)).toEqual([{ loading: false, paused: true }])
    await wrapper.get('[data-size]').trigger('click')
    await flushPromises()
    expect(list.mock.lastCall?.[0]).toEqual({ account_id: 42, status: 'failed', type: 'metrics', page: 1, page_size: 50 })
    expect(wrapper.emitted('stateChanged')?.at(-1)).toEqual([{ loading: false, paused: false }])
  })

  it('validates the account filter without issuing a request', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.get('input').setValue('-2')
    await wrapper.get('form').trigger('submit')
    expect(list).toHaveBeenCalledOnce()
    expect(wrapper.get('[role="alert"]').text()).toContain('正整数账号 ID')
  })

  it('retries an error without a blank page', async () => {
    list.mockRejectedValueOnce(new Error('temporary network failure'))
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('加载遥测发送记录失败')
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('OAuth account')
  })

  it('aborts stale filter requests and does not overwrite a newer result', async () => {
    let resolveOld!: (value: CodexTelemetryObservationsResponse) => void
    list.mockReturnValueOnce(new Promise((resolve) => { resolveOld = resolve }))
    const wrapper = mountPanel()
    const firstSignal = list.mock.calls[0]![1].signal as AbortSignal
    await wrapper.get('input').setValue('99')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(firstSignal.aborted).toBe(true)
    resolveOld(response({ items: [{ ...entry, account_name: 'stale account' }] }))
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale account')
  })

  it('uses complete English telemetry labels', async () => {
    const wrapper = mountPanel('en')
    await flushPromises()
    await expand(wrapper)
    expect(wrapper.text()).toContain('Includes simulated events')
    expect(wrapper.text()).toContain('Parent thread')
    expect(wrapper.text()).not.toContain('admin.fingerprintObservation')
  })

  it('shows OS, actual provenance, unknown delivery and field sources without implying simulation for observed batches', async () => {
    list.mockResolvedValue(response({ simulation_enabled: false, observation_enabled: true, items: [{
      ...entry, contains_simulated: false, source: 'observed', os_family: 'macos', status: 'unknown',
      pool_id: 'pool-uuid', batch_id: 'batch-uuid', reasons: ['unknown_os'], error: 'delivery_unknown',
      field_sources: { response_model: 'observed', sandbox_policy: 'simulated' },
    }] }))
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.text()).toContain('仅真实观测')
    expect(wrapper.text()).toContain('macOS')
    expect(wrapper.text()).toContain('发送结果未知')
    expect(wrapper.text()).toContain('不会盲目重发')
    expect(wrapper.text()).not.toContain('包含模拟事件')
    await expand(wrapper)
    expect(wrapper.text()).toContain('pool-uuid')
    expect(wrapper.text()).toContain('batch-uuid')
    expect(wrapper.text()).toContain('response_model')
    expect(wrapper.text()).toContain('字段来源')
    expect(wrapper.text()).toContain('无法可靠识别系统')
  })

  it('applies system and source filters and retains them during refresh', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.get('select[aria-label="系统"]').setValue('linux')
    await wrapper.get('select[aria-label="数据来源"]').setValue('mixed')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(list.mock.lastCall?.[0]).toMatchObject({ os_family: 'linux', source: 'mixed', page: 1 })
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    expect(list.mock.lastCall?.[0]).toMatchObject({ os_family: 'linux', source: 'mixed' })
  })
})
