import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { CodexResponseEvidence } from '@/api/admin/accounts'
import CodexResponseEvidenceDetails from '../CodexResponseEvidenceDetails.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function render(evidence: CodexResponseEvidence = {}) {
  return mount(CodexResponseEvidenceDetails, { props: { evidence } })
}

describe('Codex response evidence', () => {
  it('keeps a JSON model mismatch independent of both safety buffering headers', () => {
    const wrapper = render({ upstream_response_model: 'gpt-5.6-luna', model_relation: 'different',
      model_conflict: false, model_evidence_source: 'response.model', safety_buffering_enabled: true,
      safety_buffering_faster_model: 'gpt-5.6-luna', header_evidence_scope: 'response' })
    expect(wrapper.get('[data-testid="codex-response-model"]').text()).toBe('gpt-5.6-luna')
    expect(wrapper.get('[data-testid="codex-response-model-relation"]').text()).toContain('modelRelations.different')
    expect(wrapper.get('[data-testid="codex-safety-buffering-enabled"]').text()).toContain('evidenceFlags.yes')
    expect(wrapper.get('[data-testid="codex-safety-buffering-faster-model"]').text()).toBe('gpt-5.6-luna')
    expect(wrapper.text()).toContain('modelEvidenceSources.response')
  })

  it.each([true, false, undefined])('preserves safety buffering tri-state (%s) without inferring a model from its header', (enabled) => {
    const wrapper = render({ safety_buffering_enabled: enabled, safety_buffering_faster_model: 'gpt-5.6-luna', header_evidence_scope: 'connection' })
    expect(wrapper.get('[data-testid="codex-response-model"]').text()).toContain('modelNotReported')
    expect(wrapper.get('[data-testid="codex-response-model-relation"]').text()).toContain('modelRelations.not_reported')
    expect(wrapper.get('[data-testid="codex-safety-buffering-enabled"]').text()).toContain(`evidenceFlags.${enabled === undefined ? 'unknown' : enabled ? 'yes' : 'no'}`)
    expect(wrapper.get('[data-testid="codex-header-evidence-scope"]').text()).toContain('headerEvidenceScopes.connection')
  })

  it('shows a conflict even if the relation from an older response schema is inconsistent', () => {
    const wrapper = render({ upstream_response_model: 'gpt-6-astra', model_relation: 'exact', model_conflict: true, model_evidence_source: 'model' })
    expect(wrapper.get('[data-testid="codex-response-model-relation"]').text()).toContain('modelRelations.conflicting')
    expect(wrapper.text()).toContain('modelEvidenceSources.top_level')
  })

  it('renders only allowed Cookie diagnostic fields, with independent persistent and session expiry', () => {
    const wrapper = render({ cookie_diagnostic: { sent: true, source: 'mixed', names: ['__oailb', '_cfuvid'],
      cookies: [{ name: '__oailb', expires_at: '2026-09-22T12:00:00Z', value: 'private-cookie-value' }, { name: '_cfuvid' }],
      reason: 'cookie_sent', raw_header: 'Cookie: secret', authorization_generation: 'private-generation',
    } } as CodexResponseEvidence)
    const cookies = wrapper.get('[data-testid="codex-cookie-diagnostic"]')
    expect(cookies.text()).toContain('cookieSources.mixed')
    expect(cookies.text()).toContain('__oailb, _cfuvid')
    expect(cookies.text()).toContain(new Date('2026-09-22T12:00:00Z').toLocaleString())
    expect(cookies.text()).toContain('cookieSession')
    expect(cookies.text()).not.toContain('private-cookie-value')
    expect(cookies.text()).not.toContain('Cookie: secret')
    expect(cookies.text()).not.toContain('private-generation')
  })

  it('does not echo unrecognized diagnostic source and reason strings', () => {
    const wrapper = render({ cookie_diagnostic: { sent: false, source: 'Authorization: Bearer private-token', reason: 'private-reason' } } as unknown as CodexResponseEvidence)
    expect(wrapper.text()).toContain('cookieSources.unknown')
    expect(wrapper.text()).toContain('cookieReasons.unknown')
    expect(wrapper.text()).not.toContain('private-token')
    expect(wrapper.text()).not.toContain('private-reason')
  })

  it.each(['cookie_staged', 'cookie_target_required', 'cookie_target_rejected', 'cookie_commit_conflict', 'cookie_attempt_closed'])('renders the safe Cookie commit outcome %s', (reason) => {
    const wrapper = render({ cookie_diagnostic: { sent: false, source: 'none', reason } })
    expect(wrapper.get('[data-testid="codex-cookie-diagnostic"]').text()).toContain(`cookieReasons.${reason}`)
    expect(wrapper.text()).not.toContain('cookieReasons.unknown')
  })
})
