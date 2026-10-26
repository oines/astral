import type {
  AstralInternalAuthCeremony,
  AstralInternalAuthLoginResult,
  AstralInternalAuthPasskeyView,
  AstralInternalAuthSessionView,
  AstralInternalAuthStatus,
  AstralInternalAuthTotpSetup,
  AstralInternalAuthView,
} from '@astral/sdk'

export type AuthStatus = AstralInternalAuthStatus
export type PasskeyView = AstralInternalAuthPasskeyView
export type WebSession = AstralInternalAuthSessionView
export type AuthSettings = AstralInternalAuthView
export type LoginResult = AstralInternalAuthLoginResult
export type Ceremony = Omit<AstralInternalAuthCeremony, 'public_key'> & { public_key: Record<string, unknown> }
export type TOTPSetup = AstralInternalAuthTotpSetup
export class AuthError extends Error {
  constructor(public code: string, message: string, public status: number) { super(message); this.name = 'AuthError' }
}

export async function authRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await response.json().catch(() => undefined)
  if (!response.ok) {
    const code = typeof data?.code === 'string' ? data.code : 'request_failed'
    if (response.status === 401 && code !== 'invalid_credentials' && path.startsWith('/api/')) window.dispatchEvent(new Event('astral:unauthorized'))
    throw new AuthError(code, typeof data?.message === 'string' ? data.message : `Request failed (${response.status}).`, response.status)
  }
  return data as T
}

export const authAPI = {
  status: () => authRequest<AuthStatus>('/auth/status'),
  settings: () => authRequest<AuthSettings>('/api/v1/auth'),
  login: (username: string, password: string) => authRequest<LoginResult>('/auth/login', 'POST', { username, password }),
  reauthenticatePasskey: () => authRequest<LoginResult>('/api/v1/auth/reauth', 'POST', { method: 'passkey' }),
  reauthenticate: (password: string) => authRequest<LoginResult>('/api/v1/auth/reauth', 'POST', { password }),
  totp: (challenge_id: string, code: string) => authRequest<LoginResult>('/auth/login/totp', 'POST', { challenge_id, code }),
  recovery: (challenge_id: string, code: string) => authRequest<LoginResult>('/auth/login/recovery', 'POST', { challenge_id, code }),
  passkeyLoginBegin: (challenge_id?: string) => authRequest<Ceremony>('/auth/login/passkey/begin', 'POST', { challenge_id }),
  passkeyLoginFinish: (challenge_id: string, credential: unknown) => authRequest<LoginResult>('/auth/login/passkey/finish', 'POST', { challenge_id, credential }),
  logout: () => authRequest<{ ok: boolean }>('/auth/session', 'DELETE'),
  account: (username: string, password = '') => authRequest<{ ok: boolean }>('/api/v1/auth/account', 'POST', { username, password }),
  totpBegin: () => authRequest<TOTPSetup>('/api/v1/auth/totp/begin', 'POST', {}),
  totpConfirm: (setup_id: string, code: string) => authRequest<{ codes: string[] }>('/api/v1/auth/totp/confirm', 'POST', { setup_id, code }),
  totpRemove: () => authRequest<{ ok: boolean }>('/api/v1/auth/totp', 'DELETE'),
  passkeyBegin: (name: string) => authRequest<Ceremony>('/api/v1/auth/passkeys/begin', 'POST', { name }),
  passkeyFinish: (challenge_id: string, credential: unknown) => authRequest<PasskeyView>('/api/v1/auth/passkeys/finish', 'POST', { challenge_id, credential }),
  passkeyRemove: (id: string) => authRequest<{ ok: boolean }>(`/api/v1/auth/passkeys/${encodeURIComponent(id)}`, 'DELETE'),
  passwordless: (enabled: boolean) => authRequest<AuthSettings>('/api/v1/auth/passwordless', 'PATCH', { enabled }),
  recoveryCodes: () => authRequest<{ codes: string[] }>('/api/v1/auth/recovery-codes', 'POST', {}),
  revokeSession: (id: string) => authRequest<{ ok: boolean }>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, 'DELETE'),
  revokeSessions: () => authRequest<{ ok: boolean }>('/api/v1/auth/sessions', 'DELETE'),
}

export function passkeySupported(location: Pick<Location, 'protocol'> = window.location, credentialType: unknown = typeof window === 'undefined' ? undefined : window.PublicKeyCredential) {
  // Product policy deliberately excludes the browser's localhost HTTP exception.
  return location.protocol === 'https:' && typeof credentialType === 'function'
}
function decode(value: unknown): ArrayBuffer {
  if (typeof value !== 'string') throw new Error('Invalid WebAuthn binary value.')
  const binary = atob(value.replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(binary, char => char.charCodeAt(0)).buffer
}
function encode(value: ArrayBuffer | null): string | null {
  if (value === null) return null
  return btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
function descriptors(value: unknown) {
  return (value as Array<Record<string, unknown>> | undefined)?.map(item => ({ ...item, id: decode(item.id) }))
}
export function creationOptions(value: Record<string, unknown>): PublicKeyCredentialCreationOptions {
  const user = value.user as Record<string, unknown>
  return { ...value, challenge: decode(value.challenge), user: { ...user, id: decode(user.id) }, excludeCredentials: descriptors(value.excludeCredentials), authenticatorSelection: { ...(value.authenticatorSelection as object), residentKey: 'required', requireResidentKey: true, userVerification: 'required' } } as PublicKeyCredentialCreationOptions
}
export function requestOptions(value: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  return { ...value, challenge: decode(value.challenge), allowCredentials: descriptors(value.allowCredentials), userVerification: 'required' } as PublicKeyCredentialRequestOptions
}
export function credentialJSON(credential: PublicKeyCredential) {
  const response = credential.response
  const common = { id: credential.id, rawId: encode(credential.rawId), type: credential.type, authenticatorAttachment: credential.authenticatorAttachment, clientExtensionResults: credential.getClientExtensionResults() }
  if ('attestationObject' in response) {
    const attestation = response as AuthenticatorAttestationResponse
    return { ...common, response: { clientDataJSON: encode(response.clientDataJSON), attestationObject: encode(attestation.attestationObject), transports: attestation.getTransports?.() ?? [] } }
  }
  const assertion = response as AuthenticatorAssertionResponse
  return { ...common, response: { clientDataJSON: encode(response.clientDataJSON), authenticatorData: encode(assertion.authenticatorData), signature: encode(assertion.signature), userHandle: encode(assertion.userHandle) } }
}
export async function getPasskey(ceremony: Ceremony, signal?: AbortSignal) {
  if (!passkeySupported()) throw new AuthError('passkey_unavailable', 'Passkey requires HTTPS and a compatible browser.', 400)
  const credential = await navigator.credentials.get({ publicKey: requestOptions(ceremony.public_key), signal })
  if (!credential) throw new Error('Passkey authentication was cancelled.')
  return credentialJSON(credential as PublicKeyCredential)
}
export async function createPasskey(ceremony: Ceremony, signal?: AbortSignal) {
  if (!passkeySupported()) throw new AuthError('passkey_unavailable', 'Passkey requires HTTPS and a compatible browser.', 400)
  const credential = await navigator.credentials.create({ publicKey: creationOptions(ceremony.public_key), signal })
  if (!credential) throw new Error('Passkey creation was cancelled.')
  return credentialJSON(credential as PublicKeyCredential)
}

export function sessionDevice(userAgent: string): string {
  const browser = /Edg\//.test(userAgent) ? 'Edge' : /Firefox\//.test(userAgent) ? 'Firefox' : /Chrome\//.test(userAgent) ? 'Chrome' : /Safari\//.test(userAgent) ? 'Safari' : ''
  const system = /Android/.test(userAgent) ? 'Android' : /iPhone|iPad/.test(userAgent) ? 'iOS' : /Windows/.test(userAgent) ? 'Windows' : /Macintosh|Mac OS X/.test(userAgent) ? 'macOS' : /Linux/.test(userAgent) ? 'Linux' : ''
  return [browser, system].filter(Boolean).join(' · ')
}
