// Disposable CI fixtures only. These public test credentials never configure a
// user's installation; callers supply their freshly-created temporary data root.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import https from 'node:https'
import { createHash, timingSafeEqual, X509Certificate } from 'node:crypto'
import { isIP } from 'node:net'

const account = { username: 'ci-administrator', password: 'Astral-disposable-CI-password-42!' }

// The private control endpoint is HTTPS with an explicitly trusted local
// certificate and SPKI pin. Node completes both checks before sending HTTP
// headers/body, including the local control token and administrator password.
export async function configureWebFixtureAdministrator(dataDirectory, control, credentials = account) {
  const endpoint = new URL(control.url)
  const hostname = endpoint.hostname.replace(/^\[|\]$/g, '')
  const loopback = hostname === '::1' || (isIP(hostname) === 4 && hostname.startsWith('127.'))
  assert.ok(endpoint.protocol === 'https:' && loopback && !endpoint.username && !endpoint.password && !endpoint.search && !endpoint.hash && endpoint.pathname === '/', 'Invalid private control HTTPS endpoint')
  assert.match(control.server_pin, /^[a-f0-9]{64}$/)
  assert.match(control.token, /^[a-f0-9]{64}$/)
  const certificate = new X509Certificate(await readFile(path.join(dataDirectory, 'computer-identity', 'identity.pem')))
  const expected = Buffer.from(control.server_pin, 'hex')
  const certificatePin = createHash('sha256').update(certificate.publicKey.export({ type: 'spki', format: 'der' })).digest()
  assert.ok(timingSafeEqual(certificatePin, expected), 'Private control pin does not match its local certificate')
  const body = JSON.stringify(credentials)
  return new Promise((resolve, reject) => {
    const request = https.request(new URL('/setup', endpoint), {
      method: 'POST', agent: false, ca: certificate.toString(), rejectUnauthorized: true,
      minVersion: 'TLSv1.3', maxVersion: 'TLSv1.3', ALPNProtocols: ['http/1.1'],
      checkServerIdentity: (_hostname, peer) => {
        const actual = createHash('sha256').update(new X509Certificate(peer.raw).publicKey.export({ type: 'spki', format: 'der' })).digest()
        if (!timingSafeEqual(actual, expected)) return new Error('Private control Server identity mismatch')
      },
      headers: { Authorization: `LocalControl ${control.token}`, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) },
      signal: AbortSignal.timeout(10_000),
    }, response => {
      response.resume()
      response.once('end', () => resolve(response.statusCode))
      response.once('error', reject)
    })
    request.once('error', reject)
    request.end(body)
  })
}

export async function webFixtureCookie(dataDirectory, serverURL) {
  const status = await fetch(`${serverURL}/auth/status`, { signal: AbortSignal.timeout(5000) })
  assert.ok(status.ok)
  if (!(await status.json()).configured) {
    const control = JSON.parse(await readFile(path.join(dataDirectory, 'control.json'), 'utf8'))
    const created = await configureWebFixtureAdministrator(dataDirectory, control)
    assert.equal(created, 204, 'Temporary Web administrator initialization failed')
  }
  const login = await fetch(`${serverURL}/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'User-Agent': 'Astral disposable CI' },
    body: JSON.stringify(account), signal: AbortSignal.timeout(10_000),
  })
  assert.equal(login.status, 200, 'Temporary Web administrator login failed')
  const result = await login.json()
  assert.equal(result.authenticated, true)
  const cookieName = new URL(serverURL).protocol === 'https:' ? '__Host-astral_session' : 'astral_session'
  const cookie = login.headers.getSetCookie().find(value => value.startsWith(`${cookieName}=`))
  assert.ok(cookie, 'Web login did not issue an independent session')
  return cookie.split(';', 1)[0]
}
