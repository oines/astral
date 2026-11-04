import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'

const protocol = 'astral-mac-release'

// The update fixture can be built early, but neither final ZIP bytes nor the
// real updater may be used before the owning notary process succeeds.
export function notarizationGate(channel = process) {
  assert.ok(channel.connected && typeof channel.send === 'function', 'Parallel Mac update acceptance requires its owning IPC channel.')
  const controller = new globalThis.AbortController()
  const pending = new Set()
  let ready = false
  const changed = () => { for (const notify of pending) notify() }
  const abort = reason => { controller.abort(new Error(reason)); changed() }
  const message = value => {
    if (value?.type !== protocol) return
    if (value.event === 'notarized') { ready = true; changed() }
    if (value.event === 'abort') abort('Mac release acceptance was canceled by its owner.')
  }
  const disconnect = () => abort('Mac release acceptance lost its owning IPC channel.')
  const terminate = () => abort('Mac release acceptance received a termination signal.')
  channel.on('message', message)
  channel.on('disconnect', disconnect)
  channel.on('SIGTERM', terminate)
  channel.on('SIGINT', terminate)
  channel.send({ type: protocol, event: 'checker-ready' }, error => { if (error) abort('Mac release acceptance could not reach its owner.') })
  return {
    signal: controller.signal,
    wait() {
      return new Promise((resolve, reject) => {
        const notify = () => {
          if (!controller.signal.aborted && !ready) return
          pending.delete(notify)
          if (controller.signal.aborted) reject(controller.signal.reason)
          else resolve()
        }
        pending.add(notify)
        notify()
      })
    },
    dispose() {
      channel.off('message', message)
      channel.off('disconnect', disconnect)
      channel.off('SIGTERM', terminate)
      channel.off('SIGINT', terminate)
      if (channel.connected) channel.disconnect()
    },
  }
}

function start(command, ipc, options) {
  const child = spawn(command.program, command.args, {
    cwd: options.cwd, env: options.environment,
    stdio: ipc ? ['ignore', 'inherit', 'inherit', 'ipc'] : ['ignore', 'inherit', 'inherit'],
    detached: process.platform !== 'win32',
  })
  const owned = { child, closed: false }
  owned.done = new Promise(resolve => {
    child.once('error', error => { owned.closed = true; resolve({ error }) })
    child.once('close', (code, signal) => { owned.closed = true; resolve({ code, signal }) })
  })
  return owned
}

function stop(owned, signal) {
  if (!owned?.child.pid) return
  try {
    // Only children created in new process groups by this invocation are used.
    // The group also contains electron-builder, hdiutil and signing children.
    if (process.platform === 'win32') { if (!owned.closed) owned.child.kill(signal) }
    else process.kill(-owned.child.pid, signal)
  } catch (error) { if (error.code !== 'ESRCH') throw error }
}

function requireSuccess(label, result) {
  if (result.error) throw new Error(`${label} could not start.`, { cause: result.error })
  assert.equal(result.code, 0, `${label} failed (${result.signal || result.code}); no Mac release acceptance can be granted.`)
}

async function completesWithin(promise, milliseconds) {
  let timer
  try {
    return await Promise.race([promise.then(() => true), new Promise(resolve => { timer = setTimeout(() => resolve(false), milliseconds) })])
  } finally { clearTimeout(timer) }
}

export async function runMacReleaseAcceptance({ notarize, update, cwd, environment = process.env, signal, cleanupTimeout = 45_000, killTimeout = 5000 }) {
  signal?.throwIfAborted()
  const options = { cwd, environment }
  let signing
  let checking
  let abortListener
  let checkerReady = false
  let notarized = false
  let granted = false
  let canceled = false
  let ipcError
  const cancelChecker = () => {
    if (checking?.child.connected) checking.child.send({ type: protocol, event: 'abort' }, () => {})
  }
  const sendGrant = () => {
    if (canceled || !checkerReady || !notarized || granted || !checking.child.connected) return
    granted = true
    checking.child.send({ type: protocol, event: 'notarized' }, error => { if (error) ipcError?.(error) })
  }
  try {
    signing = start(notarize, false, options)
    checking = start(update, true, options)
    checking.child.on('message', value => {
      if (value?.type !== protocol || value.event !== 'checker-ready') return
      checkerReady = true
      if (canceled) cancelChecker()
      else sendGrant()
    })
    const failed = new Promise((_, reject) => {
      ipcError = error => reject(new Error('The Mac update acceptance IPC channel failed.', { cause: error }))
      if (signal) {
        abortListener = () => reject(signal.reason)
        signal.addEventListener('abort', abortListener, { once: true })
        if (signal.aborted) abortListener()
      }
    })
    const notaryResult = signing.done.then(result => {
      requireSuccess('Mac notarization', result)
      notarized = true
      sendGrant()
    })
    const updateResult = checking.done.then(result => {
      requireSuccess('Mac update acceptance', result)
      assert.ok(granted, 'The Mac update checker exited before receiving successful notarization.')
    })
    await Promise.race([Promise.all([notaryResult, updateResult]), failed])
  } catch (error) {
    canceled = true
    cancelChecker()
    stop(signing, 'SIGTERM')
    const finished = Promise.all([signing?.done, checking?.done])
    // The checker handles IPC cancellation inside its existing finally block,
    // quitting its real disposable App/Server before the process-group fallback.
    const settled = await completesWithin(finished, cleanupTimeout)
    stop(signing, 'SIGKILL')
    stop(checking, 'SIGKILL')
    if (!settled && !await completesWithin(finished, killTimeout)) {
      throw new AggregateError([error], 'Mac release children did not stop within the cleanup budget; retain runner diagnostics.', { cause: error })
    }
    throw error
  } finally {
    if (abortListener) signal.removeEventListener('abort', abortListener)
  }
}
