import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/vue'
import { createI18n } from 'vue-i18n'
import type { ProxyGeoInfo } from '@/types'
import en from '@/i18n/locales/en/admin/resources'
import zh from '@/i18n/locales/zh/admin/resources'
import ProxyGeoDetails from '../ProxyGeoDetails.vue'

function runtimeMessages(messages: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? () => value : runtimeMessages(value as Record<string, unknown>),
  ]))
}
function show(geo: ProxyGeoInfo, locale = 'en') {
  return render(ProxyGeoDetails, {
    props: { geo }, global: { plugins: [createI18n({ legacy: false, locale, messages: {
      en: runtimeMessages({ admin: en }), zh: runtimeMessages({ admin: zh }),
    } })] },
  })
}
afterEach(cleanup)

describe('ProxyGeoDetails', () => {
  it('shows timezone and compact status, revealing IPv6, method and timestamp only on demand', async () => {
    show({ ip_address: '2001:db8::123', country: 'Japan', region: 'Tokyo', city: 'Tokyo', timezone: 'Asia/Tokyo', geo_status: 'success', geo_checked_at: '2026-09-18T01:00:00Z' })
    expect(screen.getByText('Japan · Tokyo')).toBeTruthy()
    expect(screen.getByText('Asia/Tokyo')).toBeTruthy()
    expect(screen.getByText('Location confirmed')).toBeTruthy()
    expect(screen.queryByText('2001:db8::123')).toBeNull()
    await fireEvent.click(screen.getByText('Lookup details'))
    expect(await screen.findByText('2001:db8::123')).toBeTruthy()
    expect(screen.getByText('Lookup by exit IP')).toBeTruthy()
    expect(screen.getByText('Location checked at')).toBeTruthy()
  })

  it('shows failed location separately and never substitutes a Seattle result', async () => {
    show({ ip_address: '2001:db8::123', geo_status: 'failed', geo_reason: 'incomplete_location' })
    expect(screen.getByText('Timezone unconfirmed')).toBeTruthy()
    expect(screen.getByText('Location lookup failed')).toBeTruthy()
    expect(screen.queryByText('Seattle')).toBeNull()
    await fireEvent.click(screen.getByText('Lookup details'))
    expect(await screen.findByText('Required location fields are missing')).toBeTruthy()
    expect(screen.getByText(/Connectivity and location lookups have separate results/)).toBeTruthy()
  })

  it('keeps legacy missing metadata unknown and translates known failure reasons', async () => {
    const { unmount } = show({}, 'zh')
    expect(screen.getByText('地域未检测')).toBeTruthy()
    expect(screen.getByText('时区未确认')).toBeTruthy()
    unmount()
    show({ geo_status: 'failed', geo_reason: 'ip_mismatch' }, 'zh')
    await fireEvent.click(screen.getByText('检测详情'))
    expect(await screen.findByText('查询结果与出口 IP 不匹配')).toBeTruthy()
  })

  it('keeps a saved historical location visible without expiring its timestamp', async () => {
    show({ country: 'Japan', region: 'Tokyo', city: 'Tokyo', timezone: 'Asia/Tokyo', geo_status: 'success', geo_checked_at: '2020-01-01T00:00:00Z' }, 'zh')
    expect(screen.getByText('Japan · Tokyo')).toBeTruthy()
    expect(screen.getByText('Asia/Tokyo')).toBeTruthy()
    expect(screen.getByText('地域已确认')).toBeTruthy()
    expect(screen.queryByText(/成功结果持久保存/)).toBeNull()
    await fireEvent.click(screen.getByText('检测详情'))
    expect(await screen.findByText(/成功结果持久保存/)).toBeTruthy()
    expect(screen.getByText(/2020/)).toBeTruthy()
    expect(screen.queryByText('之前的有效地域已过期')).toBeNull()
  })
})
