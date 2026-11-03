import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import EventDetailDialog from '../components/EventDetailDialog.vue'
import type { PromptAuditEvent } from '../types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function keywordEvent(matchedKeyword?: string): PromptAuditEvent {
  return {
    id: 1, job_id: 1, decision: 'critical', risk_level: 'critical', action: 'Block',
    categories: ['keyword'], matched_scanners: ['keyword'], scanner_scores: { keyword: 1 },
    scanner_evidence: { keyword: 'sha256:stored-hash' }, matched_keyword: matchedKeyword,
    scanner_backend: 'local-keyword', scanner_version: '1', guard_endpoint_id: '',
    policy_id: 'keyword-substring', policy_version: 1, config_version: 1, chunk_total: 1, latency_ms: 5,
    issue_summaries: [{
      category: 'keyword', scanner_id: 'keyword', title: 'Keyword block', description: '',
      severity: 'critical', severity_label: '', action: 'Block', action_label: '',
      code: 'prompt_audit_keyword', score: 1, evidence: 'sha256:stored-hash', evidence_hash: 'stored-hash',
    }],
    created_at: '2026-11-03T00:00:00Z',
    snapshot: {
      request_id: 'req-keyword', user_id: 1, username: '', user_email: '', api_key_id: 1,
      api_key_name: '', group_id: 1, group_name: '', provider: 'openai', endpoint: '/v1/responses',
      protocol: 'responses', model: 'gpt-test', prompt_hash: 'prompt-hash',
      redacted_preview: 'stored prompt', full_prompt: 'stored prompt', prompt_length: 13,
      message_count: 1, stage: 'http',
    },
  }
}

function mountDetail(event: PromptAuditEvent) {
  return mount(EventDetailDialog, {
    props: { show: true, event, loading: false },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
  })
}

describe('Prompt audit keyword details', () => {
  it('shows the recorded triggering keyword as text in the summary, risks and structured result', async () => {
    const keyword = '<img src=x onerror=alert(1)> Admin Rule'
    const wrapper = mountDetail(keywordEvent(keyword))
    expect(wrapper.get('[data-test="summary-matched-keyword"]').text()).toBe(keyword)
    expect(wrapper.get('[data-test="summary-matched-keyword"]').isVisible()).toBe(true)
    await wrapper.get('[role="tablist"]').findAll('button')[1].trigger('click')
    expect(wrapper.get('[data-test="risk-matched-keyword"]').text()).toBe(keyword)
    expect(wrapper.get('[data-test="risk-matched-keyword"]').isVisible()).toBe(true)
    expect(wrapper.get('[data-test="risk-guard-return"] pre').text()).toContain(JSON.stringify(keyword))
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('[data-test="risk-issue"]').text()).toContain('sha256:stored-hash')
  })

  it('marks legacy records as not recorded and updates when another event opens', async () => {
    const wrapper = mountDetail(keywordEvent('previous keyword'))
    await wrapper.setProps({ event: { ...keywordEvent(), id: 2 } })
    expect(wrapper.get('[data-test="summary-matched-keyword"]').text()).toBe('admin.promptAudit.keyword.notRecorded')
    expect(wrapper.get('[data-test="risk-matched-keyword"]').text()).toBe('admin.promptAudit.keyword.notRecorded')
    expect(wrapper.text()).not.toContain('previous keyword')
    expect(wrapper.get('[data-test="risk-guard-return"] pre').text()).not.toContain('matched_keyword')
  })

  it('does not show keyword evidence on Guard-only events', () => {
    const wrapper = mountDetail({
      ...keywordEvent(), categories: ['pii'], matched_scanners: ['pii'], scanner_backend: 'qwen3guard-openai',
      issue_summaries: [{ ...keywordEvent().issue_summaries[0], category: 'pii', scanner_id: 'pii' }],
    })
    expect(wrapper.find('[data-test="summary-matched-keyword"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="risk-matched-keyword"]').exists()).toBe(false)
  })
})
