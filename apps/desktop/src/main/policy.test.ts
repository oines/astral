import { describe, expect, it } from 'vitest'
import { externalURL, trustedSender } from './policy'

describe('desktop native boundary', () => {
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
