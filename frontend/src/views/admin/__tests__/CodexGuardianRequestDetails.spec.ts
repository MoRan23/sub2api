import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import type { CodexGuardianRequestObservation } from '@/api/admin/fingerprintObservations'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
import zh from '@/i18n/locales/zh/admin/fingerprintObservation'
import CodexGuardianRequestDetails from '../components/CodexGuardianRequestDetails.vue'

type RuntimeMessages = { [key: string]: RuntimeMessages | (() => string) }
function runtimeMessages(messages: Record<string, unknown>): RuntimeMessages {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? () => value : runtimeMessages(value as Record<string, unknown>),
  ]))
}

const markerAbsent: CodexGuardianRequestObservation = {
  status: 'absent', source: 'http_request', applied: false, reason: 'not_requested', routing_hint_present: false,
  credits: { status: 'missing', type: 'missing', present: false },
  parent_response: { status: 'missing', type: 'missing', present: false },
}

function renderDetails(observation?: CodexGuardianRequestObservation, locale = 'en') {
  return render(CodexGuardianRequestDetails, {
    props: { observation },
    global: { plugins: [createI18n({ legacy: false, locale, messages: {
      en: runtimeMessages({ admin: en }), zh: runtimeMessages({ admin: zh }),
    } })] },
  })
}

afterEach(cleanup)

describe('CodexGuardianRequestDetails', () => {
  it('does not infer fields for legacy records', () => {
    renderDetails()
    expect(screen.getByText('Not recorded')).toBeTruthy()
    expect(screen.queryByTestId('guardian-request-kind')).toBeNull()
    expect(screen.queryByText('Not sent')).toBeNull()
    expect(screen.queryByText('Applied')).toBeNull()
  })

  it.each([
    [{ ...markerAbsent }, 'Not provided'],
    [{ ...markerAbsent, status: 'valid', kind: 'reviewer' }, 'reviewer'],
    [{ ...markerAbsent, status: 'valid', kind: 'classifier' }, 'classifier'],
    [{ ...markerAbsent, status: 'invalid' }, 'Invalid value'],
    [{ ...markerAbsent, status: 'valid' }, 'Invalid value'],
  ] as const)('shows the explicit marker state %#', (observation, expected) => {
    renderDetails(observation)
    expect(screen.getByTestId('guardian-request-kind').textContent).toBe(expected)
  })

  it('shows physical WS evidence and does not retain it when the selected record changes', async () => {
    const { rerender } = renderDetails({ ...markerAbsent, source: 'ws_handshake', status: 'valid', kind: 'reviewer',
      applied: true, reason: 'guardian_contract',
      credits: { status: 'valid', type: 'string', present: true, value: 'true' },
      parent_response: { status: 'valid', type: 'string', present: true },
    })
    expect(screen.getByText(en.fingerprintObservation.request.guardianRequest.sources.ws_handshake)).toBeTruthy()
    expect(screen.getByText('Applied')).toBeTruthy()
    expect(screen.getByTestId('guardian-service-tier').textContent).toBe('Not sent')
    expect(within(screen.getByTestId('guardian-credits')).getByText('"true"')).toBeTruthy()
    expect(within(screen.getByTestId('guardian-parent-response')).getByText('Present · Valid format')).toBeTruthy()
    await rerender({ observation: { ...markerAbsent, service_tier: 'priority', routing_hint_present: true } })
    expect(screen.getByTestId('guardian-request-kind').textContent).toBe('Not provided')
    expect(screen.getByTestId('guardian-service-tier').textContent).toBe('priority')
    expect(screen.queryByText('"true"')).toBeNull()
    expect(screen.queryByText(en.fingerprintObservation.request.guardianRequest.sources.ws_handshake)).toBeNull()
    await rerender({ observation: undefined })
    expect(screen.getByText('Not recorded')).toBeTruthy()
    expect(screen.queryByText('priority')).toBeNull()
  })

  it.each(['true', 'false'] as const)('keeps the credits string %s explicit', (value) => {
    renderDetails({ ...markerAbsent, credits: { status: 'valid', present: true, type: 'string', value } })
    const field = within(screen.getByTestId('guardian-credits'))
    expect(field.getByText(JSON.stringify(value))).toBeTruthy()
    expect(field.getByText('String')).toBeTruthy()
  })

  it.each(['null', 'boolean', 'number', 'object', 'array'] as const)('shows invalid %s credits without rendering its value', (type) => {
    renderDetails({ ...markerAbsent, credits: { status: 'invalid', present: true, type } })
    const field = within(screen.getByTestId('guardian-credits'))
    expect(field.getByText('Invalid value')).toBeTruthy()
    expect(field.getByText(en.fingerprintObservation.request.outboundMetadata.jsonTypes[type])).toBeTruthy()
    expect(field.queryByText('Not provided')).toBeNull()
  })

  it('never renders extra raw fields or malformed marker contents', () => {
    const observation = {
      ...markerAbsent, status: 'invalid', kind: '<img src=x onerror=alert(1)>', reason: 'unrecognized',
      credits: { status: 'invalid', present: true, type: 'string', value: 'secret-credits-value' },
      parent_response: { status: 'valid', present: true, type: 'string', value: 'resp_private_parent' },
      raw_body: 'private-user-content',
    }
    const { container } = renderDetails(observation as CodexGuardianRequestObservation)
    expect(screen.getByTestId('guardian-request-kind').textContent).toBe('Invalid value')
    expect(screen.getByTestId('guardian-request-reason').textContent).toBe('Not recorded')
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).not.toContain('resp_private_parent')
    expect(container.textContent).not.toContain('secret-credits-value')
    expect(container.textContent).not.toContain('private-user-content')
  })

  it.each(['not_requested', 'invalid_marker', 'non_codex_upstream', 'guardian_contract'] as const)('translates processing reason %s', (reason) => {
    renderDetails({ ...markerAbsent, reason }, 'zh')
    expect(screen.getByTestId('guardian-request-reason').textContent).toBe(zh.fingerprintObservation.request.guardianRequest.reasons[reason])
  })
})
