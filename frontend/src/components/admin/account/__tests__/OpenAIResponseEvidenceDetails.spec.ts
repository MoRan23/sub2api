import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { OpenAIResponseEvidence } from '@/api/admin/accounts'
import OpenAIResponseEvidenceDetails from '../OpenAIResponseEvidenceDetails.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function render(evidence: OpenAIResponseEvidence = {}) {
  return mount(OpenAIResponseEvidenceDetails, { props: { evidence } })
}

describe('OpenAI response evidence after cache retirement', () => {
  it.each(['review_required', 'review_not_required', 'mixed', 'invalid'] as const)('renders Guardian %s as response evidence', (status) => {
    const wrapper = render({ guardian_assessment: { status, invalid_present: true } })
    expect(wrapper.get('[data-testid="openai-guardian-assessment"]').text()).toContain(`guardianStatuses.${status}`)
    expect(wrapper.get('[data-testid="openai-guardian-invalid"]').text()).toContain('guardianInvalidPresent')
    expect(wrapper.text()).toContain('guardianHint')
  })

  it('keeps missing historical diagnostics distinct from valid negative assessments', async () => {
    const wrapper = render()
    expect(wrapper.get('[data-testid="openai-guardian-assessment"]').text()).toContain('notRecorded')
    for (const key of ['imagegen_request_id', 'authorization_error', 'identity_error_code']) {
      expect(wrapper.get(`[data-testid="openai-diagnostic-${key}"]`).text()).toContain('notRecorded')
    }
    await wrapper.setProps({ evidence: { guardian_assessment: { status: 'review_not_required' } } })
    expect(wrapper.get('[data-testid="openai-guardian-assessment"]').text()).toContain('guardianStatuses.review_not_required')
    expect(wrapper.find('[data-testid="openai-guardian-invalid"]').exists()).toBe(false)
  })

  it('renders only sanitized header fields as text and clears them on another observation', async () => {
    const wrapper = render({ header_diagnostics: {
      imagegen_request_id: 'image-request-1', authorization_error: '<img src=x onerror=alert(1)>',
      identity_error_code: 'authorization_revoked',
      raw_error_json: 'private-base64',
    } } as OpenAIResponseEvidence)
    expect(wrapper.get('[data-testid="openai-diagnostic-imagegen_request_id"]').text()).toBe('image-request-1')
    expect(wrapper.get('[data-testid="openai-diagnostic-authorization_error"]').text()).toBe('<img src=x onerror=alert(1)>')
    expect(wrapper.get('[data-testid="openai-diagnostic-identity_error_code"]').text()).toBe('authorization_revoked')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private-base64')
    await wrapper.setProps({ evidence: {} })
    expect(wrapper.text()).not.toContain('image-request-1')
    expect(wrapper.text()).not.toContain('authorization_revoked')
  })

  it('keeps a JSON model mismatch independent of safety buffering headers', () => {
    const wrapper = render({ upstream_response_model: 'gpt-5.6-luna', model_relation: 'different',
      model_conflict: false, model_evidence_source: 'response.model', safety_buffering_enabled: true,
      safety_buffering_faster_model: 'header-model', header_evidence_scope: 'response' })
    expect(wrapper.get('[data-testid="openai-response-model"]').text()).toBe('gpt-5.6-luna')
    expect(wrapper.get('[data-testid="openai-response-model-relation"]').text()).toContain('modelRelations.different')
    expect(wrapper.get('[data-testid="openai-safety-buffering-enabled"]').text()).toContain('evidenceFlags.yes')
    expect(wrapper.get('[data-testid="openai-safety-buffering-faster-model"]').text()).toBe('header-model')
    expect(wrapper.text()).toContain('modelEvidenceSources.response')
  })

  it.each([true, false, undefined])('preserves safety buffering tri-state (%s) without inferring a model', (enabled) => {
    const wrapper = render({ safety_buffering_enabled: enabled, safety_buffering_faster_model: 'header-model', header_evidence_scope: 'connection' })
    expect(wrapper.get('[data-testid="openai-response-model"]').text()).toContain('modelNotReported')
    expect(wrapper.get('[data-testid="openai-response-model-relation"]').text()).toContain('modelRelations.not_reported')
    expect(wrapper.get('[data-testid="openai-safety-buffering-enabled"]').text()).toContain(`evidenceFlags.${enabled === undefined ? 'unknown' : enabled ? 'yes' : 'no'}`)
    expect(wrapper.get('[data-testid="openai-header-evidence-scope"]').text()).toContain('headerEvidenceScopes.connection')
  })

  it('shows conflicts without rendering legacy ticket or Cookie diagnostics', () => {
    const wrapper = render({ upstream_response_model: 'gpt-6-astra', model_relation: 'exact', model_conflict: true,
      model_evidence_source: 'model', cookie_diagnostic: { names: ['private-cookie'], token: 'private-token' },
    } as OpenAIResponseEvidence)
    expect(wrapper.get('[data-testid="openai-response-model-relation"]').text()).toContain('modelRelations.conflicting')
    expect(wrapper.text()).toContain('modelEvidenceSources.top_level')
    expect(wrapper.text()).not.toContain('private-cookie')
    expect(wrapper.text()).not.toContain('private-token')
    expect(wrapper.find('[data-testid="codex-cookie-diagnostic"]').exists()).toBe(false)
  })
})
