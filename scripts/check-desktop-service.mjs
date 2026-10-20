import assert from 'node:assert/strict'
import { fork } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { LocalService } from '../apps/desktop/src/main/service.ts'

const root = fileURLToPath(new URL('../', import.meta.url))
const binaries = path.join(root, 'dist/bin')
const binary = path.join(binaries, `astral-${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`)
process.env.ASTRAL_SERVER_LISTEN = '127.0.0.1:0'

if (process.argv[2] === '--child') {
  const service = new LocalService()
  await service.start(binary, process.argv[3], binaries)
  process.send({ pid: service.child.pid, url: service.url })
} else {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-desktop-service-'))
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
    await service.stop()

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
    } finally {
      await reopened.stop()
    }
    console.log('Desktop service startup, private Owner handoff, graceful stop, parent crash, and identity-preserving restart passed.')
  } finally {
    parent?.kill('SIGTERM')
    await service.stop()
    await rm(directory, { recursive: true, force: true })
  }
}
