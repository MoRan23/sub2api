import { describe, expect, it } from 'vitest'
import { buildProxyUrl, parseProxyUrl } from '@/utils/proxyUrl'

describe('proxy batch URL parsing (IPv6 support)', () => {
  it.each([
    ['socks5://[2001:db8::1]:1080', true],
    ['socks5h://[2001:db8::1]:1080', true],
    ['http://[::1]:8080', true],
    ['socks5://user:pass@[2001:db8::1]:1080', true],
    ['socks5://proxy.example.com:1080', true],
    ['http://192.168.1.1:8080', true],
    ['socks5://user:pass@proxy.example.com:1080', true],
    // bare IPv6 without brackets is ambiguous with host:port — rejected
    ['socks5://2001:db8::1:1080', false],
    // unsupported schemes / malformed ports stay invalid
    ['ftp://example.com:21', false],
    ['socks5://example.com:port', false]
  ])('%s => %s', (line, expected) => {
    expect(parseProxyUrl(line) !== null).toBe(expected)
  })

  it('extracts bare IPv6 host without brackets', () => {
    const url = 'socks5://user:pass@[2001:db8::1]:1080'
    const parsed = parseProxyUrl(url)!
    expect(parsed).toEqual({ protocol: 'socks5', username: 'user', password: 'pass', host: '2001:db8::1', port: 1080 })
    expect(buildProxyUrl(parsed)).toBe(url)
  })
})
