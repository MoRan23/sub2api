import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import EnforcementStatus from '../components/EnforcementStatus.vue'
import type { PromptAuditEvent, PromptEnforcementOutcome } from '../types'
import english from '@/i18n/locales/en/admin/promptAudit'
import chinese from '@/i18n/locales/zh/admin/promptAudit'

afterEach(cleanup)

function event(enforcement?: PromptEnforcementOutcome | null): PromptAuditEvent {
  return {
    id: 1, job_id: 1, decision: 'critical', risk_level: 'critical', action: 'Block',
    categories: ['keyword'], matched_scanners: ['keyword'], scanner_scores: {}, scanner_evidence: {},
    keyword_kind: 'jailbreak', enforcement, scanner_backend: 'local-keyword', scanner_version: '1',
    guard_endpoint_id: '', policy_id: 'keyword', policy_version: 1, config_version: 1,
    chunk_total: 1, latency_ms: 1, issue_summaries: [], created_at: '2026-11-11T00:00:00Z',
    snapshot: { request_id: 'request', user_id: 42, username: '', user_email: '', api_key_id: 3,
      api_key_name: '', group_name: '', provider: 'openai', endpoint: '/v1/responses', protocol: 'responses',
      model: 'test', prompt_hash: '', redacted_preview: '', full_prompt: '', prompt_length: 0, message_count: 1, stage: 'http' },
  }
}

function state(overrides: Partial<PromptEnforcementOutcome> = {}): PromptEnforcementOutcome {
  return { recorded: true, counted: true, kind: 'jailbreak', violation_count: 10, window_hours: 24,
    ban_threshold: 10, auto_banned: true, epoch: 2, email_status: 'sent', user_status: 'disabled', can_unban: true, ...overrides }
}

function display(value: PromptAuditEvent, props = {}) {
  return render(EnforcementStatus, { props: { event: value, ...props }, global: {
    plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { admin: english } } })],
  } })
}

describe('Prompt audit enforcement details', () => {
  it('shows missing historical enforcement without inferring an action', () => {
    display(event())
    expect(screen.getByText('Not recorded')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByText('Recorded violation count')).toBeNull()
  })

  it('shows other keyword classification even when no enforcement was recorded', () => {
    display({ ...event(), keyword_kind: 'other' }, { compact: true })
    expect(screen.getByText('Other keyword')).toBeTruthy()
    expect(screen.getByText('Not recorded')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders compact Chinese status with recorded counts and current unban action', async () => {
    const view = render(EnforcementStatus, { props: { event: event(state()), compact: true }, global: {
      plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh: { admin: chinese } } })],
    } })
    expect(screen.getByText('当时 10 / 10 次 · 24 小时内')).toBeTruthy()
    expect(screen.getByText('已停用')).toBeTruthy()
    expect(screen.queryByText('统计周期')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: '快捷解封' }))
    expect(view.emitted().unban).toEqual([[42]])
  })

  it('shows frozen counts and current state, then emits the exact user to unban', async () => {
    const view = display(event(state()))
    expect(screen.getByText('Jailbreak')).toBeTruthy()
    expect(screen.getByText('24 hours')).toBeTruthy()
    expect(screen.getByText('Disabled')).toBeTruthy()
    expect(screen.getByText('Sent')).toBeTruthy()
    expect(screen.getByText(/starts a new counting cycle/)).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Unban user' }))
    expect(view.emitted().unban).toEqual([[42]])
    await view.rerender({ event: event(state({ user_status: 'active', can_unban: false })) })
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.getByText('Active')).toBeTruthy()
    expect(screen.getByText('Automatically banned by this event')).toBeTruthy()
  })

  it.each(['not_requested', 'queued', 'sending', 'retry', 'sent', 'failed', 'cancelled'])(
    'translates email status %s', (emailStatus) => {
      display(event(state({ email_status: emailStatus })))
      expect(screen.getByText(english.promptAudit.enforcement.emailStatuses[emailStatus as keyof typeof english.promptAudit.enforcement.emailStatuses])).toBeTruthy()
    },
  )

  it.each([
    { can_unban: false }, { user_status: 'active' }, { user_status: 'deleted' },
  ])('only offers unban with explicit permission and a disabled user: %j', overrides => {
    display(event(state(overrides)))
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('requires an existing positive user ID and handles busy and disabled controls', async () => {
    const value = event(state())
    const view = display(value, { unbanning: true })
    const busy = screen.getByRole('button', { name: 'Unbanning…' }) as HTMLButtonElement
    expect(busy.disabled).toBe(true)
    expect(busy.getAttribute('aria-busy')).toBe('true')
    await fireEvent.click(busy)
    expect(view.emitted().unban).toBeUndefined()
    await view.rerender({ unbanning: false, disabled: true })
    expect((screen.getByRole('button') as HTMLButtonElement).disabled).toBe(true)
    await view.rerender({ event: { ...value, snapshot: { ...value.snapshot, user_id: 0 } }, disabled: false })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('uses safe text for unknown states and does not carry evidence between events', async () => {
    const view = display({ ...event(state({ email_status: '<img src=x>', reason: '<script>alert(1)</script>' })), keyword_kind: 'other' })
    expect(screen.getByText('Unknown (<img src=x>)')).toBeTruthy()
    expect(screen.getByText('Other keyword')).toBeTruthy()
    expect(view.container.querySelector('img, script')).toBeNull()
    await view.rerender({ event: event(null) })
    expect(screen.getByText('Not recorded')).toBeTruthy()
    expect(screen.queryByText('Other keyword')).toBeNull()
    expect(screen.queryByText('Unknown (<img src=x>)')).toBeNull()
  })
})
