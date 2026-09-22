import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { CodexTurnStateRouteEvidence } from '@/api/admin/accounts'
import CodexTurnStateRouteDetails from '../CodexTurnStateRouteDetails.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : '') }) }))

describe('Codex package route diagnostics', () => {
  it('shows a matched Lite package and actual proxy without exposing private binding fields', () => {
    const evidence = { wire_mode: 'lite', actual_proxy_id: 8, route_source: 'bundle', bundle_proxy_id: 8,
      route_generation: 'private-generation', proxy_url: 'https://user:password@private-host', token: 'private-token', cookies: 'private-cookie' } as CodexTurnStateRouteEvidence
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence, proxyNames: { 8: 'Issuing proxy' } } })
    expect(wrapper.get('[data-testid="codex-route-actual-proxy"]').text()).toBe('Issuing proxy')
    expect(wrapper.get('[data-testid="codex-route-bundle-proxy"]').text()).toBe('Issuing proxy')
    expect(wrapper.get('[data-testid="codex-route-source"]').text()).toContain('routeSources.bundle')
    expect(wrapper.get('[data-testid="codex-route-wire-mode"]').text()).toContain('wireModes.lite')
    expect(wrapper.text()).not.toContain('private-')
    expect(wrapper.text()).not.toContain('password')
  })

  it.each([undefined, null, -1, Number.NaN, Number.POSITIVE_INFINITY])('does not treat missing or invalid proxy ID %s as direct', (id) => {
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence: { actual_proxy_id: id, bundle_proxy_id: id } } })
    expect(wrapper.get('[data-testid="codex-route-actual-proxy"]').text()).toContain('diagnosticUnknown')
    expect(wrapper.get('[data-testid="codex-route-bundle-proxy"]').text()).toContain('diagnosticUnknown')
    expect(wrapper.text()).not.toContain('proxyDirect')
  })

  it('recognizes explicit direct routing independently of package absence', () => {
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence: { actual_proxy_id: 0, route_source: 'account', wire_mode: 'responses' } } })
    expect(wrapper.get('[data-testid="codex-route-actual-proxy"]').text()).toContain('proxyDirect')
    expect(wrapper.get('[data-testid="codex-route-bundle-proxy"]').text()).toContain('diagnosticUnknown')
  })

  it('recognizes a direct learned package only from explicit zero IDs', () => {
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence: { actual_proxy_id: 0, bundle_proxy_id: 0, route_source: 'bundle' } } })
    expect(wrapper.get('[data-testid="codex-route-actual-proxy"]').text()).toContain('proxyDirect')
    expect(wrapper.get('[data-testid="codex-route-bundle-proxy"]').text()).toContain('proxyDirect')
  })

  it('shows the collector send route without claiming an existing package was selected', () => {
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence: { wire_mode: 'lite', actual_proxy_id: 9, route_source: 'collector' }, proxyNames: { 9: 'Collection proxy' } } })
    expect(wrapper.get('[data-testid="codex-route-actual-proxy"]').text()).toBe('Collection proxy')
    expect(wrapper.get('[data-testid="codex-route-source"]').text()).toContain('routeSources.collector')
    expect(wrapper.get('[data-testid="codex-route-bundle-proxy"]').text()).toContain('diagnosticUnknown')
  })

  it('does not render unrecognized wire modes, route sources or proxy strings', () => {
    const secret = 'https://private-user:private-password@private-host'
    const wrapper = mount(CodexTurnStateRouteDetails, { props: { evidence: {
      wire_mode: secret, route_source: secret, actual_proxy_id: secret, bundle_proxy_id: secret,
    } as unknown as CodexTurnStateRouteEvidence } })
    expect(wrapper.text()).toContain('wireModes.unknown')
    expect(wrapper.text()).toContain('routeSources.unknown')
    expect(wrapper.text()).not.toContain('private-')
  })
})
