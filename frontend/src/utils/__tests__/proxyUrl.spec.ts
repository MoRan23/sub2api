import { describe, expect, it } from 'vitest'
import { buildProxyUrl, parseProxyUrl, shadowsocksCredentialError, shadowsocksMethods } from '../proxyUrl'

const key128 = btoa('0123456789abcdef')
const key256 = btoa('0123456789abcdef0123456789abcdef')

describe('Shadowsocks proxy URLs', () => {
  it.each(shadowsocksMethods)('round trips %s without changing the password or IPv6', (method) => {
    const password = method.startsWith('2022-') ? (method.includes('aes-128') ? key128 : key256) : ' 密码:@?#%/ + '
    const proxy = { protocol: 'ss' as const, host: '2001:db8::1', port: 8388, username: method, password, name: '节点 / #1' }
    expect(parseProxyUrl(buildProxyUrl(proxy))).toEqual(proxy)
  })

  it('decodes AEAD Base64URL userinfo once and preserves special password characters', () => {
    const password = ' a%20:b@c '
    const userinfo = btoa(`aes-256-gcm:${password}`).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
    expect(parseProxyUrl(`ss://${userinfo}@ss.example:8388/#test`)).toEqual({
      protocol: 'ss', host: 'ss.example', port: 8388, username: 'aes-256-gcm', password, name: 'test'
    })
  })

  it.each([
    'ss://aes-256-gcm:secret@ss.example:8388/?plugin=v2ray-plugin',
    'ss://aes-256-gcm:secret@ss.example:8388?anything=1',
    'ss://aes-256-gcm:secret@ss.example:8388?',
    'ss://aes-256-gcm:secret@ss.example:8388/path',
    'ss://aes-256-gcm:secret@ss.example:0',
    'ss://aes-256-gcm:secret@ss.example:65536',
    'ss://rc4-md5:secret@ss.example:8388',
    'ss://aes-256-gcm:@ss.example:8388',
    'ss://aes-256-gcm:%zz@ss.example:8388',
    'ss://aes-256-gcm:secret@2001:db8::1:8388',
    `ss://${btoa(`2022-blake3-aes-128-gcm:${key128}`)}@ss.example:8388`,
    `ss://2022-blake3-aes-256-gcm:${encodeURIComponent(key128)}@ss.example:8388`,
    `ss://2022-blake3-aes-128-gcm:${encodeURIComponent(`${key128}:${key128}`)}@ss.example:8388`,
  ])('rejects unsupported or malformed input #%#', (input) => {
    expect(parseProxyUrl(input)).toBeNull()
  })

  it('reports a safe key error without including input', () => {
    expect(shadowsocksCredentialError('2022-blake3-aes-128-gcm', 'secret')).toBe('ssKeyInvalid')
    expect(shadowsocksCredentialError('aes-128-gcm', ' ')).toBeNull()
    expect(shadowsocksCredentialError('aes-128-gcm', '')).toBe('ssPasswordRequired')
  })

  it('does not decode percent escapes twice in plain userinfo', () => {
    expect(parseProxyUrl('ss://aes-128-gcm:%2520@ss.example:8388')?.password).toBe('%20')
  })

  it('requires canonical padded Base64 for SS2022', () => {
    expect(shadowsocksCredentialError('2022-blake3-aes-128-gcm', key128.replace(/=+$/, ''))).toBe('ssKeyInvalid')
    expect(shadowsocksCredentialError('2022-blake3-aes-128-gcm', ` ${key128}`)).toBe('ssKeyInvalid')
    expect(shadowsocksCredentialError('2022-blake3-aes-128-gcm', key128.replace('Zg==', 'Zh=='))).toBe('ssKeyInvalid')
  })

  it('checks the storage limit in UTF-8 bytes', () => {
    expect(shadowsocksCredentialError('aes-128-gcm', '中'.repeat(33))).toBeNull()
    expect(shadowsocksCredentialError('aes-128-gcm', '中'.repeat(34))).toBe('ssPasswordTooLong')
  })
})
