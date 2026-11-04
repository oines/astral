import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { access, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { notarizationGate, runMacReleaseAcceptance } from './mac-release-acceptance.mjs'

const helper = new URL('./mac-release-acceptance.mjs', import.meta.url).href
const checker = fileURLToPath(new URL('./check-desktop-update.mjs', import.meta.url))
const cli = fileURLToPath(new URL('./check-mac-release.mjs', import.meta.url))

async function exists(file) {
  try { await access(file); return true } catch (error) { if (error.code !== 'ENOENT') throw error; return false }
}

async function waitFor(read) {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (await read()) return
    await delay(10)
  }
  assert.fail('A synthetic child did not reach the expected state.')
}

async function fixture(t, mode = 'success') {
  const root = await mkdtemp(path.join(tmpdir(), 'astral-mac-release-test-'))
  const controller = new globalThis.AbortController()
  const active = new Set()
  t.after(async () => {
    controller.abort(new Error('The synthetic test has finished.'))
    await Promise.allSettled(active)
    await rm(root, { recursive: true, force: true })
  })
  const script = path.join(root, 'child.mjs')
  await writeFile(script, `
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { access, appendFile, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { notarizationGate } from ${JSON.stringify(helper)}
const root = process.env.FIXTURE_ROOT
const mode = process.env.FIXTURE_MODE
const role = process.argv[2]
const file = name => path.join(root, name)
const event = async name => { await appendFile(file('events'), name + '\\n'); await writeFile(file(name), 'reached') }
const exists = name => access(file(name)).then(() => true, () => false)
await writeFile(file(role + '.pid'), String(process.pid))
if (role === 'notary') {
  let stopped = false
  let grandchild
  let reaped
  process.on('SIGTERM', () => { stopped = true })
  try {
    if (mode === 'cancel') {
      grandchild = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
      reaped = new Promise(resolve => grandchild.once('close', resolve))
      await writeFile(file('grandchild.pid'), String(grandchild.pid))
    }
    // A ZIP exists before acceptance and has deliberately wrong contents.
    await writeFile(file('target.zip'), 'unaccepted partial package')
    await event('notary-started')
    if (mode !== 'early-notary') while (!await exists('fixture-prepared') && !stopped) await delay(10)
    if (mode === 'fail-notary') process.exitCode = 7
    else if (['cancel', 'fail-checker', 'early-checker', 'disconnect'].includes(mode)) {
      while (!stopped) await delay(10)
    } else if (!stopped) {
      await writeFile(file('target.zip'), 'final notarized package')
      await event('notary-accepted')
      if (mode === 'fail-after-accepted') process.exitCode = 9
    }
  } finally {
    if (grandchild) {
      grandchild.kill('SIGTERM')
      await reaped
    }
    await event('notary-cleaned')
  }
} else {
  if (mode === 'early-checker') {
    while (!await exists('notary-started')) await delay(10)
    process.exit(0)
  }
  if (mode === 'early-notary') await delay(100)
  const gate = notarizationGate()
  try {
    while (!await exists('notary-started') && !gate.signal.aborted) await delay(10)
    gate.signal.throwIfAborted()
    await writeFile(file('fixture-prepared'), 'signed old fixture')
    await event('fixture-prepared')
    if (mode === 'fail-checker') throw new Error('synthetic fixture signing failure')
    if (mode === 'disconnect') process.disconnect()
    await gate.wait()
    assert.equal(await readFile(file('target.zip'), 'utf8'), 'final notarized package')
    await event('app-started')
  } catch { process.exitCode = 3 }
  finally { await event('checker-cleaned'); gate.dispose() }
}
`)
  const command = role => ({ program: process.execPath, args: [script, role] })
  const options = {
    notarize: command('notary'), update: command('checker'), cwd: root,
    environment: { ...process.env, FIXTURE_ROOT: root, FIXTURE_MODE: mode }, cleanupTimeout: 2000, killTimeout: 2000,
  }
  const run = (overrides = {}) => {
    const promise = runMacReleaseAcceptance({ ...options, ...overrides, signal: overrides.signal ? AbortSignal.any([controller.signal, overrides.signal]) : controller.signal })
    active.add(promise)
    promise.then(() => active.delete(promise), () => active.delete(promise))
    return promise
  }
  return { root, run, events: async () => (await readFile(path.join(root, 'events'), 'utf8')).trim().split('\n') }
}

test('signed fixture preparation overlaps notarization, while final ZIP use and App launch wait for successful notary exit', { timeout: 10_000 }, async t => {
  const f = await fixture(t)
  await f.run()
  const events = await f.events()
  assert.ok(events.indexOf('fixture-prepared') < events.indexOf('notary-accepted'), 'Notary cannot complete until independent fixture preparation has started.')
  assert.ok(events.indexOf('notary-cleaned') < events.indexOf('app-started'), 'Accepted output alone is insufficient; the complete notary process must exit successfully.')
  assert.ok(events.includes('checker-cleaned'))
})

test('a preexisting ZIP and failed notarization never start update acceptance; the checker is canceled and joined', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'fail-notary')
  await assert.rejects(f.run(), /Mac notarization failed \(7\)/)
  assert.equal(await readFile(path.join(f.root, 'target.zip'), 'utf8'), 'unaccepted partial package')
  const events = await f.events()
  assert.ok(events.includes('checker-cleaned'))
  assert.equal(events.includes('app-started'), false)
})

test('Apple acceptance followed by a failing package verification still cannot release the updater gate', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'fail-after-accepted')
  await assert.rejects(f.run(), /Mac notarization failed \(9\)/)
  const events = await f.events()
  assert.ok(events.includes('notary-accepted') && events.includes('checker-cleaned'))
  assert.equal(events.includes('app-started'), false)
})

test('notarization that finishes before the checker handshake is delivered when the checker becomes ready', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'early-notary')
  await f.run()
  const events = await f.events()
  assert.ok(events.indexOf('notary-cleaned') < events.indexOf('fixture-prepared'))
  assert.ok(events.includes('app-started') && events.includes('checker-cleaned'))
})

test('fixture failure cancels and joins the still-running notary without granting access to the ZIP', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'fail-checker')
  await assert.rejects(f.run(), /Mac update acceptance failed \(3\)/)
  const events = await f.events()
  if (process.platform !== 'win32') assert.ok(events.includes('notary-cleaned'))
  assert.ok(events.includes('checker-cleaned'))
  assert.equal(events.includes('app-started'), false)
})

test('a checker cannot report success before successful notarization', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'early-checker')
  await assert.rejects(f.run(), /exited before receiving successful notarization/)
  if (process.platform !== 'win32') assert.ok((await f.events()).includes('notary-cleaned'))
})

test('owning IPC disconnect aborts the blocked checker instead of hanging or starting the App', { timeout: 10_000 }, async t => {
  const f = await fixture(t, 'disconnect')
  await assert.rejects(f.run(), /Mac update acceptance failed \(3\)/)
  const events = await f.events()
  assert.ok(events.includes('checker-cleaned'))
  if (process.platform !== 'win32') assert.ok(events.includes('notary-cleaned'))
  assert.equal(events.includes('app-started'), false)
})

test('external cancellation joins both owned children and their signing descendant', { timeout: 10_000, skip: process.platform === 'win32' }, async t => {
  const f = await fixture(t, 'cancel')
  const controller = new globalThis.AbortController()
  const run = f.run({ signal: controller.signal })
  const rejected = assert.rejects(run, /synthetic cancellation/)
  await waitFor(() => exists(path.join(f.root, 'grandchild.pid')))
  await waitFor(() => exists(path.join(f.root, 'fixture-prepared')))
  controller.abort(new Error('synthetic cancellation'))
  await rejected
  for (const name of ['notary', 'checker', 'grandchild']) {
    const pid = Number(await readFile(path.join(f.root, `${name}.pid`), 'utf8'))
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' }, `${name} must have exited and been reaped.`)
  }
  const events = await f.events()
  assert.ok(events.includes('notary-cleaned') && events.includes('checker-cleaned'))
  assert.equal(events.includes('app-started'), false)
})

test('a notary executable that fails to spawn cancels a checker even before its IPC handshake', { timeout: 10_000 }, async t => {
  const f = await fixture(t)
  await assert.rejects(f.run({ notarize: { program: path.join(f.root, 'does-not-exist'), args: [] } }), /Mac notarization could not start/)
  assert.ok((await f.events()).includes('checker-cleaned'))
  assert.equal((await f.events()).includes('app-started'), false)
})

test('the gate remembers an early success grant and still cancels later work on disconnect', async () => {
  const channel = new EventEmitter()
  channel.connected = true
  channel.send = (value, callback) => { assert.equal(value.event, 'checker-ready'); callback() }
  channel.disconnect = () => { channel.connected = false }
  const gate = notarizationGate(channel)
  channel.emit('message', { type: 'astral-mac-release', event: 'notarized' })
  await gate.wait()
  channel.emit('disconnect')
  await assert.rejects(gate.wait(), /lost its owning IPC channel/)
  gate.dispose()
  for (const name of ['message', 'disconnect', 'SIGINT', 'SIGTERM']) assert.equal(channel.listenerCount(name), 0)
})

test('the new parallel acceptance CLI and checker refuse developer accounts before reading a package', () => {
  const environment = { ...process.env }
  for (const name of ['CI', 'GITHUB_ACTIONS', 'RUNNER_TEMP']) delete environment[name]
  for (const [script, args] of [[cli, ['/nonexistent/Astral.app', 'arm64']], [checker, ['--await-notarization']]]) {
    const result = spawnSync(process.execPath, [script, ...args], { env: environment, encoding: 'utf8', timeout: 10_000 })
    assert.equal(result.status, 1)
    assert.match(result.stderr, /requires a disposable GitHub Actions user; never run on a developer account/)
  }
})
