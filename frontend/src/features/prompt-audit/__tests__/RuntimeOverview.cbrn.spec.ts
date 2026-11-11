import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RuntimeOverview from '../components/RuntimeOverview.vue'
import type { PromptAuditRuntime } from '../types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: { value: 'en' }, t: (key: string) => key }) }))

const runtime = (): PromptAuditRuntime => ({
  process_status: 'disabled', effective_mode: 'off', keyword_blocking_active: false,
  expected_config_version: 7, active_config_version: 7, worker_total: 4, worker_active: 0, queue_capacity: 100,
  queue: { staging: 0, queued: 0, processing: 0, retry: 0, done: 5, failed: 0, active: 0 },
  processed_total: 5, failed_total: 0, enqueued_total: 5, dropped_total: 0, database_status: 'ok', redis_status: 'ok', endpoints: {},
  guard_metrics: { total: 1, allowed: 1, flagged: 0, blocked: 0, keyword_blocked: 0, unavailable: 0, invalid: 0, timeouts: 0, failovers: 0, bulkhead_full: 0, record_failed: 0 },
})

describe('CBRN runtime observation', () => {
  it('shows independent effective CBRN status and counter, with missing fields marked unrecorded', async () => {
    const wrapper = mount(RuntimeOverview, { props: { runtime: runtime(), loading: false, error: '' } })
    const status = () => wrapper.findAll('dl > div').find((entry) => entry.text().includes('admin.promptAudit.runtime.cbrnBlocking'))!
    expect(status().text()).toContain('admin.promptAudit.keyword.notRecorded')
    const metric = () => wrapper.findAll('p').find((entry) => entry.text() === 'admin.promptAudit.metrics.cbrnBlocked')!.element.parentElement!.textContent
    expect(metric()).toContain('—')
    await wrapper.setProps({ runtime: { ...runtime(), cbrn_blocking_active: true, guard_metrics: { ...runtime().guard_metrics, cbrn_blocked: 12 } } })
    expect(status().text()).toContain('admin.promptAudit.status.running')
    expect(metric()).toContain('12')
    await wrapper.setProps({ runtime: { ...runtime(), cbrn_blocking_active: false } })
    expect(status().text()).toContain('admin.promptAudit.status.disabled')
  })
})
