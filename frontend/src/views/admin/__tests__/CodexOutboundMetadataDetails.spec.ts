import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { FingerprintObservationEntry } from '@/api/admin/fingerprintObservations'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
import zh from '@/i18n/locales/zh/admin/fingerprintObservation'
import CodexOutboundMetadataDetails from '../components/CodexOutboundMetadataDetails.vue'

type RuntimeMessages = { [key: string]: RuntimeMessages | ((context: { named: (key: string) => unknown }) => string) }
function runtimeMessages(messages: Record<string, unknown>): RuntimeMessages {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string'
      ? (context: { named: (key: string) => unknown }) => value.replace(/\{(\w+)\}/g, (_match, name: string) => String(context.named(name)))
      : runtimeMessages(value as Record<string, unknown>),
  ]))
}

const observation: Partial<FingerprintObservationEntry> = {
  request_kind: 'compaction', history_ingest_requested: false,
  metadata_status: { request_kind: 'valid', history_ingest_requested: 'valid', compaction: 'valid', tool_namespaces_info: 'valid' },
  compaction: { trigger: 'auto', reason: 'token_limit', implementation: 'remote', phase: 'before', strategy: 'summarize' },
  tool_namespaces_info: [{
    namespace: 'browser_key', name: 'browser_tools', functions: [
      { function: 'search_key', name: 'search_tools', direct: false, deferred: true, code_mode_name: 'browser.search', source: { kind: 'mcp', server_name: 'search-server' } },
      { function: 'navigate_key', name: 'navigate', direct: true, deferred: false, source: { kind: 'harness' } },
    ],
  }],
}

const mounted: ReturnType<typeof mount>[] = []
function mountMetadata(entry: Partial<FingerprintObservationEntry> = observation, locale = 'en') {
  const wrapper = mount(CodexOutboundMetadataDetails, {
    props: { observation: entry as FingerprintObservationEntry },
    global: { plugins: [createI18n({ legacy: false, locale, messages: {
      en: runtimeMessages({ admin: en }), zh: runtimeMessages({ admin: zh }),
    } })] },
  })
  mounted.push(wrapper)
  return wrapper
}
async function expand(wrapper: ReturnType<typeof mountMetadata>, testID: string) {
  const details = wrapper.get(`[data-testid="${testID}"]`)
  ;(details.element as HTMLDetailsElement).open = true
  await details.trigger('toggle')
  return details
}
function fieldValue(wrapper: ReturnType<typeof mountMetadata>, label: string): string | undefined {
  return wrapper.findAll('dl > div').find(field => field.get('dt').text() === label)?.get('dd').text()
}
afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount() })

describe('CodexOutboundMetadataDetails', () => {
  it.each(['new', 'cleared', 'cold_resume', 'warm_fork', 'cold_fork', 'supplied_history'] as const)(
    'shows the observed %s history state and preserves explicit false boolean metadata',
    historyInitialization => {
      const wrapper = mountMetadata({
        history_initialization: historyInitialization,
        guardian_recorded_items: false,
        analytics_enabled: true,
        metadata_status: { history_initialization: 'valid', guardian_recorded_items: 'valid', analytics_enabled: 'valid' },
      })
      expect(fieldValue(wrapper, 'History initialization')).toBe(historyInitialization)
      expect(fieldValue(wrapper, 'Guardian recorded items')).toBe('false')
      expect(fieldValue(wrapper, 'Analytics enabled')).toBe('true')
    },
  )

  it('distinguishes missing typed metadata from legacy observations and invalid values', async () => {
    const wrapper = mountMetadata({ agent_name: 'guardian', subagent_kind: 'guardian' })
    for (const label of ['History initialization', 'Guardian recorded items', 'Analytics enabled']) {
      expect(fieldValue(wrapper, label)).toBe('Not collected')
    }
    await wrapper.setProps({ observation: {
      metadata_status: { history_initialization: 'missing', guardian_recorded_items: 'missing', analytics_enabled: 'missing' },
    } as FingerprintObservationEntry })
    for (const label of ['History initialization', 'Guardian recorded items', 'Analytics enabled']) {
      expect(fieldValue(wrapper, label)).toBe('Not provided')
    }
    await wrapper.setProps({ observation: {
      history_initialization: '<unsafe>', guardian_recorded_items: 'false', analytics_enabled: 'true',
      metadata_status: { history_initialization: 'valid', guardian_recorded_items: 'valid', analytics_enabled: 'invalid' },
    } as unknown as FingerprintObservationEntry })
    for (const label of ['History initialization', 'Guardian recorded items', 'Analytics enabled']) {
      expect(fieldValue(wrapper, label)).toBe('Unable to parse')
    }
    expect(wrapper.text()).not.toContain('<unsafe>')
    await wrapper.setProps({ observation: {
      guardian_recorded_items: true, analytics_enabled: false,
      metadata_status: { guardian_recorded_items: 'valid', analytics_enabled: 'valid' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'Guardian recorded items')).toBe('true')
    expect(fieldValue(wrapper, 'Analytics enabled')).toBe('false')
  })

  it('shows only the bounded MCP attribution summary and distinguishes oversized and invalid carriers', async () => {
    const wrapper = mountMetadata({})
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Not collected')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: false, type: 'missing', status: 'missing' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Absent · Missing · Not provided')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: true, type: 'string', bytes: 42, status: 'valid', reported_status: 'complete', content: 'private server content' },
    } as unknown as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Present · String · 42 bytes · Recorded · Reported status: complete')
    expect(wrapper.text()).not.toContain('private server content')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: true, type: 'string', bytes: 16385, status: 'oversized' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Present · String · >16384 bytes · Exceeds 16 KiB')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: true, type: 'object', status: 'invalid' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Present · Object · Unable to parse')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: false, type: 'unavailable', status: 'invalid' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Absent · Unavailable · Unable to parse')
    await wrapper.setProps({ observation: {
      mcp_attribution: { present: true, type: 'string', bytes: 80, status: 'valid', reported_status: 'attribution_error', error_reason: 'private error' },
    } as unknown as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'MCP attribution')).toBe('Present · String · 80 bytes · Recorded · Reported status: attribution_error')
    expect(wrapper.text()).not.toContain('private error')
  })

  it('shows prompt cache absence, session matching and overrides without exposing the key', async () => {
    const wrapper = mountMetadata({})
    expect(fieldValue(wrapper, 'Prompt cache key')).toBe('Not collected')
    for (const [present, strategy, expected] of [
      [false, 'absent', 'Absent · Not provided'],
      [true, 'session', 'Present · Matches outbound session'],
      [true, 'override', 'Present · Explicit override'],
      [true, 'invalid', 'Present · Invalid value'],
    ] as const) {
      await wrapper.setProps({ observation: {
        prompt_cache_key: { present, strategy, value: 'private-cache-key' },
      } as unknown as FingerprintObservationEntry })
      expect(fieldValue(wrapper, 'Prompt cache key')).toBe(expected)
      expect(wrapper.text()).not.toContain('private-cache-key')
    }
  })

  it('keeps request flags compact and expands all observed tool and compaction fields only on demand', async () => {
    const wrapper = mountMetadata()
    expect(fieldValue(wrapper, 'Request kind')).toBe('compaction')
    expect(fieldValue(wrapper, 'History ingest requested')).toBe('false')
    expect(wrapper.text()).toContain('1 namespaces · 2 functions')
    expect(wrapper.text()).not.toContain('token_limit')
    expect(wrapper.text()).not.toContain('search-server')
    expect(wrapper.text()).not.toContain('browser_tools')
    await expand(wrapper, 'codex-compaction')
    for (const [label, value] of [['Trigger', 'auto'], ['Reason', 'token_limit'], ['Implementation', 'remote'], ['Phase', 'before'], ['Strategy', 'summarize']]) {
      expect(fieldValue(wrapper, label!)).toBe(value)
    }
    await expand(wrapper, 'codex-tool-namespaces')
    for (const value of ['browser_key', 'browser_tools', 'search_key', 'search_tools', 'browser.search', 'mcp', 'search-server', 'navigate_key', 'navigate', 'harness']) {
      expect(wrapper.text()).toContain(value)
    }
    expect(fieldValue(wrapper, 'Direct')).toBe('false')
    expect(fieldValue(wrapper, 'Deferred')).toBe('true')
    expect(wrapper.findAll('dt').filter(term => term.text() === 'MCP server name')).toHaveLength(1)
  })

  it('distinguishes old observations, absent fields and malformed fields without showing invalid payloads', async () => {
    const wrapper = mountMetadata({})
    expect(fieldValue(wrapper, 'History ingest requested')).toBe('Not collected')
    expect(wrapper.findAll('summary').every(summary => summary.text().includes('Not collected'))).toBe(true)
    await wrapper.setProps({ observation: {
      ...observation,
      metadata_status: { request_kind: 'missing', history_ingest_requested: 'missing', compaction: 'invalid', tool_namespaces_info: 'invalid' },
    } as FingerprintObservationEntry })
    expect(fieldValue(wrapper, 'Request kind')).toBe('Not provided')
    expect(fieldValue(wrapper, 'History ingest requested')).toBe('Not provided')
    await expand(wrapper, 'codex-compaction')
    await expand(wrapper, 'codex-tool-namespaces')
    expect(wrapper.text()).toContain('Unable to parse')
    expect(wrapper.text()).not.toContain('token_limit')
    expect(wrapper.text()).not.toContain('browser_tools')
  })

  it('labels truncation before expansion and explains limits without claiming a complete list', async () => {
    const wrapper = mountMetadata({ ...observation, metadata_status: { ...observation.metadata_status!, tool_namespaces_info: 'truncated' } })
    expect(wrapper.get('[data-testid="codex-tool-namespaces"] summary').text()).toContain('Truncated')
    expect(wrapper.text()).not.toContain('This list is incomplete')
    await expand(wrapper, 'codex-tool-namespaces')
    expect(wrapper.text()).toContain('Retains at most 64 namespaces and 256 functions in total; name fields are limited to 256 characters. This list is incomplete.')
    expect(wrapper.text()).toContain('search_key')
    await wrapper.setProps({ observation: { ...observation, metadata_status: { ...observation.metadata_status!, tool_namespaces_info: 'truncated' }, tool_namespaces_info: [{ namespace: 'limited', name: 'limited', functions: [] }] } as FingerprintObservationEntry })
    expect(wrapper.text()).toContain('No functions recorded; the truncated list cannot confirm whether this namespace is empty')
    expect(wrapper.text()).not.toContain('No functions in this namespace')
  })

  it('shows a valid empty tool list separately from missing metadata and supports empty namespaces', async () => {
    const wrapper = mountMetadata({ ...observation, tool_namespaces_info: undefined })
    expect(wrapper.text()).toContain('0 namespaces · 0 functions')
    await expand(wrapper, 'codex-tool-namespaces')
    expect(wrapper.text()).toContain('An empty tool namespace list was provided')
    await wrapper.setProps({ observation: { ...observation, tool_namespaces_info: [{ namespace: 'empty', name: 'empty', functions: [] }] } as FingerprintObservationEntry })
    expect(wrapper.text()).toContain('No functions in this namespace')
  })

  it('renders names as escaped text with wrapping containers on narrow layouts', async () => {
    const unsafeName = '<img src=x onerror="alert(1)">' + 'longname'.repeat(28)
    const wrapper = mountMetadata({ ...observation, tool_namespaces_info: [{ namespace: 'ns', name: unsafeName, functions: [] }] })
    await expand(wrapper, 'codex-tool-namespaces')
    expect(wrapper.find('img').exists()).toBe(false)
    const value = wrapper.findAll('dd').find(field => field.text() === unsafeName)!
    expect(value.classes()).toContain('break-all')
    expect(value.element.parentElement?.classList.contains('min-w-0')).toBe(true)
    expect(wrapper.findAll('dl').every(list => list.classes().includes('min-w-0'))).toBe(true)
  })

  it('provides complete Chinese metadata labels and summaries', async () => {
    const wrapper = mountMetadata({
      ...observation,
      history_initialization: 'warm_fork', guardian_recorded_items: false, analytics_enabled: true,
      metadata_status: { ...observation.metadata_status, history_initialization: 'valid', guardian_recorded_items: 'valid', analytics_enabled: 'valid' },
      mcp_attribution: { present: true, type: 'string', bytes: 42, status: 'valid' },
      prompt_cache_key: { present: true, strategy: 'override' },
    }, 'zh')
    expect(wrapper.text()).toContain('请求与工具元数据')
    expect(wrapper.text()).toContain('1 个命名空间 · 2 个函数')
    await expand(wrapper, 'codex-compaction')
    await expand(wrapper, 'codex-tool-namespaces')
    expect(wrapper.text()).toContain('MCP 服务名称')
    expect(fieldValue(wrapper, '请求历史导入')).toBe('false')
    expect(fieldValue(wrapper, '历史初始化')).toBe('warm_fork')
    expect(fieldValue(wrapper, 'Guardian 已记录条目')).toBe('false')
    expect(fieldValue(wrapper, '分析已启用')).toBe('true')
    expect(fieldValue(wrapper, 'MCP 归属')).toBe('存在 · 字符串 · 42 字节 · 已记录')
    expect(fieldValue(wrapper, '提示缓存键')).toBe('存在 · 显式覆盖')
    expect(wrapper.text()).not.toContain('admin.fingerprintObservation')
  })
})
