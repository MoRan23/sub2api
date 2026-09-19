import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import type { FingerprintObservationEntry, RequestTimezoneScan } from '@/api/admin/fingerprintObservations'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
import enAccounts from '@/i18n/locales/en/admin/accounts'
import FingerprintObservationRequestDetails from '../components/FingerprintObservationRequestDetails.vue'

const legacyEntry: FingerprintObservationEntry = {
  sequence_id: 1, timestamp: '2026-09-10T05:30:00Z', user_id: 1, username: 'alice', email: '',
  api_key_id: 2, api_key_name: 'work', account_id: 3, account_name: 'OpenAI', pinned: false,
  client_reported_installation_id: '', outbound_installation_id: '', session_id: '', thread_id: '',
  parent_thread_id: '', forked_from_thread_id: '', user_agent: '', originator: '', openai_beta: '',
  version: '', inbound_endpoint: 'POST /v1/responses',
}

type RuntimeMessages = { [key: string]: RuntimeMessages | (() => string) }

// The test runner uses vue-i18n's runtime build, so static messages are functions.
function runtimeMessages(messages: Record<string, unknown>): RuntimeMessages {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? () => value : runtimeMessages(value as Record<string, unknown>),
  ]))
}

function renderDetails(overrides: Partial<FingerprintObservationEntry> = {}) {
  return render(FingerprintObservationRequestDetails, {
    props: { observation: { ...legacyEntry, ...overrides } },
    global: { plugins: [createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': runtimeMessages({ admin: { ...en, ...enAccounts } }) } })] },
  })
}

async function openDetails() {
  await fireEvent.click(screen.getByText('Timezone and residency details'))
  return screen.findByRole('region', { name: 'Processing report' })
}

afterEach(cleanup)

describe('FingerprintObservationRequestDetails', () => {
  it('shows the received response length as unclassified when the account subscription is unknown', async () => {
    renderDetails({ codex_turn_state: {
      enabled: false, action: 'passthrough', model: 'gpt-observed', outbound_length: 0,
      response_length: 332, response_shape: 'unknown', response_source: 'header',
    } })
    await openDetails()
    const observation = within(screen.getByTestId('codex-turn-state-observation'))
    expect(observation.getByText('Unclassified shape (332)')).toBeTruthy()
    expect(observation.getByText('Response header')).toBeTruthy()
    expect(observation.queryByText('Not observed')).toBeNull()
    expect(observation.queryByText('Matches target shape (332)')).toBeNull()
  })

  it('shows passive turn-state request and response observations when the account cache is disabled', async () => {
    renderDetails({ codex_turn_state: {
      enabled: false, action: 'passthrough', source: 'client', model: 'gpt-observed',
      outbound_length: 292, response_length: 332, response_shape: 'target',
    } })
    expect(screen.queryByTestId('codex-turn-state-observation')).toBeNull()
    await openDetails()
    const observation = within(screen.getByTestId('codex-turn-state-observation'))
    expect(observation.getByText('Disabled (observation only)')).toBeTruthy()
    expect(observation.getByText('gpt-observed')).toBeTruthy()
    expect(observation.getByText('292')).toBeTruthy()
    expect(observation.getByText('Matches target shape (332)')).toBeTruthy()
    expect(observation.getByText('Passed through')).toBeTruthy()
  })

  it.each([
    ['Asia/Tokyo', '09/10/2026, 14:30:00 GMT+9'],
    ['America/New_York', '09/10/2026, 01:30:00 EDT'],
    ['invalid/timezone', '09/10/2026, 05:30:00 UTC'],
    [undefined, '09/10/2026, 05:30:00 UTC'],
  ])('formats gateway receipt time with the frozen target %s, using UTC for unavailable targets', async (target, formatted) => {
    renderDetails({ timezone_target: target, timezone_conversions: [
      { source: 'environment_context', path: 'input.0.content.0.text', original: 'Asia/Shanghai', output: target, status: 'converted', received_at: '2026-09-10T05:30:00Z' },
    ] })
    const report = await openDetails()
    expect(within(report).getByText(`Gateway receipt time: ${formatted}`)).toBeTruthy()
  })

  it('does not invent an egress target for legacy observations', async () => {
    renderDetails()
    await openDetails()
    expect(screen.queryByTestId('egress-location-details')).toBeNull()
  })

  it('keeps compatibility loss separate from an unchanged outbound integrity result and expands only on demand', async () => {
    const turnID = '01998b93-f718-7000-9000-111122223333'
    renderDetails({
      turn_id: turnID,
      conversion_check: {
        status: 'known_loss',
        issues: [{ path: 'messages.1.content.0', reason: 'unsupported_chat_semantics' }],
      },
      request_integrity: {
        mode: 'observe', status: 'unchanged', baseline_protocol: 'chat_completions',
        baseline_stage: 'responses_adapter_output', attempt: 1, transport: 'http',
      },
    })

    expect(screen.getByTestId('conversion-check-summary').textContent).toContain('Known compatibility loss')
    expect(screen.getByTestId('request-integrity-summary').textContent).toContain('No differences')
    expect(screen.queryByTestId('conversion-check-details')).toBeNull()
    expect(screen.queryByText('messages.1.content.0')).toBeNull()
    expect(screen.queryByText(turnID)).toBeNull()

    await openDetails()
    const conversion = screen.getByTestId('conversion-check-details')
    expect(within(conversion).getByText('Known compatibility loss')).toBeTruthy()
    expect(within(conversion).getByText('messages.1.content.0')).toBeTruthy()
    expect(within(conversion).getByText('unsupported_chat_semantics')).toBeTruthy()
    expect(within(conversion).getByText('Checks key semantics of the original Chat request against the converted Responses request; this is not a full-field or lossless check.')).toBeTruthy()
    expect(within(conversion).queryByText('No differences')).toBeNull()
    expect(within(screen.getByTestId('request-integrity-details')).getByText('No differences')).toBeTruthy()
    expect(screen.getByText(turnID)).toBeTruthy()
  })

  it('describes a checked conversion as a key-semantics check without requiring an integrity observation', async () => {
    renderDetails({ conversion_check: { status: 'checked' } })
    expect(screen.getByTestId('conversion-check-summary').textContent).toContain('Key semantics checked')
    expect(screen.queryByTestId('request-integrity-summary')).toBeNull()

    await openDetails()
    const detail = screen.getByTestId('conversion-check-details')
    expect(within(detail).getByText('Key semantics checked')).toBeTruthy()
    expect(within(detail).queryByText('No differences')).toBeNull()
    expect(within(detail).getByText('Checks key semantics of the original Chat request against the converted Responses request; this is not a full-field or lossless check.')).toBeTruthy()
    expect(screen.queryByTestId('request-integrity-details')).toBeNull()
  })

  it('does not invent a conversion result or a complete location for legacy timezone-only observations', async () => {
    renderDetails({
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: 'Europe/London', current: false, status: 'valid' },
      ] },
    })
    expect(screen.queryByTestId('conversion-check-summary')).toBeNull()
    await openDetails()
    expect(screen.queryByTestId('conversion-check-details')).toBeNull()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    expect(within(inbound).getByText('Europe/London')).toBeTruthy()
    expect(inbound.querySelector('[data-location-field="country"]')).toBeNull()
    expect(screen.queryByTestId('search-location-action')).toBeNull()
  })

  it('shows all five actual location fields and the replaced before/after objects only when expanded', async () => {
    const before = { type: 'approximate', country: 'CN', region: 'Guangdong', city: 'Shenzhen', timezone: 'Asia/Shanghai' }
    const after = { type: 'approximate', country: 'US', region: 'Washington', city: 'Seattle', timezone: 'America/Los_Angeles' }
    renderDetails({
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: before.timezone, current: false, status: 'valid', location: before },
      ] },
      outbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: after.timezone, current: false, status: 'valid', location: after },
      ] },
      timezone_conversions: [{
        source: 'web_search', path: 'tools.0.user_location.timezone', original: before.timezone,
        output: after.timezone, status: 'converted', location_before: before, location_after: after,
        location_added: false,
      }],
    })
    expect(screen.queryByText('Shenzhen')).toBeNull()
    expect(screen.queryByText('Seattle')).toBeNull()

    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    for (const [field, value] of Object.entries(before)) {
      expect(inbound.querySelector(`[data-location-field="${field}"]`)?.textContent).toBe(value)
    }
    for (const [field, value] of Object.entries(after)) {
      expect(outbound.querySelector(`[data-location-field="${field}"]`)?.textContent).toBe(value)
    }
    for (const label of ['Type', 'Country', 'Region', 'City', 'Timezone']) {
      expect(within(inbound).getByText(label, { selector: 'dt' })).toBeTruthy()
      expect(within(outbound).getByText(label, { selector: 'dt' })).toBeTruthy()
    }
    const row = within(report).getByText('tools.0.user_location.timezone').closest('tr')!
    const cells = within(row).getAllByRole('cell')
    for (const [field, value] of Object.entries(before)) {
      expect(cells[1]!.querySelector(`[data-location-field="${field}"]`)?.textContent).toBe(value)
    }
    for (const [field, value] of Object.entries(after)) {
      expect(cells[2]!.querySelector(`[data-location-field="${field}"]`)?.textContent).toBe(value)
    }
    expect(within(row).getByTestId('search-location-action').textContent).toContain('Location replaced')
    expect(within(cells[1]!).queryByText('Seattle')).toBeNull()
    expect(within(cells[2]!).queryByText('Shenzhen')).toBeNull()
  })

  it('distinguishes newly added location data and renders absent fields as empty values', async () => {
    const after = { country: 'US', timezone: 'America/Los_Angeles' }
    renderDetails({
      outbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'web_search', path: 'settings.user_location.timezone', value: after.timezone, current: false, status: 'valid', location: after },
      ] },
      timezone_conversions: [{
        source: 'web_search', path: 'settings.user_location.timezone', original: '', output: after.timezone,
        status: 'converted', location_after: after, location_added: true,
      }],
    })
    const report = await openDetails()
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    for (const field of ['type', 'region', 'city']) {
      expect(outbound.querySelector(`[data-location-field="${field}"]`)?.textContent).toBe('—')
    }
    expect(outbound.querySelector('[data-location-field="country"]')?.textContent).toBe('US')
    expect(outbound.querySelector('[data-location-field="timezone"]')?.textContent).toBe('America/Los_Angeles')
    const row = within(report).getByText('settings.user_location.timezone').closest('tr')!
    const cells = within(row).getAllByRole('cell')
    expect(cells[1]!.textContent?.trim()).toBe('—')
    expect(within(cells[1]!).queryByText('US')).toBeNull()
    expect(cells[2]!.querySelector('[data-location-field="country"]')?.textContent).toBe('US')
    expect(cells[2]!.querySelector('[data-location-field="city"]')?.textContent).toBe('—')
    expect(within(row).getByTestId('search-location-action').textContent).toContain('Location added')
  })

  it('shows a compact integrity status and expands fields and safe reasons with the correct baseline boundary', async () => {
    renderDetails({ request_integrity: {
      mode: 'observe', status: 'difference', baseline_protocol: 'messages', baseline_stage: 'responses_adapter_output',
      attempt: 2, transport: 'ws', changed_fields: ['input.0.content', 'reasoning'], rule_codes: ['encrypted_reasoning_removed'], truncated: true,
    } })
    expect(screen.getByTestId('request-integrity-summary').textContent).toContain('Differences found')
    expect(screen.queryByTestId('request-integrity-details')).toBeNull()
    await openDetails()
    const detail = screen.getByTestId('request-integrity-details')
    expect(within(detail).getByText('Observe only')).toBeTruthy()
    expect(within(detail).getByText('Messages')).toBeTruthy()
    expect(within(detail).getByText('Converted Responses request')).toBeTruthy()
    expect(within(detail).getByText('The baseline is the first converted Responses body. The protocol converter itself is outside this comparison.')).toBeTruthy()
    expect(within(detail).getByText('input.0.content')).toBeTruthy()
    expect(within(detail).getByText('Encrypted reasoning removed during recovery (lossy)')).toBeTruthy()
    expect(within(detail).getByText('2')).toBeTruthy()
    expect(within(detail).getByText('WS')).toBeTruthy()
    expect(within(detail).getByText('Only the first 32 difference locations are shown; this list is incomplete.')).toBeTruthy()
    expect(detail.querySelectorAll('dl > div').length).toBe(6)
  })

  it.each([
    ['unchanged', 'No differences', undefined],
    ['expected_transform', 'Matches known transformations', undefined],
    ['skipped', 'Check incomplete', 'body_too_large'],
  ] as const)('distinguishes integrity state %s without treating incomplete checks as passes', async (status, label, reason) => {
    renderDetails({ request_integrity: {
      mode: 'observe', status, baseline_protocol: 'responses', baseline_stage: 'ingress', attempt: 1, transport: 'http', reason,
    } })
    expect(screen.getByTestId('request-integrity-summary').textContent).toContain(label)
    await openDetails()
    const detail = screen.getByTestId('request-integrity-details')
    expect(within(detail).getByText(label)).toBeTruthy()
    if (reason) expect(within(detail).getByText(/Request body exceeds the check size limit/)).toBeTruthy()
    expect(within(detail).queryByText('The baseline is the first converted Responses body. The protocol converter itself is outside this comparison.')).toBeNull()
  })

  it('does not invent integrity results for old observations', async () => {
    renderDetails()
    expect(screen.queryByTestId('request-integrity-summary')).toBeNull()
    await openDetails()
    expect(screen.queryByTestId('request-integrity-details')).toBeNull()
  })

  it('keeps unknown conversion issues as escaped text and contains long location values within the detail layout', async () => {
    const unsafeText = '<img src=x onerror="alert(1)">'
    const view = renderDetails({
      conversion_check: { status: 'known_loss', issues: [{ path: unsafeText, reason: unsafeText }] },
      outbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'web_search', path: 'tools.0.user_location.timezone', value: 'America/Los_Angeles', current: false,
        location: { type: 'approximate', country: 'US', region: 'California', city: 'LosAngeles'.repeat(40), timezone: 'America/Los_Angeles' },
      }] },
      timezone_conversions: [{ source: 'web_search', path: 'tools.0.user_location.timezone', original: '', output: 'America/Los_Angeles', status: 'converted' }],
    })
    const report = await openDetails()
    expect(view.container.querySelector('img')).toBeNull()
    expect(within(screen.getByTestId('conversion-check-details')).getAllByText(unsafeText)).toHaveLength(2)
    const city = screen.getByTestId('search-location-fields').querySelector('[data-location-field="city"]')!
    expect(city.textContent).toBe('LosAngeles'.repeat(40))
    expect(city.classList.contains('break-all')).toBe(true)
    expect(city.classList.contains('min-w-0')).toBe(true)
    expect(report.querySelector('table')?.parentElement?.classList.contains('overflow-x-auto')).toBe(true)
  })

  it('expands actual inbound and outbound values without confusing the configured target with an observation', async () => {
    renderDetails({
      event_kind: 'http_request', timezone_target: 'America/Los_Angeles',
      outbound_codex_residency: 'us', outbound_codex_residency_source: 'request_headers',
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', environment_source: 'metadata', path: 'input.0.content.0.text', value: 'Asia/Shanghai', current: true, current_date: '2026-09-10', status: 'valid' },
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: 'Europe/London', current: false, status: 'valid' },
        { source: 'environment_context', environment_source: 'metadata', path: 'input.2.content.0.text', value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', status: 'valid' },
      ] },
      outbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', environment_source: 'mapped', path: 'input.0.content.0.text', value: 'America/Los_Angeles', current: true, current_date: '2026-09-09', status: 'valid' },
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: 'America/Los_Angeles', current: false, status: 'valid' },
        { source: 'environment_context', environment_source: 'mapped', path: 'input.2.content.0.text', value: 'America/Los_Angeles', current: false, current_date: '2026-08-01', status: 'valid' },
      ] },
      timezone_conversions: [
        { source: 'environment_context', environment_source: 'mapped', path: 'input.0.content.0.text', original: 'Asia/Shanghai', output: 'America/Los_Angeles', date_before: '2026-09-10', date_after: '2026-09-09', status: 'converted', time_basis: 'gateway_received_at', received_at: '2026-09-10T05:30:00Z', reason: 'timezone_converted' },
        { source: 'environment_context', environment_source: 'mapped', path: 'input.2.content.0.text', original: 'Asia/Tokyo', output: 'America/Los_Angeles', date_before: '2026-08-01', date_after: '2026-08-01', status: 'converted', reason: 'historical_timezone_converted' },
      ],
    })

    expect(screen.queryByRole('region', { name: 'Processing report' })).toBeNull()
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    expect(within(inbound).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(inbound).getByText('Europe/London')).toBeTruthy()
    expect(within(inbound).queryByText('America/Los_Angeles')).toBeNull()
    expect(within(outbound).getAllByText('America/Los_Angeles')).toHaveLength(3)
    expect(within(outbound).queryByText('Asia/Tokyo')).toBeNull()
    expect(within(outbound).getByText('2026-09-09')).toBeTruthy()
    expect(within(report).getByText('Date calculated from gateway receipt time')).toBeTruthy()
    expect(within(report).getByText(/09\/09\/2026, 22:30:00 PDT/)).toBeTruthy()
    expect(within(inbound).getByText('2026-08-01')).toBeTruthy()
    expect(within(outbound).getByText('2026-08-01')).toBeTruthy()
    const historicalRow = within(report).getByText('Historical environment timezone converted; original date preserved').closest('tr')!
    expect(within(historicalRow).getAllByText('2026-08-01')).toHaveLength(2)
    expect(within(historicalRow).getByText('Converted')).toBeTruthy()
    expect(within(historicalRow).queryByText('Date calculated from gateway receipt time')).toBeNull()
    expect(within(historicalRow).queryByText(/Gateway receipt time/)).toBeNull()

    await fireEvent.click(screen.getByText('Timezone and residency details'))
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Processing report' })).toBeNull())
  })

  it('keeps the historical skip explanation for observations created by older versions', async () => {
    renderDetails({
      timezone_conversions: [{ source: 'environment_context', path: 'input.0.content', original: 'Asia/Tokyo', output: 'Asia/Tokyo', status: 'skipped', reason: 'historical' }],
    })
    const report = await openDetails()
    expect(within(report).getByText('Historical environment preserved')).toBeTruthy()
    expect(within(report).getByText('Skipped')).toBeTruthy()
    expect(within(report).queryByText('Historical environment timezone converted; original date preserved')).toBeNull()
  })

  it('shows quoted environment text as an unqualified candidate without historical or invalid warnings', async () => {
    renderDetails({
      timezone_comparison_status: 'not_applicable',
      inbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'reference', path: 'input.0.content.0.text',
        value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', status: 'invalid', reason: 'environment_metadata_missing',
      }] },
      timezone_conversions: [{
        source: 'environment_context', environment_source: 'reference', path: 'input.0.content.0.text',
        original: 'Asia/Tokyo', output: 'Asia/Tokyo', date_before: '2026-08-01', date_after: '2026-08-01',
        status: 'skipped', reason: 'environment_metadata_missing',
      }],
    })
    expect(screen.getByText('Timezone comparison not applicable')).toBeTruthy()
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    expect(within(inbound).getByText('Text candidate (not an environment source)')).toBeTruthy()
    expect(within(inbound).getByText('Asia/Tokyo')).toBeTruthy()
    expect(within(inbound).getByText('2026-08-01')).toBeTruthy()
    expect(screen.queryByText('Current environment')).toBeNull()
    expect(screen.queryByText('Historical environment')).toBeNull()
    expect(screen.queryByText('Invalid value')).toBeNull()
    expect(screen.queryByText('Environment check incomplete')).toBeNull()
    expect(within(report).getByText('Skipped')).toBeTruthy()
    expect(within(report).getByText('Text candidate (not an environment source)')).toBeTruthy()
    expect(within(report).getByText('This is a text candidate, not a declared environment source; excluded from conversion and comparison')).toBeTruthy()
    expect(within(report).queryByText('Converted')).toBeNull()
  })

  it('classifies metadata-backed environments as current or historical while retaining their actual values', async () => {
    renderDetails({
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', environment_source: 'metadata', path: 'input.0.content.0.text', value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', status: 'valid' },
        { source: 'environment_context', environment_source: 'metadata', path: 'input.2.content.0.text', value: 'Asia/Shanghai', current: true, current_date: '2026-09-18', status: 'valid' },
      ] },
    })
    await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const current = within(inbound).getByText('input.2.content.0.text').closest('li')!
    const historical = within(inbound).getByText('input.0.content.0.text').closest('li')!
    expect(within(current).getByText('Current environment')).toBeTruthy()
    expect(within(current).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(current).getByText('2026-09-18')).toBeTruthy()
    expect(within(current).queryByText('Historical environment')).toBeNull()
    expect(within(historical).getByText('Historical environment')).toBeTruthy()
    expect(within(historical).getByText('Asia/Tokyo')).toBeTruthy()
    expect(within(historical).getByText('2026-08-01')).toBeTruthy()
    expect(within(historical).queryByText('Current environment')).toBeNull()
    expect(within(inbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    expect(within(inbound).queryByText('Environment source unclassified')).toBeNull()
  })

  it('preserves a mapped outbound environment classification after the protocol removes source metadata', async () => {
    renderDetails({
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'metadata', path: 'messages.1.content.0.text',
        value: 'Asia/Shanghai', current: true, current_date: '2026-09-18', status: 'valid',
      }] },
      outbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'mapped', path: 'input.0.content.0.text',
        value: 'America/Los_Angeles', current: true, current_date: '2026-09-17', status: 'valid',
      }] },
      timezone_conversions: [{
        source: 'environment_context', environment_source: 'mapped', path: 'input.0.content.0.text',
        original: 'Asia/Shanghai', output: 'America/Los_Angeles', date_before: '2026-09-18', date_after: '2026-09-17',
        status: 'converted', reason: 'timezone_converted',
      }],
    })
    const report = await openDetails()
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    expect(within(outbound).getByText('Current environment')).toBeTruthy()
    expect(within(outbound).getByText('America/Los_Angeles')).toBeTruthy()
    expect(within(outbound).getByText('2026-09-17')).toBeTruthy()
    expect(within(outbound).queryByText('Asia/Shanghai')).toBeNull()
    expect(within(outbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    expect(within(outbound).queryByText('Environment source unclassified')).toBeNull()
    const cells = within(within(report).getByText('input.0.content.0.text').closest('tr')!).getAllByRole('cell')
    expect(within(cells[1]!).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(cells[1]!).getByText('2026-09-18')).toBeTruthy()
    expect(within(cells[2]!).getByText('America/Los_Angeles')).toBeTruthy()
    expect(within(cells[2]!).getByText('2026-09-17')).toBeTruthy()
    expect(within(report).getByText('Converted')).toBeTruthy()
  })

  it.each([
    [true, 'Structural fallback · Current environment', '2026-09-18', '2026-09-17', 'timezone_converted'],
    [false, 'Structural fallback · Historical environment', '2026-08-01', '2026-08-01', 'historical_timezone_converted'],
  ] as const)('retains a structural fallback source and actual dates through outbound conversion (current=%s)', async (current, label, beforeDate, afterDate, reason) => {
    renderDetails({
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'messages.0.content.0.text',
        value: 'Asia/Shanghai', current, current_date: beforeDate, status: 'valid',
      }] },
      outbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text',
        value: 'America/Los_Angeles', current, current_date: afterDate, status: 'valid',
      }] },
      timezone_conversions: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text',
        original: 'Asia/Shanghai', output: 'America/Los_Angeles', date_before: beforeDate, date_after: afterDate,
        status: 'converted', reason,
      }],
    })
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    expect(within(inbound).getByText(label)).toBeTruthy()
    expect(within(inbound).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(inbound).getByText(beforeDate)).toBeTruthy()
    expect(within(outbound).getByText(label)).toBeTruthy()
    expect(within(outbound).getByText('America/Los_Angeles')).toBeTruthy()
    expect(within(outbound).getByText(afterDate)).toBeTruthy()
    expect(within(outbound).queryByText('Asia/Shanghai')).toBeNull()
    expect(within(outbound).queryByText('Environment source unclassified')).toBeNull()
    expect(within(outbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    const row = within(report).getByText('input.0.content.0.text').closest('tr')!
    const cells = within(row).getAllByRole('cell')
    expect(within(cells[0]!).getByText('Structural fallback (strict environment shape)')).toBeTruthy()
    expect(within(cells[1]!).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(cells[1]!).getByText(beforeDate)).toBeTruthy()
    expect(within(cells[2]!).getByText('America/Los_Angeles')).toBeTruthy()
    expect(within(cells[2]!).getByText(afterDate)).toBeTruthy()
    expect(within(row).getByText('Converted')).toBeTruthy()
  })

  it('does not promote a neighboring text reference to a structural fallback environment', async () => {
    renderDetails({
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text', value: 'Asia/Shanghai', current: true, current_date: '2026-09-18', status: 'valid' },
        { source: 'environment_context', environment_source: 'reference', path: 'input.1.content.0.text', value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', reason: 'environment_metadata_missing' },
      ] },
      timezone_conversions: [
        { source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text', original: 'Asia/Shanghai', output: 'America/Los_Angeles', status: 'converted', reason: 'timezone_converted' },
        { source: 'environment_context', environment_source: 'reference', path: 'input.1.content.0.text', original: 'Asia/Tokyo', output: 'Asia/Tokyo', status: 'skipped', reason: 'environment_metadata_missing' },
      ],
    })
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const fallback = within(inbound).getByText('input.0.content.0.text').closest('li')!
    const reference = within(inbound).getByText('input.1.content.0.text').closest('li')!
    expect(within(fallback).getByText('Structural fallback · Current environment')).toBeTruthy()
    expect(within(reference).getByText('Text candidate (not an environment source)')).toBeTruthy()
    expect(within(reference).getByText('Asia/Tokyo')).toBeTruthy()
    expect(within(reference).getByText('2026-08-01')).toBeTruthy()
    expect(within(reference).queryByText(/Structural fallback/)).toBeNull()
    expect(within(reference).queryByText('Historical environment')).toBeNull()
    expect(within(reference).queryByText('Invalid value')).toBeNull()
    const referenceRow = within(report).getByText('input.1.content.0.text').closest('tr')!
    expect(within(referenceRow).getByText('Text candidate (not an environment source)')).toBeTruthy()
    expect(within(referenceRow).getByText('Skipped')).toBeTruthy()
    expect(within(referenceRow).queryByText(/Structural fallback/)).toBeNull()
    expect(within(referenceRow).queryByText('Converted')).toBeNull()
  })

  it('reports an incomplete real environment check without treating malformed values as successful conversion', async () => {
    renderDetails({
      timezone_comparison_status: 'incomplete',
      inbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'metadata', path: 'input.0.content.0.text',
        value: 'Not/A-Timezone', current: true, current_date: '2026-09-18', status: 'invalid', reason: 'invalid_timezone',
      }] },
      timezone_conversions: [{
        source: 'environment_context', environment_source: 'metadata', path: 'input.0.content.0.text',
        original: 'Not/A-Timezone', output: 'Not/A-Timezone', date_before: '2026-09-18', date_after: '2026-09-18',
        status: 'incomplete', reason: 'invalid_timezone',
      }],
    })
    expect(screen.getByText('Timezone comparison incomplete')).toBeTruthy()
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    expect(within(inbound).getByText('Current environment')).toBeTruthy()
    expect(within(inbound).getByText('Environment check incomplete')).toBeTruthy()
    expect(within(inbound).getByText('Not/A-Timezone')).toBeTruthy()
    expect(within(inbound).getByText('2026-09-18')).toBeTruthy()
    expect(within(inbound).queryByText('Invalid value')).toBeNull()
    expect(within(inbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    expect(within(report).getByText('Check incomplete')).toBeTruthy()
    expect(within(report).queryByText('Converted')).toBeNull()
    expect(screen.queryByText('Outbound values verified')).toBeNull()
  })

  it('retains structural fallback provenance while warning about an environment corrupted after adaptation', async () => {
    renderDetails({
      timezone_comparison_status: 'incomplete',
      inbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text',
        value: 'Asia/Shanghai', current: true, current_date: '2026-09-18', status: 'valid',
      }] },
      outbound_timezone_observations: { scan_status: 'complete', items: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text',
        value: 'broken-zone', current: true, current_date: '2026-09-17', status: 'invalid', reason: 'invalid_timezone',
      }] },
      timezone_conversions: [{
        source: 'environment_context', environment_source: 'structural_fallback', path: 'input.0.content.0.text',
        original: 'Asia/Shanghai', output: 'broken-zone', status: 'incomplete', reason: 'invalid_timezone',
      }],
    })
    const report = await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    const outbound = screen.getByRole('region', { name: 'Actual outbound content' })
    expect(within(inbound).queryByText('Environment check incomplete')).toBeNull()
    expect(within(outbound).getByText('Structural fallback · Current environment')).toBeTruthy()
    expect(within(outbound).getByText('broken-zone')).toBeTruthy()
    expect(within(outbound).getByText('Environment check incomplete')).toBeTruthy()
    expect(within(outbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    expect(within(report).getByText('Check incomplete')).toBeTruthy()
    expect(screen.getByText('Timezone comparison incomplete')).toBeTruthy()
    expect(screen.queryByText('Outbound values verified')).toBeNull()
  })

  it('keeps legacy environments unclassified instead of inferring history from the old current flag', async () => {
    renderDetails({
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', path: 'input.0.content.0.text', value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', status: 'valid' },
        { source: 'environment_context', path: 'input.1.content.0.text', value: 'Asia/Shanghai', current: true, current_date: '2026-09-18', status: 'valid' },
      ] },
    })
    await openDetails()
    const inbound = screen.getByRole('region', { name: 'Client inbound declarations' })
    expect(within(inbound).getAllByText('Environment source unclassified')).toHaveLength(2)
    expect(within(inbound).getByText('Asia/Tokyo')).toBeTruthy()
    expect(within(inbound).getByText('2026-08-01')).toBeTruthy()
    expect(within(inbound).getByText('Asia/Shanghai')).toBeTruthy()
    expect(within(inbound).getByText('2026-09-18')).toBeTruthy()
    expect(within(inbound).queryByText('Current environment')).toBeNull()
    expect(within(inbound).queryByText('Historical environment')).toBeNull()
    expect(within(inbound).queryByText('Text candidate (not an environment source)')).toBeNull()
    expect(within(inbound).queryByText('Invalid value')).toBeNull()
  })

  it('pairs each review flag with its own label and distinguishes false from missing values', async () => {
    const view = renderDetails({
      auto_review_enabled: true,
      node_repl_auto_review_required: false,
      node_repl_disabled: true,
    })
    await openDetails()

    const flagValue = (label: string) => {
      const term = screen.getByText(label, { selector: 'dt' })
      expect(term.parentElement?.tagName).toBe('DIV')
      return term.parentElement?.querySelector('dd')?.textContent
    }
    expect(flagValue('Auto review')).toBe('true')
    expect(flagValue('Node REPL auto review required')).toBe('false')
    expect(flagValue('Node REPL disabled')).toBe('true')

    await view.rerender({ observation: {
      ...legacyEntry,
      auto_review_enabled: false,
      node_repl_auto_review_required: true,
      node_repl_disabled: false,
    } })
    expect(flagValue('Auto review')).toBe('false')
    expect(flagValue('Node REPL auto review required')).toBe('true')
    expect(flagValue('Node REPL disabled')).toBe('false')

    await view.rerender({ observation: legacyEntry })
    expect(flagValue('Auto review')).toBe('—')
    expect(flagValue('Node REPL auto review required')).toBe('—')
    expect(flagValue('Node REPL disabled')).toBe('—')
  })

  it('keeps legacy missing data separate from a complete empty scan and an absent residency header', async () => {
    const view = renderDetails()
    await openDetails()
    expect(within(screen.getByRole('region', { name: 'Client inbound declarations' })).getByText('Not collected')).toBeTruthy()
    expect(screen.queryByText('Scan complete; no supported timezone or search location fields found')).toBeNull()
    expect(screen.queryByText('America/Los_Angeles')).toBeNull()

    await view.rerender({ observation: {
      ...legacyEntry, inbound_timezone_observations: { scan_status: 'complete', items: [] },
      outbound_timezone_observations: { scan_status: 'complete', items: [] },
      timezone_conversions: [], outbound_codex_residency: '',
    } })
    expect(screen.getAllByText('Scan complete; no supported timezone or search location fields found')).toHaveLength(2)
    expect(screen.getByText('No field processing records')).toBeTruthy()
    expect(screen.getByText('Not sent')).toBeTruthy()
  })

  it.each([
    ['limited', 'Scan limited; results incomplete'],
    ['parse_failed', 'Parsing failed; scan incomplete'],
    ['not_applicable', 'Not applicable'],
  ] as const)('does not present a %s scan as no fields found', async (status, label) => {
    const scan: RequestTimezoneScan = { scan_status: status, items: [] }
    renderDetails({ inbound_timezone_observations: scan, timezone_comparison_status: 'incomplete' })
    await openDetails()
    expect(within(screen.getByRole('region', { name: 'Client inbound declarations' })).getByText(label)).toBeTruthy()
    expect(screen.queryByText('Scan complete; no supported timezone or search location fields found')).toBeNull()
    expect(screen.getByText('Timezone comparison incomplete')).toBeTruthy()
  })

  it('labels WebSocket frame attempts and residency inherited from their handshake', async () => {
    renderDetails({ event_kind: 'ws_response_create', outbound_codex_residency: 'us', outbound_codex_residency_source: 'ws_handshake' })
    await openDetails()
    expect(screen.getByText('WS request frame')).toBeTruthy()
    expect(screen.getByText(/From the current connection handshake headers/)).toBeTruthy()
    expect(screen.getByText(/not confirmation of upstream receipt or response delivery/)).toBeTruthy()
  })

  it.each([
    ['value_not_observable', 'The original value is not observable and cannot be reliably compared'],
    ['quoted_xml_content', 'Environment contains XML comments or CDATA; preserved unchanged'],
    ['source_changed_before_apply', 'Source content or location changed during adaptation; conversion was not applied'],
  ])('explains why %s was skipped without implying a successful conversion', async (reason, label) => {
    renderDetails({
      timezone_conversions: [{ source: 'environment_context', path: 'input.0.content', original: '', output: '', status: 'skipped', reason }],
    })
    const report = await openDetails()
    expect(within(report).getByText(label)).toBeTruthy()
    expect(within(report).getByText('Skipped')).toBeTruthy()
    expect(within(report).queryByText('Converted')).toBeNull()
  })

  it('renders malformed values as text and reports a removed or unmatched source without claiming delivery', async () => {
    const unsafeText = '<img src=x onerror="alert(1)">'
    const view = renderDetails({
      timezone_comparison_status: 'not_sent',
      inbound_timezone_observations: { scan_status: 'complete', items: [{ source: 'web_search', path: unsafeText, value: unsafeText, current: false, status: 'invalid', reason: 'invalid_timezone' }] },
      timezone_conversions: [{ source: 'web_search', path: unsafeText, original: unsafeText, output: '', status: 'not_sent', reason: 'not_sent' }, { source: 'environment_context', path: 'input.0.content', original: 'UTC', output: '', status: 'unmatched', reason: 'unmatched' }],
    })
    const report = await openDetails()
    expect(view.container.querySelector('img')).toBeNull()
    expect(screen.getAllByText(unsafeText).length).toBeGreaterThan(0)
    expect(within(report).getByText('The source was not sent after protocol adaptation')).toBeTruthy()
    expect(within(report).getByText('Inbound source could not be matched to an outbound location')).toBeTruthy()
    expect(screen.queryByText('Outbound values verified')).toBeNull()
  })
})
