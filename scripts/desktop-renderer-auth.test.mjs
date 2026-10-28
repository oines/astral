import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { evaluateRenderer, managedRendererEndpoint, RendererConnectionError, RendererNavigationError, RendererTimeoutError, rendererAuthentication, rendererUpdateMethod, rendererUpdateProgress, startRendererUpdate } from './desktop-renderer-auth.mjs'

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

test('current update lifecycle accepts available and typed retries, while ready is limited to an explicit old-package probe', async t => {
  const profile = await fixture(t)
  const probe = updates => async () => ({ ready: true, origin, readyState: 'complete', mounted: true, onboardingVisible: true, status: 200, auth: { authenticated: true, desktop: true }, updates })
  for (const updates of [
    { currentVersion: '0.1.2', phase: 'available', version: '0.1.3' },
    { currentVersion: '0.1.2', phase: 'error', retry: 'check' },
    { currentVersion: '0.1.2', phase: 'error', retry: 'update' },
  ]) assert.equal(await rendererAuthentication(profile, origin, { evaluate: probe(updates) }), true)
  const old = { currentVersion: '0.1.0', phase: 'ready', version: '0.1.1' }
  await assert.rejects(rendererAuthentication(profile, origin, { evaluate: probe(old) }), /invalid lifecycle state/)
  assert.equal(await rendererAuthentication(profile, origin, { evaluate: probe(old), allowLegacyUpdates: true }), true)
  await assert.rejects(rendererAuthentication(profile, origin, { evaluate: probe({ ...old, phase: 'error', retry: 'install' }) }), /invalid retry action/)
})

test('the update driver prefers on-demand capability and requires explicit permission for an untouched install-only package', async () => {
  for (const [updates, allowLegacy, expected] of [
    [{ check() {}, update() {} }, false, 'update'],
    [{ check() {}, update() {}, install() {} }, true, 'update'],
    [{ check() {}, install() {} }, true, 'install'],
  ]) {
    const evaluate = (_endpoint, expression) => runInNewContext(expression, { window: { astralDesktop: { updates } } })
    assert.equal(await rendererUpdateMethod('unused', { evaluate, allowLegacy }), expected)
  }
  for (const updates of [{ check() {}, install() {} }, { update() {} }, { check() {} }]) {
    const evaluate = (_endpoint, expression) => runInNewContext(expression, { window: { astralDesktop: { updates } } })
    await assert.rejects(rendererUpdateMethod('unused', { evaluate }), /on-demand update|support checking/)
  }
})

test('an explicit update returns before its download and preserves asynchronous rejection for the replacement wait', async () => {
  let rejectDownload
  let calls = 0
  const download = new Promise((_resolve, reject) => { rejectDownload = reject })
  const window = { astralDesktop: { updates: { update: () => { calls++; return download }, state: async () => ({ phase: 'downloading', version: '0.1.3' }) } } }
  const evaluate = (_endpoint, expression) => runInNewContext(expression, { window })
  await startRendererUpdate('unused', 'update', { evaluate })
  assert.equal(calls, 1)
  assert.equal(window.__astralUpdateAcceptance.complete, false)
  assert.equal((await rendererUpdateProgress('unused', { evaluate })).phase, 'downloading')
  await assert.rejects(startRendererUpdate('unused', 'update', { evaluate }), /already started/)
  rejectDownload(new Error('Installer checksum failed'))
  await new Promise(resolve => setTimeout(resolve, 0))
  await assert.rejects(rendererUpdateProgress('unused', { evaluate }), /Installer checksum failed/)
})

test('synchronous IPC failures and engine errors stay visible instead of being treated as an expected exit', async () => {
  for (const engineError of [false, true]) {
    const window = { astralDesktop: { updates: {
      update: () => { if (!engineError) throw new Error('Native update denied') },
      state: async () => ({ phase: engineError ? 'error' : 'available', error: 'Native installer failed' }),
    } } }
    const evaluate = (_endpoint, expression) => runInNewContext(expression, { window })
    await startRendererUpdate('unused', 'update', { evaluate })
    await new Promise(resolve => setTimeout(resolve, 0))
    await assert.rejects(rendererUpdateProgress('unused', { evaluate }), /Native update denied|Native installer failed/)
  }
})

test('the frozen official install action is invoked without changing its bridge or swallowing its result', async () => {
  let calls = 0
  const updates = { check() {}, install: async () => { calls++ }, state: async () => ({ phase: 'installing' }) }
  const window = { astralDesktop: { updates } }
  const evaluate = (_endpoint, expression) => runInNewContext(expression, { window })
  const method = await rendererUpdateMethod('unused', { evaluate, allowLegacy: true })
  await startRendererUpdate('unused', method, { evaluate })
  await new Promise(resolve => setTimeout(resolve, 0))
  assert.equal(calls, 1)
  assert.equal(updates.update, undefined, 'The driver must not polyfill or patch the old official bridge')
  assert.equal((await rendererUpdateProgress('unused', { evaluate })).phase, 'installing')
})

test('an update that closes CDP before its acknowledgement is dispatched once and still requires replacement evidence', async () => {
  for (const closure of ['transport', 'context']) {
    let calls = 0
    let diagnostic
    const WebSocket = class extends globalThis.EventTarget {
      constructor() {
        super()
        globalThis.queueMicrotask(() => this.dispatchEvent(new globalThis.Event('open')))
      }
      send(raw) {
        const command = JSON.parse(raw)
        const window = { astralDesktop: { updates: { install: () => {
          calls++
          if (closure === 'transport') this.dispatchEvent(new globalThis.Event('close'))
          else this.dispatchEvent(new globalThis.MessageEvent('message', { data: JSON.stringify({ id: 1, error: { code: -32000, message: 'Execution context was destroyed.' } }) }))
          return Promise.resolve()
        } } } }
        const value = runInNewContext(command.params.expression, { window })
        // The update's microtask can run before CDP sends the evaluation reply.
        globalThis.queueMicrotask(() => this.dispatchEvent(new globalThis.MessageEvent('message', { data: JSON.stringify({ id: 1, result: { result: { value } } }) })))
      }
      close() {}
    }
    const evaluate = (endpoint, expression) => evaluateRenderer(endpoint, expression, { WebSocket })
    assert.equal(await startRendererUpdate('unused', 'install', { evaluate, diagnose: value => { diagnostic = value } }), false)
    assert.equal(calls, 1, 'A missing acknowledgement must never retry the native update')
    assert.match(diagnostic, /not acknowledged.*closed/)
  }
  let diagnostics = 0
  for (const message of [
    { result: { exceptionDetails: { text: 'Native update method is unavailable' } } },
    { error: { code: -32601, message: 'Runtime.evaluate denied' } },
  ]) {
    const evaluate = (endpoint, expression) => evaluateRenderer(endpoint, expression, { WebSocket: cdpMessage(message) })
    await assert.rejects(startRendererUpdate('unused', 'update', { evaluate, diagnose: () => { diagnostics++ } }), /unavailable|denied/)
  }
  assert.equal(diagnostics, 0, 'Script and protocol failures must stay fatal')
})

test('CDP transport closure is distinguishable from script and protocol failures after update dispatch', async () => {
  const WebSocket = class extends globalThis.EventTarget {
    constructor() {
      super()
      globalThis.queueMicrotask(() => this.dispatchEvent(new globalThis.Event('open')))
    }
    send() { this.dispatchEvent(new globalThis.Event('close')) }
    close() {}
  }
  await assert.rejects(evaluateRenderer('unused', 'true', { WebSocket }), error => error instanceof RendererConnectionError)
  await assert.rejects(evaluateRenderer('unused', 'true', { WebSocket: cdpMessage({ result: { exceptionDetails: { text: 'Update rejected' } } }) }), error => !(error instanceof RendererConnectionError) && /Update rejected/.test(error.message))
})

test('only progress after explicit update dispatch can wait through CDP loss or timeout, without reporting success or redispatching', async () => {
  let updates = 0
  const window = { astralDesktop: { updates: { update: async () => { updates++ } } } }
  await startRendererUpdate('unused', 'update', { evaluate: (_endpoint, expression) => runInNewContext(expression, { window }) })
  for (const ErrorType of [RendererConnectionError, RendererNavigationError, RendererTimeoutError]) {
    const error = new ErrorType('The old renderer no longer answers')
    const evaluate = async () => { throw error }
    await assert.rejects(rendererUpdateProgress('unused', { evaluate }), value => value === error)
    let diagnostic
    assert.equal(await rendererUpdateProgress('unused', { evaluate, afterUpdateDispatch: true, diagnose: value => { diagnostic = value } }), undefined)
    assert.match(diagnostic, /Waiting for actual Desktop replacement/)
    assert.equal(updates, 1, 'Missing progress must never dispatch another update')
  }
  for (const error of [new Error('Native installer failed'), new Error('Runtime.evaluate was denied'), new Error('A script threw: timed out')]) {
    await assert.rejects(rendererUpdateProgress('unused', { evaluate: async () => { throw error }, afterUpdateDispatch: true }), value => value === error)
  }
})

test('a successful progress query must return a valid lifecycle state, even after update dispatch', async () => {
  const progress = state => ({ evaluate: async () => ({ request: { started: true }, state }), afterUpdateDispatch: true })
  for (const state of [undefined, null, false, 'downloading', [], {}, { phase: 'invented' }, { phase: 'ready' }]) {
    await assert.rejects(rendererUpdateProgress('unused', progress(state)), /no valid native update state|invalid native update phase/)
  }
  for (const phase of ['idle', 'checking', 'current', 'available', 'downloading', 'installing', 'unavailable']) {
    assert.equal((await rendererUpdateProgress('unused', progress({ phase }))).phase, phase)
  }
  await assert.rejects(rendererUpdateProgress('unused', progress({ phase: 'error', error: 'Native installer rejected the update' })), /Real native update failed: Native installer rejected/)
  assert.equal((await rendererUpdateProgress('unused', { ...progress({ phase: 'ready' }), allowLegacyUpdates: true })).phase, 'ready', 'Only the frozen official install contract may report ready')
})

test('a nonresponsive CDP command has a typed timeout that stays fatal before update dispatch', async t => {
  const WebSocket = class extends globalThis.EventTarget {
    constructor() {
      super()
      globalThis.queueMicrotask(() => this.dispatchEvent(new globalThis.Event('open')))
    }
    send() {}
    close() {}
  }
  const profile = await fixture(t)
  const evaluate = (endpoint, expression) => evaluateRenderer(endpoint, expression, { WebSocket })
  await assert.rejects(rendererAuthentication(profile, origin, { evaluate }), error => error instanceof RendererTimeoutError)
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
