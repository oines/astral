import { describe, expect, it } from 'vitest'
import { computerServerAddresses, hostnameSettingsIssue, localizeHostnameError, sameHostnames } from './network-access'

const discovered = [
  { url: 'http://100.90.80.70:4545', label: 'Tailscale', kind: 'tailscale' },
  { url: 'http://192.168.1.10:4545', label: 'LAN', kind: 'lan' },
]

describe('Computer enrollment address choices', () => {
  it('offers the current remote browser origin first without requiring a global address setting', () => {
    const choices = computerServerAddresses(discovered, 'https://astral.example', false, 'Current browser address')
    expect(choices).toEqual([{ url: 'https://astral.example', label: 'Current browser address', kind: 'browser' }, ...discovered])
    expect(discovered).toHaveLength(2)
  })

  it('does not duplicate a browser address already found by discovery', () => {
    const choices = computerServerAddresses(discovered, 'http://192.168.1.10:4545', false, 'Current browser address')
    expect(choices.map(address => address.url)).toEqual(['http://192.168.1.10:4545', 'http://100.90.80.70:4545'])
  })

  it.each(['http://localhost:4545', 'http://astral.localhost:4545', 'http://127.0.0.1:4545', 'http://127.0.0.2:4545', 'http://[::1]:4545', 'http://0.0.0.0:4545', 'http://[::]:4545'])('keeps remote discovery candidates when the browser uses %s', origin => {
    expect(computerServerAddresses(discovered, origin, false, 'Current browser address')).toEqual(discovered)
  })

  it('keeps Desktop discovery order and ignores unusable browser origins', () => {
    expect(computerServerAddresses(discovered, 'https://astral.example', true, 'Current browser address')).toEqual(discovered)
    expect(computerServerAddresses(discovered, 'not a URL', false, 'Current browser address')).toEqual(discovered)
    expect(computerServerAddresses(discovered, 'file:///app/index.html', false, 'Current browser address')).toEqual(discovered)
  })
})

describe('hostname save reconciliation', () => {
  it('matches acknowledged normalization of case, final dots, IDNs and IPv6', () => {
    expect(sameHostnames([' ASTRAL.example. ', 'BÜCHER.example', '[2001:0db8::1]'], ['2001:db8::1', 'astral.example', 'xn--bcher-kva.example'])).toBe(true)
    expect(sameHostnames(['astral.example', 'astral.example'], ['astral.example'])).toBe(true)
    expect(sameHostnames(['[::ffff:192.168.1.10]'], ['192.168.1.10'])).toBe(true)
    expect(sameHostnames(['127.1'], ['127.0.0.1'])).toBe(false)
    expect(sameHostnames(['192.168.001.10'], ['192.168.1.10'])).toBe(false)
  })

  it.each(['https://astral.example', 'astral.example:4545', 'astral.example/path', '*.astral.example', 'user@astral.example', 'astral.example?query', 'astral.example#fragment', '[astral.example]', '%61stral.example', 'astral.example\\'])('does not mistake rejected input %s for a saved hostname', input => {
    expect(sameHostnames([input], ['astral.example'])).toBe(false)
  })
})

describe('hostname settings validation', () => {
  it('requires a usable list only while restriction is enabled', () => {
    expect(hostnameSettingsIssue([], false, 'astral.example')).toBeUndefined()
    expect(hostnameSettingsIssue([], true, 'astral.example')?.key).toBe('networkHostnamesRequired')
    expect(hostnameSettingsIssue(['other.example'], true, 'astral.example')).toEqual({ key: 'networkCurrentHostnameRequired', values: { hostname: 'astral.example' } })
    expect(hostnameSettingsIssue(['BÜCHER.example.'], true, 'xn--bcher-kva.example')).toBeUndefined()
  })
  it.each(['https://astral.example', 'astral.example:4545', 'astral.example/path', '*.astral.example', '[astral.example]'])('identifies an input-format mistake before submitting %s', hostname => {
    expect(hostnameSettingsIssue([hostname], false, '')?.key).toBe('networkHostnameFormatError')
  })
  it('localizes only recognized definite hostname rejections and leaves uncertain transport errors intact', () => {
    const format = { code: 'validation', message: 'Enter a hostname or IP address without a protocol, port, path or wildcard.' }
    expect(localizeHostnameError(format, key => key)).toEqual({ code: 'validation', message: 'networkHostnameFormatError' })
    const current = { code: 'validation', message: 'Keep the hostname used by this browser in the allowed list.' }
    expect(localizeHostnameError(current, (key, values) => `${key}:${values?.hostname}`, 'astral.example')).toEqual({ code: 'validation', message: 'networkCurrentHostnameRequired:astral.example' })
    const transport = new TypeError('connection lost')
    expect(localizeHostnameError(transport, key => key)).toBe(transport)
    const other = { code: 'unavailable', message: 'Disk full' }
    expect(localizeHostnameError(other, key => key)).toBe(other)
  })
})
