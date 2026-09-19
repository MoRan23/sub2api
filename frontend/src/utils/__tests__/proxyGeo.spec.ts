import { describe, expect, it } from 'vitest'
import type { Proxy } from '@/types'
import { applyProxyProbeResult } from '../proxyGeo'

function proxy(): Proxy {
  return {
    id: 1, name: 'test', protocol: 'socks5', host: 'localhost', port: 1080, status: 'active',
    created_at: '', updated_at: '', ip_address: '2001:db8::1', country: 'Japan', country_code: 'JP',
    region: 'Tokyo', city: 'Tokyo', timezone: 'Asia/Tokyo', geo_status: 'success',
    geo_checked_at: '2026-09-18T00:00:00Z',
  }
}

describe('applyProxyProbeResult', () => {
  it('keeps fields omitted by a legacy quality result for the same exit IP', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: true, latency_ms: 18, ip_address: target.ip_address, country: 'Japan', country_code: 'JP' })
    expect(target).toMatchObject({ region: 'Tokyo', city: 'Tokyo', timezone: 'Asia/Tokyo', latency_status: 'success', latency_ms: 18 })
  })

  it('clears old geography when a new IPv6 exit has a failed lookup, independently of successful connectivity', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: true, ip_address: '2001:db8::2', latency_ms: 12, geo_status: 'failed', geo_reason: 'incomplete_location', geo_checked_at: '2026-09-18T01:00:00Z' })
    expect(target).toMatchObject({ ip_address: '2001:db8::2', geo_status: 'failed', latency_status: 'success', geo_reason: 'incomplete_location' })
    for (const key of ['country', 'country_code', 'region', 'city', 'timezone'] as const) expect(target[key]).toBeUndefined()
  })

  it('accepts the full retained snapshot and its original timestamp on same-IP lookup failure', () => {
    const target = proxy()
    const geo = { ...target }
    applyProxyProbeResult(target, { ...geo, success: true, geo_status: 'failed', geo_reason: 'timeout' })
    expect(target).toMatchObject({ city: 'Tokyo', timezone: 'Asia/Tokyo', geo_status: 'failed', geo_checked_at: '2026-09-18T00:00:00Z' })
  })

  it('applies an authoritative manual result that clears previous location fields', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: true, ip_address: target.ip_address, geo_status: 'failed', geo_reason: 'timeout' })
    expect(target.city).toBeUndefined()
    expect(target.timezone).toBeUndefined()
    expect(target.geo_checked_at).toBeUndefined()
  })

  it('preserves the previous geo snapshot on a client-side connectivity error with no new result', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: false, message: 'connection failed' })
    expect(target).toMatchObject({ ip_address: '2001:db8::1', timezone: 'Asia/Tokyo', latency_status: 'failed' })
  })

  it('clears a stale exit IP when an authoritative response no longer returns one', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: false, geo_status: 'failed', geo_reason: 'probe_failed' })
    expect(target.ip_address).toBeUndefined()
    expect(target.timezone).toBeUndefined()
  })

  it('does not carry region or timezone across a legacy result with a different IP', () => {
    const target = proxy()
    applyProxyProbeResult(target, { success: true, ip_address: '203.0.113.3', country: 'Canada', country_code: 'CA' })
    expect(target).toMatchObject({ country: 'Canada', ip_address: '203.0.113.3' })
    expect(target.region).toBeUndefined()
    expect(target.timezone).toBeUndefined()
    expect(target.geo_status).toBeUndefined()
  })
})
