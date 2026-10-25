import { describe, expect, it } from 'vitest'
import { desktopRequestHeaders, externalURL, trustedSender } from './policy'

describe('desktop native boundary', () => {
  it('attaches process identity only to managed-origin trusted main-frame requests', () => {
    const server = 'http://127.0.0.1:4545'
    const frame = { url: `${server}/settings/security`, parent: null }
    const request = { url: `${server}/auth/status`, method: 'GET', resourceType: 'xhr', webContentsId: 7, frame, requestHeaders: {} }
    expect(desktopRequestHeaders(request, 7, server, 'secret')).toEqual({ Authorization: 'Desktop secret' })
    for (const change of [
      { url: 'https://evil.example' }, { url: 'http://127.0.0.1:4546/api/v1/bootstrap' },
      { webContentsId: 8 }, { frame: null }, { frame: { url: 'https://evil.example', parent: null } },
      { frame: { url: `${server}/file`, parent: frame } },
    ]) expect(desktopRequestHeaders({ ...request, ...change }, 7, server, 'secret')).toEqual({})
  })
  it('strips credentials case-insensitively across redirects and limits file child frames', () => {
    const server = 'http://127.0.0.1:4545'
    const main = { url: server, parent: null }
    const request = { url: `${server}/api/v1/files/file_1/content`, method: 'GET', resourceType: 'subFrame', webContentsId: 7, frame: { url: 'about:blank', parent: main }, requestHeaders: {} }
    expect(desktopRequestHeaders(request, 7, server, 'secret').Authorization).toBe('Desktop secret')
    expect(desktopRequestHeaders({ ...request, url: `${server}/api/v1/bootstrap` }, 7, server, 'secret')).toEqual({})
    expect(desktopRequestHeaders({ ...request, frame: { url: 'https://evil.example', parent: main } }, 7, server, 'secret')).toEqual({})
    expect(desktopRequestHeaders({ ...request, url: 'https://evil.example', requestHeaders: { authorization: 'Desktop secret' } }, 7, server, 'secret')).toEqual({})
  })
  it('accepts the local app main frame and rejects foreign origins and child frames', () => {
    expect(trustedSender('http://127.0.0.1:4545/settings/general', null, 'http://127.0.0.1:4545')).toBe(true)
    for (const url of ['http://127.0.0.1:4546', 'https://example.org', 'file:///tmp/page.html', 'not a url', undefined]) {
      expect(trustedSender(url, null, 'http://127.0.0.1:4545')).toBe(false)
    }
    expect(trustedSender('http://127.0.0.1:4545', {}, 'http://127.0.0.1:4545')).toBe(false)
  })
  it('opens web and mail links without exposing arbitrary OS protocols', () => {
    expect(externalURL('https://example.org/report?q=1')).toBe('https://example.org/report?q=1')
    expect(externalURL('mailto:owner@example.org')).toBe('mailto:owner@example.org')
    for (const url of ['file:///etc/passwd', 'javascript:alert(1)', 'ssh://host', 'https://user:secret@example.org', '', null]) {
      expect(() => externalURL(url)).toThrow()
    }
  })
})
