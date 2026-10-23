import type { ProxyProtocol } from '@/types'

export const shadowsocksMethods = [
  'aes-128-gcm', 'aes-192-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305',
  '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305'
] as const

export interface ProxyConnection {
  protocol: ProxyProtocol
  host: string
  port: number
  username?: string | null
  password?: string | null
  name?: string
}

export type ParsedProxyUrl = ProxyConnection & { username: string; password: string }

/** Validation messages are translation keys and never contain credentials. */
export function shadowsocksCredentialError(method: string, password?: string | null): string | null {
  if (!shadowsocksMethods.some(value => value === method)) return 'ssMethodInvalid'
  // An absent password is an untouched edit; the server checks the stored value.
  if (password == null) return null
  if (password.length === 0) return 'ssPasswordRequired'
  if (new TextEncoder().encode(password).length > 100) return 'ssPasswordTooLong'
  if (method.startsWith('2022-')) {
    const size = method === '2022-blake3-aes-128-gcm' ? 16 : 32
    try {
      const decoded = atob(password)
      if (decoded.length !== size || btoa(decoded) !== password) return 'ssKeyInvalid'
    } catch {
      return 'ssKeyInvalid'
    }
  }
  return null
}

/** SIP002 userinfo is decoded once; options we cannot execute are rejected. */
export function parseProxyUrl(line: string): ParsedProxyUrl | null {
  const input = line.trim()
  const scheme = input.match(/^(https?|socks5h?|ss):\/\//i)
  if (!scheme) return null
  const protocol = scheme[1].toLowerCase() as ProxyProtocol
  let address = input.slice(scheme[0].length)
  let name: string | undefined
  try {
    const hash = address.indexOf('#')
    if (hash >= 0) {
      name = decodeURIComponent(address.slice(hash + 1))
      address = address.slice(0, hash)
    }
    // Reject plugins, all unknown query options and paths instead of dropping them.
    if (address.includes('?')) return null
    const at = address.lastIndexOf('@')
    let userinfo = at >= 0 ? address.slice(0, at) : ''
    let authority = at >= 0 ? address.slice(at + 1) : address
    if (authority.endsWith('/')) authority = authority.slice(0, -1)
    const hostPort = authority.match(/^(\[[0-9a-f:.]+\]|[^\s:/@#[\]\\%]+):(\d+)$/i)
    if (!hostPort) return null
    const port = Number(hostPort[2])
    if (!Number.isInteger(port) || port < 1 || port > 65535) return null
    const host = hostPort[1].replace(/^\[|\]$/g, '')
    // Also validate IPv6 syntax, rather than only accepting its characters.
    if (hostPort[1].startsWith('[')) new URL(`http://${hostPort[1]}:${port}`)
    let encodedUserinfo = false
    if (protocol === 'ss' && !userinfo.includes(':')) {
      if (!/^[A-Za-z0-9+/_-]+={0,2}$/.test(userinfo)) return null
      const binary = atob(userinfo.replace(/-/g, '+').replace(/_/g, '/'))
      userinfo = new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(binary, char => char.charCodeAt(0)))
      encodedUserinfo = true
    }
    const colon = userinfo.indexOf(':')
    let username = colon < 0 ? userinfo : userinfo.slice(0, colon)
    let password = colon < 0 ? '' : userinfo.slice(colon + 1)
    if (!encodedUserinfo) {
      username = decodeURIComponent(username)
      password = decodeURIComponent(password)
    }
    if (protocol === 'ss') {
      if (colon < 0 || (encodedUserinfo && username.startsWith('2022-'))) return null
      if (shadowsocksCredentialError(username, password)) return null
    }
    return { protocol, host, port, username, password, ...(name ? { name } : {}) }
  } catch {
    return null
  }
}

export function proxyAuthority(proxy: Pick<ProxyConnection, 'host' | 'port'>): string {
  const host = proxy.host.replace(/^\[|\]$/g, '')
  return `${host.includes(':') ? `[${host}]` : host}:${proxy.port}`
}

/** Plain percent-encoded userinfo works for both AEAD and SS2022. */
export function buildProxyUrl(proxy: ProxyConnection): string {
  const user = encodeURIComponent(proxy.username || '')
  const pass = encodeURIComponent(proxy.password || '')
  const auth = pass || proxy.protocol === 'ss' ? `${user}:${pass}@` : user ? `${user}@` : ''
  const fragment = proxy.protocol === 'ss' && proxy.name ? `#${encodeURIComponent(proxy.name)}` : ''
  return `${proxy.protocol}://${auth}${proxyAuthority(proxy)}${fragment}`
}
