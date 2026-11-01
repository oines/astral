import type { AstralInternalHttpapiNetworkAddress as NetworkAddress } from '@astral/sdk'

// Compare an acknowledged value with its input without silently accepting a
// URL, port or path as a different allowed hostname.
function hostnameKey(raw: string): string {
  const value = raw.trim().toLowerCase().replace(/\.$/, '')
  if (!value || /[\s\\/@?#%*]/.test(value)) return value
  try {
    const parsed = new URL(`http://${value.includes(':') && !value.startsWith('[') ? `[${value}]` : value}`)
    if (parsed.port || parsed.pathname !== '/' || parsed.username || parsed.password) return value
    // URL also expands legacy IPv4 spellings such as 127.1. The Server keeps
    // those as written instead of treating them as canonical IP literals.
    if (!value.includes(':') && Array.from(value).every(character => character.charCodeAt(0) < 128)) return value
    const hostname = parsed.hostname.replace(/^\[|\]$/g, '')
    const mapped = /^::ffff:([\da-f]+):([\da-f]+)$/.exec(hostname)
    if (mapped) {
      const high = parseInt(mapped[1]!, 16), low = parseInt(mapped[2]!, 16)
      return `${high >>> 8}.${high & 255}.${low >>> 8}.${low & 255}`
    }
    return hostname
  } catch { return value }
}

export function sameHostnames(left: string[], right: string[]): boolean {
  const keys = (values: string[]) => [...new Set(values.map(hostnameKey))].sort()
  const a = keys(left), b = keys(right)
  return a.length === b.length && a.every((value, index) => value === b[index])
}

type HostnameIssue = { key: string; values?: Record<string, string> }

export function hostnameSettingsIssue(hostnames: string[], restricted: boolean, currentHostname: string): HostnameIssue | undefined {
  for (const raw of hostnames) {
    const value = raw.trim()
    if (!value || /[\s\\/@?#%*]/.test(value)) return { key: 'networkHostnameFormatError' }
    try {
      const parsed = new URL(`http://${value.includes(':') && !value.startsWith('[') ? `[${value}]` : value}`)
      if (parsed.port || parsed.pathname !== '/' || parsed.username || parsed.password) return { key: 'networkHostnameFormatError' }
    } catch { return { key: 'networkHostnameFormatError' } }
  }
  if (!restricted) return
  if (!hostnames.length) return { key: 'networkHostnamesRequired' }
  if (currentHostname && !hostnames.some(hostname => sameHostnames([hostname], [currentHostname]))) return { key: 'networkCurrentHostnameRequired', values: { hostname: currentHostname.replace(/^\[|\]$/g, '') } }
}

export function localizeHostnameError(error: unknown, message: (key: string, values?: Record<string, string>) => string, currentHostname = ''): unknown {
  if (!error || typeof error !== 'object' || !('code' in error) || !('message' in error) || typeof error.message !== 'string') return error
  if (error.message.startsWith('Enter a hostname or IP address without')) return { ...error, message: message('networkHostnameFormatError') }
  if (error.message.startsWith('Keep at least one allowed hostname') || error.message.startsWith('Add at least one allowed hostname')) return { ...error, message: message('networkHostnamesRequired') }
  if (currentHostname && error.message.startsWith('Keep the hostname used by this browser')) return { ...error, message: message('networkCurrentHostnameRequired', { hostname: currentHostname.replace(/^\[|\]$/g, '') }) }
  return error
}

export function computerServerAddresses(addresses: NetworkAddress[], currentOrigin: string, desktop: boolean, currentLabel: string): NetworkAddress[] {
  const choices = [...addresses]
  if (!desktop) {
    try {
      const current = new URL(currentOrigin)
      const hostname = current.hostname.toLowerCase().replace(/\.$/, '').replace(/^\[|\]$/g, '')
      const local = hostname === 'localhost' || hostname.endsWith('.localhost') || /^127\./.test(hostname) || hostname === '::1' || hostname === '0.0.0.0' || hostname === '::'
      if (['http:', 'https:'].includes(current.protocol) && !local && current.origin !== 'null') {
        choices.unshift({ url: current.origin, label: currentLabel, kind: 'browser' })
      }
    } catch { /* Discovery remains available without a usable browser origin. */ }
  }
  const seen = new Set<string>()
  return choices.filter(address => {
    if (seen.has(address.url)) return false
    seen.add(address.url)
    return true
  })
}
