import { afterEach, describe, expect, it, vi } from 'vitest'
import { authAPI, authRequest, creationOptions, credentialJSON, getPasskey, passkeySupported, requestOptions, sessionDevice } from './auth'

afterEach(() => vi.unstubAllGlobals())
describe('browser authentication transport', () => {
  it('uses HttpOnly cookie sessions without persisting or sending a bearer token', async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({ authenticated: true }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    await authAPI.login('alice', 'correct password')
    expect(fetch).toHaveBeenCalledWith('/auth/login', expect.objectContaining({ credentials: 'same-origin', body: JSON.stringify({ username: 'alice', password: 'correct password' }) }))
    const init = fetch.mock.calls[0]![1] as RequestInit
    expect(init.headers).toEqual({ 'Content-Type': 'application/json' })
  })
  it('does not destroy the existing session when reauthentication has an incorrect password', async () => {
    const window = new EventTarget()
    const unauthorized = vi.fn()
    window.addEventListener('astral:unauthorized', unauthorized)
    vi.stubGlobal('window', window)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ code: 'invalid_credentials', message: 'Incorrect' }), { status: 401 })))
    await expect(authAPI.reauthenticate('wrong')).rejects.toMatchObject({ code: 'invalid_credentials' })
    expect(unauthorized).not.toHaveBeenCalled()
  })
  it('returns to sign-in when an authenticated API rejects a revoked session', async () => {
    const window = new EventTarget()
    const unauthorized = vi.fn()
    window.addEventListener('astral:unauthorized', unauthorized)
    vi.stubGlobal('window', window)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ code: 'unauthorized', message: 'Sign in' }), { status: 401 })))
    await expect(authRequest('/api/v1/auth')).rejects.toMatchObject({ code: 'unauthorized' })
    expect(unauthorized).toHaveBeenCalledOnce()
  })
})
describe('Passkey ceremonies', () => {
  it('rejects HTTP including the localhost exception and browsers without WebAuthn', () => {
    expect(passkeySupported({ protocol: 'http:' }, class {})).toBe(false)
    expect(passkeySupported({ protocol: 'https:' }, undefined)).toBe(false)
    expect(passkeySupported({ protocol: 'https:' }, class {})).toBe(true)
  })
  it('does not start a browser ceremony on HTTP', async () => {
    const get = vi.fn()
    vi.stubGlobal('window', { location: { protocol: 'http:' }, PublicKeyCredential: class {} })
    vi.stubGlobal('navigator', { credentials: { get } })
    await expect(getPasskey({ challenge_id: 'x', public_key: {} })).rejects.toMatchObject({ code: 'passkey_unavailable' })
    expect(get).not.toHaveBeenCalled()
  })
  it('converts WebAuthn options and requires discoverable credentials and user verification', () => {
    const options = creationOptions({ challenge: 'AQID', user: { id: 'BAUG', name: 'alice', displayName: 'Alice' }, excludeCredentials: [{ id: '-_8', type: 'public-key' }], authenticatorSelection: { residentKey: 'discouraged', userVerification: 'preferred' } })
    expect(Array.from(new Uint8Array(options.challenge as ArrayBuffer))).toEqual([1, 2, 3])
    expect(Array.from(new Uint8Array(options.user.id as ArrayBuffer))).toEqual([4, 5, 6])
    expect(options.authenticatorSelection).toMatchObject({ residentKey: 'required', userVerification: 'required' })
    expect(requestOptions({ challenge: 'AQID', userVerification: 'discouraged' }).userVerification).toBe('required')
  })
  it('serializes binary assertions without corrupting base64url credentials', () => {
    const bytes = new Uint8Array([251, 255]).buffer
    const json = credentialJSON({ id: '-_8', type: 'public-key', rawId: bytes, response: { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: null }, getClientExtensionResults: () => ({}), authenticatorAttachment: 'platform' } as unknown as PublicKeyCredential)
    expect(json).toMatchObject({ id: '-_8', rawId: '-_8', response: { clientDataJSON: '-_8', authenticatorData: '-_8', signature: '-_8', userHandle: null } })
  })
})
it('names browser sessions without calling them physical computers', () => {
  expect(sessionDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X) AppleWebKit Safari/605')).toBe('Safari · macOS')
  expect(sessionDevice('Mozilla/5.0 (Windows NT 10) Chrome/120 Edg/120')).toBe('Edge · Windows')
  expect(sessionDevice('')).toBe('')
})
