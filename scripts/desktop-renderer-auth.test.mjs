import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { evaluateRenderer, managedRendererEndpoint, rendererAuthentication } from './desktop-renderer-auth.mjs'

const origin = 'http://127.0.0.1:4545'

async function fixture(t, targets) {
  const profile = await mkdtemp(path.join(tmpdir(), 'astral-renderer-test-'))
  const server = createServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify(targets ? targets(server.address().port) : [{ type: 'page', url: `${origin}/`, webSocketDebuggerUrl: `ws://127.0.0.1:${server.address().port}/target` }]))
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  await writeFile(path.join(profile, 'DevToolsActivePort'), `${server.address().port}\n/browser`)
  t.after(async () => {
    await new Promise(resolve => server.close(resolve))
    await rm(profile, { recursive: true, force: true })
  })
  return profile
}

test('shared renderer discovery rejects foreign page origins and nonlocal CDP endpoints', async t => {
  const foreign = await fixture(t, port => [{ type: 'page', url: `${origin}@unrelated.example/`, webSocketDebuggerUrl: `ws://127.0.0.1:${port}/target` }])
  assert.equal(await managedRendererEndpoint(foreign, origin), false, 'An origin prefix must not select a foreign page')
  for (const endpoint of [
    port => `ws://unrelated.example:${port}/target`,
    port => `wss://127.0.0.1:${port}/target`,
    port => `ws://127.0.0.1:${port === 65535 ? 65534 : port + 1}/target`,
    port => `ws://unexpected:fixture@127.0.0.1:${port}/target`,
  ]) {
    const profile = await fixture(t, port => [{ type: 'page', url: `${origin}/`, webSocketDebuggerUrl: endpoint(port) }])
    await assert.rejects(managedRendererEndpoint(profile, origin))
  }
})

function cdpMessage(message) {
  return class extends globalThis.EventTarget {
    constructor() {
      super()
      globalThis.queueMicrotask(() => this.dispatchEvent(new globalThis.Event('open')))
    }
    send() {
      this.dispatchEvent(new globalThis.MessageEvent('message', { data: JSON.stringify({ id: 1, ...message }) }))
    }
    close() {}
  }
}

function managedPage() {
  const state = {
    location: { origin, href: `${origin}/` }, readyState: 'complete', elements: 1,
    onboarding: true, display: 'flex', visibility: 'visible', opacity: '1', width: 1240, height: 860,
  }
  const onboarding = { getBoundingClientRect: () => ({ width: state.width, height: state.height }) }
  const app = {
    get childElementCount() { return state.elements },
    hasChildNodes: () => true, // Vue comment placeholders are still child nodes.
    querySelector: selector => selector === '.onboarding-layout' && state.onboarding ? onboarding : null,
  }
  return {
    state,
    globals: {
      window: { astralDesktop: { updates: { state: async () => ({ currentVersion: '0.1.0', phase: 'idle' }) } } },
      location: state.location,
      document: { get readyState() { return state.readyState }, querySelector: selector => selector === '#app' ? app : null },
      getComputedStyle: () => ({ display: state.display, visibility: state.visibility, opacity: state.opacity }),
    },
  }
}

test('a matching CDP target does not fetch until its live managed document is loaded and mounted', async t => {
  const profile = await fixture(t)
  const { state, globals } = managedPage()
  state.location.origin = 'null'
  state.location.href = 'about:blank'
  state.readyState = 'loading'
  state.elements = 0
  let requests = 0
  let updateCalls = 0
  globals.window.astralDesktop = undefined
  const evaluate = (_endpoint, expression) => runInNewContext(expression, {
    ...globals,
    fetch: async url => {
      requests++
      assert.equal(url, `${origin}/auth/status`)
      return { status: 200, json: async () => ({ authenticated: true, desktop: true }) }
    },
  })
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false)
  state.location.origin = origin
  state.location.href = `${origin}/`
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false)
  state.readyState = 'complete'
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false)
  assert.equal(requests, 0)
  globals.window.astralDesktop = { updates: { state: async () => { updateCalls++; return { currentVersion: '0.0.0', phase: 'idle' } } } }
  state.elements = 1
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), true)
  assert.equal(requests, 1)
  assert.equal(updateCalls, 1, 'The native 0.0.0 fixture bridge is invoked only after its document is loaded')
})

test('a destroyed navigation context is retried, with its CDP diagnosis preserved', async t => {
  const profile = await fixture(t)
  let diagnostic
  const evaluate = (endpoint, expression) => evaluateRenderer(endpoint, expression, {
    WebSocket: cdpMessage({ error: { code: -32000, message: 'Execution context was destroyed.' } }),
  })
  assert.equal(await rendererAuthentication(profile, origin, { evaluate, diagnose: value => { diagnostic = value } }), false)
  assert.match(diagnostic, /-32000.*Execution context was destroyed/)
  assert.equal(await rendererAuthentication(profile, origin, { evaluate: async () => ({ ready: true, origin, readyState: 'complete', mounted: true, onboardingVisible: true, status: 200, auth: { authenticated: true, desktop: true }, updates: { currentVersion: '0.1.0', phase: 'idle' } }) }), true)
})

test('authentication rejection and a cookie-only identity fail immediately instead of retrying', async t => {
  const profile = await fixture(t)
  for (const result of [
    { status: 401, auth: { authenticated: false, desktop: false } },
    { status: 200, auth: { authenticated: false, desktop: false } },
    { status: 200, auth: { authenticated: true, desktop: false } },
  ]) {
    await assert.rejects(rendererAuthentication(profile, origin, { evaluate: async () => ({ ready: true, origin, ...result }) }), /authentication endpoint|not authenticated|browser cookie/)
  }
})

test('a packaged renderer without a working typed update bridge cannot pass acceptance', async t => {
  const profile = await fixture(t)
  for (const updates of [undefined, { currentVersion: 'not-a-version', phase: 'idle' }, { currentVersion: '0.1.0', phase: 'invented' }]) {
    await assert.rejects(rendererAuthentication(profile, origin, { evaluate: async () => ({ ready: true, origin, readyState: 'complete', mounted: true, onboardingVisible: true, status: 200, auth: { authenticated: true, desktop: true }, updates }) }), /update bridge/)
  }
})

test('a successful probe cannot hide an App still showing its own login or initialization error', async t => {
  const profile = await fixture(t)
  let diagnostic
  const evaluate = async () => ({ ready: true, origin, readyState: 'complete', mounted: true, onboardingVisible: true, status: 200, auth: { authenticated: true, desktop: true }, loginScreen: true, loginText: 'Sign in' })
  assert.equal(await rendererAuthentication(profile, origin, { evaluate, diagnose: value => { diagnostic = value } }), false)
  assert.match(diagnostic, /Sign in/)
})

test('Vue comments and the pre-bootstrap blank frame or skeleton cannot prove actual App initialization', async t => {
  const profile = await fixture(t)
  const { state, globals } = managedPage()
  state.elements = 0
  state.onboarding = false
  let requests = 0
  const evaluate = (_endpoint, expression) => runInNewContext(expression, {
    ...globals,
    fetch: async () => {
      requests++
      return { status: 200, json: async () => ({ authenticated: true, desktop: true }) }
    },
  })
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false)
  assert.equal(requests, 0, 'A root containing only Vue comments is not mounted')
  state.elements = 1
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false, 'A skeleton without onboarding is not initialized')
  assert.equal(requests, 1, 'Identity must still be checked rather than hiding an authentication error behind UI readiness')
  state.onboarding = true
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), true)
})

test('onboarding must be visible and must still exist after the authentication request completes', async t => {
  const profile = await fixture(t)
  const { state, globals } = managedPage()
  let duringFetch = () => {}
  const evaluate = (_endpoint, expression) => runInNewContext(expression, {
    ...globals,
    fetch: async () => {
      duringFetch()
      return { status: 200, json: async () => ({ authenticated: true, desktop: true }) }
    },
  })
  for (const patch of [{ display: 'none' }, { visibility: 'hidden' }, { opacity: '0' }, { width: 0 }, { height: 0 }]) {
    Object.assign(state, { display: 'flex', visibility: 'visible', opacity: '1', width: 1240, height: 860 }, patch)
    assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false)
  }
  Object.assign(state, { display: 'flex', visibility: 'visible', opacity: '1', width: 1240, height: 860 })
  duringFetch = () => { state.onboarding = false }
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false, 'The UI root must be read again after fetch')
  state.onboarding = true
  duringFetch = () => { state.elements = 0 }
  assert.equal(await rendererAuthentication(profile, origin, { evaluate }), false, 'Comments replacing the App during fetch cannot pass')
})

test('script, fetch and unexpected protocol errors retain their cause and are fatal', async t => {
  const profile = await fixture(t)
  for (const message of [
    { result: { exceptionDetails: { text: 'Uncaught (in promise)', exception: { description: 'TypeError: Failed to fetch\n    at smoke-check' } } } },
    { error: { code: -32601, message: 'Runtime.evaluate was denied' } },
    { error: { code: -32601, message: 'Execution context was destroyed.' } },
  ]) {
    const evaluate = (endpoint, expression) => evaluateRenderer(endpoint, expression, { WebSocket: cdpMessage(message) })
    await assert.rejects(rendererAuthentication(profile, origin, { evaluate }), /Failed to fetch|denied|Execution context was destroyed/)
  }
})

test('an origin change during the authentication request cannot pass the managed renderer assertion', async t => {
  const profile = await fixture(t)
  const { state, globals } = managedPage()
  const evaluate = (_endpoint, expression) => runInNewContext(expression, {
    ...globals,
    fetch: async () => {
      state.location.origin = 'https://unrelated.example'
      return { status: 200, json: async () => ({ authenticated: true, desktop: true }) }
    },
  })
  await assert.rejects(rendererAuthentication(profile, origin, { evaluate }), /managed renderer/)
})
