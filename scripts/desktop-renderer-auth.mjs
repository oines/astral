import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'

export class RendererNavigationError extends Error {}

function evaluationError(message, method) {
  const exception = message.result?.exceptionDetails
  const detail = message.error?.message || exception?.exception?.description || exception?.text
  if (!detail) return undefined
  const description = `Desktop renderer ${method} failed${message.error?.code ? ` (${message.error.code})` : ''}: ${detail}`
  // These are Chromium execution-context lifecycle failures. Arbitrary script,
  // network and authentication failures must remain fatal, not become retries.
  if (message.error?.code === -32000 && /^(Execution context was destroyed\b|Cannot find (?:default execution context|context with (?:specified )?id)\b|Inspected target navigated or closed\b)/.test(detail)) return new RendererNavigationError(description)
  return new Error(description)
}

export async function rendererCommand(endpoint, method, params, { WebSocket = globalThis.WebSocket } = {}) {
  const socket = new WebSocket(endpoint)
  let timer
  try {
    return await new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(new Error('The Desktop renderer authentication check timed out.')), 10_000)
      socket.addEventListener('error', () => reject(new Error('The temporary Electron CDP connection failed.')), { once: true })
      socket.addEventListener('close', () => reject(new Error('The temporary Electron CDP connection closed before authentication was checked.')), { once: true })
      socket.addEventListener('open', () => {
        socket.send(JSON.stringify({ id: 1, method, params }))
      }, { once: true })
      socket.addEventListener('message', event => {
        try {
          const message = JSON.parse(String(event.data))
          if (message.id !== 1) return
          const error = evaluationError(message, method)
          if (error) throw error
          assert.ok(message.result, 'The renderer returned no protocol result')
          resolve(message.result)
        } catch (error) { reject(error) }
      })
    })
  } finally {
    clearTimeout(timer)
    socket.close()
  }
}

export async function evaluateRenderer(endpoint, expression, options) {
  const result = await rendererCommand(endpoint, 'Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, options)
  assert.ok(result.result && Object.hasOwn(result.result, 'value'), 'The renderer returned no authentication result')
  return result.result.value
}

// Select only the managed page advertised by this disposable Electron profile.
// Screenshot/navigation checks must never accept an arbitrary caller-supplied CDP URL.
export async function managedRendererEndpoint(profile, serverURL, { diagnose = () => {} } = {}) {
  const portFile = await readFile(path.join(profile, 'DevToolsActivePort'), 'utf8').catch(error => {
    if (error.code === 'ENOENT') return ''
    throw error
  })
  if (!portFile) { diagnose('Waiting for Electron DevToolsActivePort.'); return false }
  const port = Number(portFile.split('\n', 1)[0])
  assert.ok(Number.isInteger(port) && port > 0 && port <= 65535, 'Electron reported an invalid debugging port')
  const response = await fetch(`http://127.0.0.1:${port}/json/list`, { signal: AbortSignal.timeout(5000) })
  assert.equal(response.status, 200, 'Electron did not expose its temporary CDP target list')
  const targets = await response.json()
  const origin = new URL(serverURL).origin
  const target = targets.find(target => {
    if (target.type !== 'page') return false
    try { return new URL(target.url).origin === origin } catch { return false }
  })
  if (!target) { diagnose(`Waiting for managed page ${origin}; targets: ${JSON.stringify(targets.map(({ type, url }) => ({ type, url })))}`); return false }
  const endpoint = new URL(target.webSocketDebuggerUrl)
  assert.equal(endpoint.protocol, 'ws:')
  assert.equal(endpoint.hostname, '127.0.0.1')
  assert.equal(Number(endpoint.port), port)
  assert.equal(endpoint.username, '')
  assert.equal(endpoint.password, '')
  return endpoint.href
}

export async function rendererAuthentication(profile, serverURL, { diagnose = () => {}, evaluate = evaluateRenderer } = {}) {
  const endpoint = await managedRendererEndpoint(profile, serverURL, { diagnose })
  if (!endpoint) return false
  const origin = new URL(serverURL).origin
  let result
  try {
    result = await evaluate(endpoint, `(async () => {
      const documentState = () => {
        const app = document.querySelector('#app');
        const onboarding = app?.querySelector('.onboarding-layout');
        const bounds = onboarding?.getBoundingClientRect();
        const style = onboarding && getComputedStyle(onboarding);
        const loginScreen = app?.querySelector('.login-screen');
        return {
          origin: location.origin, href: location.href, readyState: document.readyState,
          mounted: Boolean(app?.childElementCount),
          onboardingVisible: Boolean(bounds?.width > 0 && bounds?.height > 0 && style.display !== 'none' && style.visibility === 'visible' && style.opacity !== '0'),
          loginScreen: Boolean(loginScreen), loginText: loginScreen?.textContent?.trim().slice(0, 300),
        };
      };
      const state = documentState();
      // CDP's target URL can change before its new document/context is ready.
      // Do not fetch from about:blank or the previous document during navigation.
      if (state.origin !== ${JSON.stringify(origin)} || state.readyState !== 'complete' || !state.mounted) return { ready: false, ...state };
      const response = await fetch(${JSON.stringify(new URL('/auth/status', origin).href)}, { credentials: 'same-origin', cache: 'no-store' });
      const auth = await response.json();
      const updates = await window.astralDesktop?.updates?.state();
      return { ready: true, ...documentState(), status: response.status, auth, updates };
    })()`)
  } catch (error) {
    if (!(error instanceof RendererNavigationError)) throw error
    diagnose(error.message)
    return false
  }
  diagnose(JSON.stringify(result))
  if (!result.ready) return false
  assert.equal(result.origin, origin, 'Authentication must be checked in the managed renderer')
  assert.equal(result.status, 200, `The actual Desktop renderer could not reach its authentication endpoint: ${JSON.stringify(result)}`)
  assert.equal(result.auth.authenticated, true, `The actual Desktop renderer was not authenticated: ${JSON.stringify(result)}`)
  assert.equal(result.auth.desktop, true, `The renderer used a browser cookie instead of the process-local Desktop identity: ${JSON.stringify(result)}`)
  // This smoke starts a fresh, incomplete-onboarding profile. Comments, an empty
  // loading frame, skeletons or a login/error screen cannot prove App startup.
  // Recheck its actual visible application root after the awaited auth request.
  if (result.readyState !== 'complete' || !result.mounted || !result.onboardingVisible || result.loginScreen) return false
  assert.ok(result.updates && /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(result.updates.currentVersion), 'The packaged Desktop update bridge must report its installed version')
  assert.ok(['idle', 'checking', 'current', 'downloading', 'ready', 'installing', 'error', 'unavailable'].includes(result.updates.phase), 'The packaged Desktop update bridge returned an invalid lifecycle state')
  return true
}
