import assert from 'node:assert/strict'
import test from 'node:test'
import { verifyDesktopVersion } from './desktop-version-acceptance.mjs'

function fixture({ serverVersion = '0.1.48', electronVersion = '0.1.48', manager = 'desktop', status = 200 } = {}) {
  const calls = []
  const request = async (url, options) => {
    calls.push({ url: String(url), options })
    const body = url.pathname === '/health' ? { status: 'ok', version: serverVersion } : { manager, current_version: electronVersion }
    return new globalThis.Response(JSON.stringify(body), { status: url.pathname === '/health' ? 200 : status })
  }
  return { calls, request }
}

test('checks running Electron and Go versions through existing read-only endpoints', async () => {
  const { calls, request } = fixture()
  assert.deepEqual(await verifyDesktopVersion('http://127.0.0.1:1234', 'fixture-cookie', '0.1.48', { request }), { electron_version: '0.1.48', server_version: '0.1.48' })
  assert.deepEqual(calls.map(call => call.url).sort(), ['http://127.0.0.1:1234/api/v1/updates', 'http://127.0.0.1:1234/health'])
  assert.deepEqual(calls.find(call => call.url.endsWith('/updates')).options.headers, { Cookie: 'fixture-cookie' })
  assert.ok(calls.every(call => !call.options.method || call.options.method === 'GET'))
})

test('a matching Server cannot hide stale Electron package metadata', async () => {
  await assert.rejects(verifyDesktopVersion('http://127.0.0.1:1234', 'fixture-cookie', '0.1.48', fixture({ electronVersion: '0.1.13' })), /Electron App version differs/)
})

test('a matching Electron cannot hide an old bundled Go program', async () => {
  await assert.rejects(verifyDesktopVersion('http://127.0.0.1:1234', 'fixture-cookie', '0.1.48', fixture({ serverVersion: '0.1.13' })), /Go Server version differs/)
})

test('manual Server metadata and rejected bridge requests are not Desktop evidence', async () => {
  await assert.rejects(verifyDesktopVersion('http://127.0.0.1:1234', 'fixture-cookie', '0.1.48', fixture({ manager: 'manual' })), /running Electron update bridge/)
  await assert.rejects(verifyDesktopVersion('http://127.0.0.1:1234', 'fixture-cookie', '0.1.48', fixture({ status: 401 })), /HTTP 401/)
})
