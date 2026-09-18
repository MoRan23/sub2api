import type { Proxy, ProxyTestResult } from '@/types'

type ProbeResult = Omit<ProxyTestResult, 'message'> & { message?: string }
const geoFields = ['country', 'country_code', 'region', 'city', 'timezone', 'geo_status', 'geo_reason', 'geo_checked_at'] as const

/** New responses contain a complete geography snapshot; old responses may omit fields. */
export function applyProxyProbeResult(target: Proxy, result: ProbeResult): void {
  target.latency_status = result.success ? 'success' : 'failed'
  target.latency_ms = result.success ? result.latency_ms : undefined
  target.latency_message = result.message

  if (result.ip_address && result.ip_address !== target.ip_address) {
    for (const field of geoFields) delete target[field]
  }
  if (result.geo_status !== undefined || result.ip_address) target.ip_address = result.ip_address

  // Explicit status makes omitted fields authoritative too: a changed IP or
  // expired geography must not retain location details from the previous result.
  for (const field of geoFields) {
    if (result.geo_status !== undefined || result[field] !== undefined) {
      Object.assign(target, { [field]: result[field] })
    }
  }
}
