import { cleanup, render, screen, within } from '@testing-library/vue'
import { afterEach, describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { AttributionJob } from '@/api/admin/modelAttribution'
import type { CandyTestExecution, CandyTestItem } from '@/api/admin/candyTests'
import { attributionEn, attributionZh } from '@/i18n/locales/modelAttribution'
import { candyTestsEn, candyTestsZh } from '@/i18n/locales/candyTests'
import { diagnosticDaybreakEn, diagnosticDaybreakZh } from '@/i18n/locales/diagnosticDaybreak'
import AttributionResult from '../AttributionResult.vue'
import AccountCandyTestResult from '../AccountCandyTestResult.vue'
import DiagnosticDaybreakEvidence from '../DiagnosticDaybreakEvidence.vue'

function i18n(locale = 'zh') {
  return createI18n({
    legacy: false,
    locale,
    // The app aliases vue-i18n to its runtime-only build. Supply the small
    // message formatter used by these fixtures instead of a production compiler.
    messageCompiler: (message) => (context) => String(message).replace(/\{(\w+)\}/g, (_, key: string) => String(context.named(key))),
    messages: {
      zh: { attribution: attributionZh, candyTests: candyTestsZh, diagnosticDaybreak: diagnosticDaybreakZh },
      en: { attribution: attributionEn, candyTests: candyTestsEn, diagnosticDaybreak: diagnosticDaybreakEn },
    },
  })
}

function attributionJob(): AttributionJob {
  return {
    id: 1, account_id: 42, account_name: 'Synthetic account', source: 'manual', status: 'passed',
    snapshot: {
      version: 3, config_version: 1, group_id: 0,
      policy: { model: 'gpt-6-astra', high_models: ['gpt-6-astra'], low_models: ['gpt-6-luna'] },
      daybreak: { blue: true, red: true },
    },
    result: { duration_ms: 1200, action: 'awaiting_confirmation', pass_streak: 1, completed_probes: 3 },
    created_at: '2026-10-16T00:00:00Z', finished_at: '2026-10-16T00:00:01Z',
  }
}

function pelicanItem(daybreak?: CandyTestExecution['daybreak']): CandyTestItem {
  return {
    id: 2, account_id: 42, account_name: 'Synthetic account', batch_id: 'batch-a', status: 'generated',
    model: 'gpt-6-astra', reasoning_effort: 'high', prompt_version: 'pelican-v1', cancel_requested: false,
    execution: {
      requested_model: 'gpt-6-astra', actual_model: 'gpt-6-astra', upstream_model: 'gpt-6-astra',
      reasoning_effort: 'high', model_conflict: false, model_evidence_source: 'response',
      completed: true, duration_ms: 1200, daybreak,
    },
    created_at: '2026-10-16T00:00:00Z', started_at: '2026-10-16T00:00:00Z', finished_at: '2026-10-16T00:00:01Z',
  }
}

afterEach(cleanup)

describe('diagnostic Daybreak evidence', () => {
  it('shows a recorded absence and its reason instead of implying that evidence is missing', () => {
    render(DiagnosticDaybreakEvidence, { props: { evidence: { cyber: '', reason: 'catalog_unavailable' } }, global: { plugins: [i18n()] } })
    expect(screen.getByText('未添加')).toBeTruthy()
    expect(screen.getByText(/无法确认上游模型目录/)).toBeTruthy()
    expect(screen.queryByText('未记录')).toBeNull()
  })

  it('keeps unknown reason codes visible for newer server versions', () => {
    render(DiagnosticDaybreakEvidence, { props: { evidence: { cyber: '', reason: 'new_server_reason' } }, global: { plugins: [i18n('en')] } })
    expect(screen.getByText('Not added')).toBeTruthy()
    expect(screen.getByText(/new_server_reason/)).toBeTruthy()
  })

  it('shows frozen account preferences separately from each successful probe’s actual field', () => {
    const job = attributionJob()
    job.result.daybreak = [
      { cyber: 'daybreak_blue', reason: 'automatic' },
      { cyber: '', reason: 'capability_unavailable' },
      null,
    ]
    render(AttributionResult, { props: { job }, global: { plugins: [i18n()] } })
    expect(screen.getByText('Blue: 开启 / Red: 开启')).toBeTruthy()
    const evidence = within(screen.getByRole('region', { name: 'Daybreak 出站证据' }))
    expect(evidence.getByText('daybreak_blue')).toBeTruthy()
    expect(evidence.getByText('未添加')).toBeTruthy()
    expect(evidence.getByText(/模型没有所需字段的有效能力证据/)).toBeTruthy()
    expect(evidence.getByText('未记录')).toBeTruthy()
    expect(evidence.getByText('#3')).toBeTruthy()
  })

  it('does not infer legacy attribution evidence from account preferences or probe model', () => {
    const job = attributionJob()
    delete job.snapshot.version
    delete job.snapshot.daybreak
    render(AttributionResult, { props: { job }, global: { plugins: [i18n()] } })
    const evidence = within(screen.getByRole('region', { name: 'Daybreak 出站证据' }))
    expect(evidence.getByText('未记录')).toBeTruthy()
    expect(screen.queryByText('daybreak_blue')).toBeNull()
    expect(screen.queryByText('账号 Daybreak 偏好（任务快照）')).toBeNull()
  })

  it.each([
    ['daybreak_preferences_changed', 'passed', '账号 Daybreak 偏好已变化'],
    ['daybreak_conditions_mixed', 'mismatch', '三条成功探针的 Daybreak 测试条件不一致'],
    ['daybreak_evidence_missing', 'passed', '缺少成功探针的 Daybreak 出站证据'],
  ] as const)('preserves the verdict while explaining %s does not change allowlists', (reason, status, text) => {
    const job = attributionJob()
    job.reason = reason
    job.status = status
    job.result.action = 'stale_policy'
    job.result.pass_streak = 0
    render(AttributionResult, { props: { job }, global: { plugins: [i18n()] } })
    expect(screen.getByText(status === 'passed' ? '通过' : '不通过')).toBeTruthy()
    expect(screen.getByText(new RegExp(text))).toBeTruthy()
    expect(screen.getByTestId('attribution-result-action').textContent).toBe(attributionZh.actions.stale_policy)
    expect(screen.getByTestId('attribution-pass-streak').textContent).toBe('0')
  })

  it('shows the pelican successful execution field and updates to an omitted-field reason', async () => {
    const { rerender } = render(AccountCandyTestResult, {
      props: { item: pelicanItem({ cyber: 'daybreak_red', reason: 'automatic' }) },
      global: { plugins: [i18n('en')] },
    })
    expect(screen.getByText('daybreak_red')).toBeTruthy()
    await rerender({ item: pelicanItem({ cyber: '', reason: 'red_disabled' }) })
    expect(screen.queryByText('daybreak_red')).toBeNull()
    expect(screen.getByText('Not added')).toBeTruthy()
    expect(screen.getByText(/This model requires the account Red preference/)).toBeTruthy()
  })

  it('shows legacy pelican evidence as not recorded', () => {
    render(AccountCandyTestResult, { props: { item: pelicanItem() }, global: { plugins: [i18n()] } })
    expect(screen.getByText('未记录')).toBeTruthy()
    expect(screen.queryByText('未添加')).toBeNull()
  })
})
