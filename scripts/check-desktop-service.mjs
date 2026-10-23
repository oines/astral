import assert from 'node:assert/strict'
import { execFile, fork } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir, userInfo } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { LocalService } from '../apps/desktop/src/main/service.ts'

const root = fileURLToPath(new URL('../', import.meta.url))
const binaries = path.join(root, 'dist/bin')
const system = process.platform === 'win32' ? 'windows' : process.platform
const binary = path.join(binaries, `astral-${system}-${process.arch === 'x64' ? 'amd64' : process.arch}${system === 'windows' ? '.exe' : ''}`)
process.env.ASTRAL_SERVER_LISTEN = '127.0.0.1:0'
const execute = promisify(execFile)

async function assertNoLocalService(directory) {
  const localRoot = path.join(directory, 'computers', 'local')
  await assert.rejects(readFile(path.join(localRoot, 'service', 'spec.json')), { code: 'ENOENT' })
  const { stdout } = await execute(binary, ['computer', 'service', 'status', '--root', localRoot], { windowsHide: true, timeout: 5000 })
  const status = JSON.parse(stdout)
  assert.equal(status.state, 'not-installed', 'The desktop local Daemon was registered as a user service')
  const args = system === 'darwin'
    ? ['/bin/launchctl', ['print', `gui/${userInfo().uid}/app.astral.daemon.${status.id}`]]
    : system === 'windows' ? ['schtasks.exe', ['/query', '/tn', `\\Astral\\${status.display_name}`]] : undefined
  if (args) await assert.rejects(execute(args[0], args[1], { windowsHide: true, timeout: 5000 }), error => typeof error.code === 'number', 'A separate desktop local Daemon job exists')
}

if (process.argv[2] === '--child') {
  const service = new LocalService()
  await service.start(binary, process.argv[3], binaries)
  process.send({ pid: service.child.pid, url: service.url })
} else {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral 中文 desktop service '))
  const service = new LocalService()
  let parent
  try {
    const missing = new LocalService()
    await assert.rejects(missing.start(path.join(directory, 'missing-binary'), directory, binaries), /ENOENT/)
    const token = await service.start(binary, directory, binaries)
    const bootstrap = await fetch(`${service.url}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` } })
    assert.equal(bootstrap.status, 200)
    const first = await bootstrap.json()
    assert.equal(first.computers.length, 1)
    await assertNoLocalService(directory)
    await service.stop()
    assert.equal(service.child.exitCode, 0, 'The managed Server did not shut down gracefully')
    await assertNoLocalService(directory)

    parent = fork(fileURLToPath(import.meta.url), ['--child', directory], { stdio: ['ignore', 'ignore', 'inherit', 'ipc'] })
    const ready = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Managed child did not start')), 30_000)
      parent.once('error', error => { clearTimeout(timeout); reject(error) })
      parent.once('message', value => { clearTimeout(timeout); resolve(value) })
      parent.once('exit', code => { clearTimeout(timeout); reject(new Error(`Managed parent exited early (${code})`)) })
    })
    parent.kill('SIGKILL')
    const deadline = Date.now() + 10_000
    let stopped = false
    while (Date.now() < deadline) {
      try { await fetch(`${ready.url}/health`, { signal: AbortSignal.timeout(1000) }) }
      catch { stopped = true; break }
      await delay(100)
    }
    assert.ok(stopped, 'The Server survived the death of its desktop parent')
    const reopened = new LocalService()
    try {
      const reopenedToken = await reopened.start(binary, directory, binaries)
      const response = await fetch(`${reopened.url}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${reopenedToken}` } })
      const next = await response.json()
      assert.equal(next.computers[0].id, first.computers[0].id)
      await assertNoLocalService(directory)
    } finally {
      await reopened.stop()
    }
    console.log('Desktop service startup, private Owner handoff, graceful stop, parent crash, identity-preserving restart, and absence of a separate local Daemon service passed.')
  } finally {
    parent?.kill('SIGTERM')
    await service.stop()
    await rm(directory, { recursive: true, force: true })
  }
}
