import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import type { OpenAIEgressLocation } from '@/api/admin/fingerprintObservations'
import en from '@/i18n/locales/en/admin/fingerprintObservation'
import zh from '@/i18n/locales/zh/admin/fingerprintObservation'
import OpenAIEgressLocationDetails from '../components/OpenAIEgressLocationDetails.vue'

function runtimeMessages(messages: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? () => value : runtimeMessages(value as Record<string, unknown>),
  ]))
}
const location: OpenAIEgressLocation = {
  route_type: 'proxy', proxy_id: 5, ip_address: '2001:db8::23', country: 'Japan', country_code: 'JP',
  region: 'Tokyo', city: 'Tokyo', timezone: 'Asia/Tokyo', status: 'fresh', source: 'proxy_exit',
  checked_at: '2026-09-18T01:02:00Z',
}
function show(overrides: Partial<OpenAIEgressLocation> = {}, locale = 'en') {
  return render(OpenAIEgressLocationDetails, { props: { location: { ...location, ...overrides } }, global: { plugins: [
    createI18n({ legacy: false, locale, messages: { en: runtimeMessages({ admin: en }), zh: runtimeMessages({ admin: zh }) } }),
  ] } })
}
afterEach(cleanup)

describe('OpenAIEgressLocationDetails', () => {
  it('summarizes the measured target and expands the route, IPv6 source and timestamp', async () => {
    show()
    expect(screen.getByText(/Exit location confirmed/)).toBeTruthy()
    expect(screen.queryByText(location.ip_address!)).toBeNull()
    await fireEvent.click(screen.getByText(/Exit location confirmed/))
    expect(await screen.findByText(location.ip_address!)).toBeTruthy()
    expect(screen.getByText('Proxy #5')).toBeTruthy()
    expect(screen.getByText('Proxy exit IP')).toBeTruthy()
    expect(screen.getByText('Asia/Tokyo')).toBeTruthy()
    expect(screen.getByText('Location checked at')).toBeTruthy()
  })

  it('identifies direct routing and a retained result without presenting it as a new successful lookup', async () => {
    show({ route_type: 'direct', proxy_id: undefined, source: 'direct_exit', status: 'stale', reason: 'refresh_pending' })
    await fireEvent.click(screen.getByText(/Recent exit location retained/))
    expect(await screen.findByText('Direct from this service instance')).toBeTruthy()
    expect(screen.getByText('This service instance’s exit IP')).toBeTruthy()
    expect(screen.getByText(/within the last 24 hours/)).toBeTruthy()
    expect(screen.getByText('Location refresh pending')).toBeTruthy()
    expect(screen.queryByText('Exit location confirmed')).toBeNull()
  })

  it('explicitly marks Seattle fallback as an unconfirmed IP location in Chinese', async () => {
    show({ status: 'fallback', source: 'fallback', country: 'United States', country_code: 'US', region: 'Washington', city: 'Seattle', timezone: 'America/Los_Angeles', reason: 'ip_changed_geo_unavailable', checked_at: undefined }, 'zh')
    await fireEvent.click(screen.getByText(/Seattle 兜底/))
    expect(await screen.findByText(/这不是该 IP 的实测位置/)).toBeTruthy()
    expect(screen.getByText('预设兜底地域')).toBeTruthy()
    expect(screen.getByText('出口 IP 已改变，新 IP 的地域尚未确认')).toBeTruthy()
    expect(screen.getByText('未采集')).toBeTruthy()
  })
})
