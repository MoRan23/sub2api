import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import type { FingerprintObservationEntry, RequestTimezoneScan } from '@/api/admin/fingerprintObservations'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
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
    global: { plugins: [createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': runtimeMessages({ admin: en }) } })] },
  })
}

async function openDetails() {
  await fireEvent.click(screen.getByText('Timezone and residency details'))
  return screen.findByRole('region', { name: 'Processing report' })
}

afterEach(cleanup)

describe('FingerprintObservationRequestDetails', () => {
  it('expands actual inbound and outbound values without confusing the configured target with an observation', async () => {
    renderDetails({
      event_kind: 'http_request', timezone_target: 'America/Los_Angeles',
      outbound_codex_residency: 'us', outbound_codex_residency_source: 'request_headers',
      timezone_comparison_status: 'matched',
      inbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', path: 'input.0.content.0.text', value: 'Asia/Shanghai', current: true, current_date: '2026-09-10', status: 'valid' },
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: 'Europe/London', current: false, status: 'valid' },
        { source: 'environment_context', path: 'input.2.content.0.text', value: 'Asia/Tokyo', current: false, current_date: '2026-08-01', status: 'valid' },
      ] },
      outbound_timezone_observations: { scan_status: 'complete', items: [
        { source: 'environment_context', path: 'input.0.content.0.text', value: 'America/Los_Angeles', current: true, current_date: '2026-09-09', status: 'valid' },
        { source: 'web_search', path: 'tools.0.user_location.timezone', value: 'America/Los_Angeles', current: false, status: 'valid' },
        { source: 'environment_context', path: 'input.2.content.0.text', value: 'America/Los_Angeles', current: false, current_date: '2026-08-01', status: 'valid' },
      ] },
      timezone_conversions: [
        { source: 'environment_context', path: 'input.0.content.0.text', original: 'Asia/Shanghai', output: 'America/Los_Angeles', date_before: '2026-09-10', date_after: '2026-09-09', status: 'converted', time_basis: 'gateway_received_at', received_at: '2026-09-10T05:30:00Z', reason: 'timezone_converted' },
        { source: 'environment_context', path: 'input.2.content.0.text', original: 'Asia/Tokyo', output: 'America/Los_Angeles', date_before: '2026-08-01', date_after: '2026-08-01', status: 'converted', reason: 'historical_timezone_converted' },
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

  it('keeps legacy missing data separate from a complete empty scan and an absent residency header', async () => {
    const view = renderDetails()
    await openDetails()
    expect(within(screen.getByRole('region', { name: 'Client inbound declarations' })).getByText('Not collected')).toBeTruthy()
    expect(screen.queryByText('Scan complete; no supported timezone fields found')).toBeNull()
    expect(screen.queryByText('America/Los_Angeles')).toBeNull()

    await view.rerender({ observation: {
      ...legacyEntry, inbound_timezone_observations: { scan_status: 'complete', items: [] },
      outbound_timezone_observations: { scan_status: 'complete', items: [] },
      timezone_conversions: [], outbound_codex_residency: '',
    } })
    expect(screen.getAllByText('Scan complete; no supported timezone fields found')).toHaveLength(2)
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
    expect(screen.queryByText('Scan complete; no supported timezone fields found')).toBeNull()
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
