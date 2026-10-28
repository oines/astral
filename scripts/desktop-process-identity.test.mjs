import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { EventEmitter, once } from 'node:events'
import test from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { runInNewContext } from 'node:vm'
import { assertWindowsProcessOwner, sameWindowsProcess, stopWindowsProcessTree, windowsManagedProcessIdentity, windowsProcessIdentity } from './desktop-process-identity.mjs'

const original = { pid: 1234, birth: '639287514716123456', exe: 'C:\\Astral fixture\\resources\\bin\\astral-windows-amd64.exe', parent: 100 }
const responds = value => async () => ({ stdout: JSON.stringify(value) })

// This parent owns the original leaf ChildProcess handle even if the first CIM
// query fails. EOF cleanup never needs a numeric PID or another identity query.
const fixtureParent = `const {spawn}=require('node:child_process');
const leaf=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore',windowsHide:true});
const closed=new Promise(resolve=>leaf.once('close',resolve));
let stopping=false;
process.stdin.resume();
process.stdin.once('end',()=>{if(stopping)return;stopping=true;leaf.kill();void closed.then(()=>process.exit(0));});
console.log(leaf.pid);`

async function closeFixture(child, closed) {
  child.stdin.end()
  if (!await Promise.race([closed.then(() => true), delay(5000, false, { ref: false })])) {
    child.kill() // Only the held direct ChildProcess handle, never a saved PID.
    assert.ok(await Promise.race([closed.then(() => true), delay(5000, false, { ref: false })]), 'Owned fixture parent did not exit during cleanup')
  }
}

test('Windows Server identity distinguishes a recycled PID from a surviving original process', () => {
  assert.equal(sameWindowsProcess(original, { ...original }), true)
  assert.equal(sameWindowsProcess(original, { ...original, birth: '639287514716123457' }), false)
  assert.equal(sameWindowsProcess(original, { ...original, pid: 4321 }), false)
  assert.equal(sameWindowsProcess(original, null), false)
  assert.equal(assertWindowsProcessOwner(original, original.exe.toUpperCase(), original.parent), original)
  assert.throws(() => assertWindowsProcessOwner(original, 'C:\\another-installation\\astral.exe'), /outside this Desktop installation/)
  assert.throws(() => assertWindowsProcessOwner(original, original.exe, 200), /not a child/)
})

test('Windows process queries do not interpret unknown identity fields or CIM errors as exit', async () => {
  assert.equal(await windowsProcessIdentity(original.pid, { command: responds(null) }), null)
  assert.deepEqual(await windowsProcessIdentity(original.pid, { command: responds(original) }), original)
  for (const value of [{}, { ...original, birth: undefined }, { ...original, birth: Number(original.birth) }, { ...original, exe: '' }, { ...original, exe: 'relative.exe' }, { ...original, parent: undefined }, { ...original, pid: 4321 }]) {
    await assert.rejects(windowsProcessIdentity(original.pid, { command: responds(value) }), /identity|process ID|another PID/)
  }
  await assert.rejects(windowsProcessIdentity(original.pid, { command: async () => { throw new Error('CIM is unavailable') } }), /identity query.*failed/)
  await assert.rejects(windowsProcessIdentity(original.pid, { command: async () => ({ stdout: '' }) }), /invalid JSON/)
  for (const pid of [0, -1, '1234', NaN, 1.5]) await assert.rejects(windowsProcessIdentity(pid), /Invalid Windows process ID/)
})

test('Windows identity reads and handle cleanup each have one bounded 30-second command without retrying timeouts', async () => {
  for (const invoke of [command => windowsProcessIdentity(original.pid, { command }), command => stopWindowsProcessTree(original, { command })]) {
    let calls = 0
    let timeout
    await assert.rejects(invoke(async (_program, _args, options) => {
      calls++
      timeout = options.timeout
      throw Object.assign(new Error('PowerShell exceeded its bounded budget'), { killed: true, signal: 'SIGTERM' })
    }), /identity query or handle-based cleanup failed/)
    assert.equal(timeout, 30_000)
    assert.equal(calls, 1, 'A timed-out identity query must not be retried or treated as process exit')
  }
})

test('cleanup skips absent and recycled roots and rejects a claim to have killed a different process identity', async () => {
  for (const current of [null, { ...original, birth: '639287514716123457' }]) {
    const result = await stopWindowsProcessTree(original, { command: responds({ stopped: false, current }) })
    assert.equal(result.stopped, false)
    assert.equal(sameWindowsProcess(original, result.current), false)
  }
  assert.equal((await stopWindowsProcessTree(original, { command: responds({ stopped: true, current: original }) })).stopped, true)
  for (const value of [{ stopped: true, current: { ...original, birth: '639287514716123457' } }, { stopped: false, current: original }, { stopped: false }, { stopped: true, current: { ...original, birth: undefined } }]) {
    await assert.rejects(stopWindowsProcessTree(original, { command: responds(value) }), /identity/)
  }
  await assert.rejects(stopWindowsProcessTree(original, { command: async () => { throw new Error('Access denied') } }), /cleanup failed/)
})

test('PowerShell failures preserve bounded action/PID/exit/output diagnostics without serializing private error fields', async () => {
  const failure = Object.assign(new Error('private-message-never-print'), {
    code: 1, killed: true, signal: 'SIGTERM', stdout: `discarded-prefix-${'x'.repeat(3000)}safe-stdout-tail`, stderr: 'CreationDate and StartTime do not match',
    stack: 'private-stack-never-print', env: { TOKEN: 'private-env-never-print' }, args: ['private-arguments-never-print'], private: 'private-field-never-print',
  })
  for (const [action, invoke] of [
    ['read', command => windowsProcessIdentity(original.pid, { command })],
    ['stop', command => stopWindowsProcessTree(original, { command })],
  ]) await assert.rejects(invoke(async () => { throw failure }), error => {
    const diagnostic = JSON.parse(error.message.slice(error.message.indexOf('{')))
    assert.equal(diagnostic.action, action)
    assert.equal(diagnostic.pid, original.pid)
    assert.equal(diagnostic.timeout_ms, 30_000)
    assert.equal(diagnostic.exit_code, 1)
    assert.equal(diagnostic.killed, true)
    assert.equal(diagnostic.signal, 'SIGTERM')
    assert.equal(diagnostic.stdout.length, 2000)
    assert.ok(diagnostic.stdout.endsWith('safe-stdout-tail'))
    assert.equal(diagnostic.stderr, failure.stderr)
    assert.ok(!/private-|discarded-prefix/.test(error.message))
    return true
  })
  await assert.rejects(windowsProcessIdentity(original.pid, { command: async () => { throw { code: 'private-exit-code-never-print', killed: 'private-killed-never-print', signal: 'private-signal-never-print', stdout: {}, stderr: null } } }), error => /"exit_code":"unknown"/.test(error.message) && !error.message.includes('private-'))
})

test('replacement polling alone can wait for a candidate Server or parent that positively disappeared', async () => {
  const app = { pid: original.parent, birth: (BigInt(original.birth) - 1n).toString(), exe: 'C:\\Astral fixture\\Astral.exe', parent: 50 }
  const read = (query, allowMissing) => windowsManagedProcessIdentity(original.pid, original.exe, app.exe, { query, allowMissing })
  for (const disappearing of ['server', 'app']) {
    const query = async pid => pid === original.pid ? (disappearing === 'server' ? null : original) : null
    assert.equal(await read(query, true), undefined, 'A disappeared candidate cannot prove replacement')
    await assert.rejects(read(query, false), /Missing Windows process identity/, 'Initial and cleanup captures still require a process identity')
  }
  assert.deepEqual(await read(async pid => pid === original.pid ? original : app, true), { managedServer: original, managedApp: app })
  for (const query of [
    async () => { throw new Error('CIM query failed') },
    async () => undefined,
    async () => ({ ...original, birth: undefined }),
    async pid => { if (pid === original.pid) return original; throw new Error('Parent CIM query failed') },
    async pid => pid === original.pid ? original : { ...app, birth: undefined },
    async pid => pid === original.pid ? original : { ...app, exe: 'C:\\foreign\\Astral.exe' },
  ]) await assert.rejects(read(query, true), /CIM query failed|identity|outside this Desktop installation/)
})

test('the owned fixture parent closes its held leaf on stdin EOF and waits for that leaf to exit', async () => {
  const leaf = new EventEmitter()
  leaf.pid = 1234
  let killed = 0
  leaf.kill = () => { killed++ }
  const stdin = new EventEmitter()
  let resumed = false
  stdin.resume = () => { resumed = true }
  const exits = []
  runInNewContext(fixtureParent, {
    require: name => { assert.equal(name, 'node:child_process'); return { spawn: () => leaf } },
    process: { execPath: 'owned-node-fixture', stdin, exit: code => { exits.push(code) } },
    console: { log() {} },
  })
  assert.equal(resumed, true)
  stdin.emit('end')
  await Promise.resolve()
  assert.equal(killed, 1)
  assert.deepEqual(exits, [], 'The parent must not exit before its leaf closes')
  leaf.emit('close')
  await Promise.resolve()
  assert.deepEqual(exits, [0])
})

test('a failed first identity query still cleans its fixture by EOF without querying or killing a saved PID', async () => {
  let queries = 0
  await assert.rejects(windowsProcessIdentity(original.pid, { command: async () => { queries++; throw new Error('First CIM query failed') } }), /identity query.*failed/)
  let eof = 0
  let directKills = 0
  let finish
  const closed = new Promise(resolve => { finish = resolve })
  const child = { stdin: { end: () => { eof++; finish() } }, kill: () => { directKills++ } }
  await closeFixture(child, closed)
  assert.equal(queries, 1)
  assert.equal(eof, 1)
  assert.equal(directKills, 0, 'A responsive owned parent must clean its leaf before exiting')
})

test('native Windows identity and handle cleanup preserve a recycled-root candidate and terminate the verified process tree', { skip: process.platform !== 'win32' || process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true', timeout: 90_000 }, async t => {
  const child = spawn(process.execPath, ['-e', fixtureParent], { stdio: ['pipe', 'pipe', 'inherit'], windowsHide: true })
  child.stdin.on('error', () => {}) // The verified tree may exit before EOF cleanup.
  const closed = new Promise(resolve => child.once('close', resolve))
  t.after(() => closeFixture(child, closed))
  const leafPID = Number(String((await once(child.stdout, 'data'))[0]).trim())
  const root = await windowsProcessIdentity(child.pid)
  const leaf = await windowsProcessIdentity(leafPID)
  assertWindowsProcessOwner(root, process.execPath, process.pid)
  assertWindowsProcessOwner(leaf, process.execPath, root.pid)
  assert.equal(sameWindowsProcess(root, await windowsProcessIdentity(root.pid)), true)
  const stale = { ...root, birth: (BigInt(root.birth) + 1n).toString() }
  const skipped = await stopWindowsProcessTree(stale)
  assert.equal(skipped.stopped, false, 'A PID with another recorded birth must not be terminated')
  assert.equal(sameWindowsProcess(root, await windowsProcessIdentity(root.pid)), true)
  assert.equal(sameWindowsProcess(leaf, await windowsProcessIdentity(leaf.pid)), true)
  assert.equal((await stopWindowsProcessTree(root)).stopped, true)
  const deadline = Date.now() + 15_000
  let rootLive = true
  let leafLive = true
  while (Date.now() < deadline) {
    rootLive = sameWindowsProcess(root, await windowsProcessIdentity(root.pid))
    leafLive = sameWindowsProcess(leaf, await windowsProcessIdentity(leaf.pid))
    if (!rootLive && !leafLive) break
    await delay(100)
  }
  assert.equal(rootLive, false, 'The verified root process survived handle cleanup')
  assert.equal(leafLive, false, 'The verified descendant process survived tree cleanup')
})
