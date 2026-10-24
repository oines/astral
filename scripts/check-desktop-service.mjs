import assert from 'node:assert/strict'
import { execFile, fork } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
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

function alive(pid) {
  try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error }
}

async function waitForExit(pid) {
  const deadline = Date.now() + 20_000
  while (alive(pid) && Date.now() < deadline) await delay(100)
  assert.ok(!alive(pid), `The Server process ${pid} survived the death of its desktop parent`)
}

async function waitForParent(ended) {
  let timeout
  try {
    await Promise.race([ended, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('The desktop helper did not exit')), 20_000) })])
  } finally {
    clearTimeout(timeout)
  }
}

async function removeDirectory(directory, serverPIDs) {
  try {
    await rm(directory, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })
  } catch (error) {
    if (process.platform === 'win32' && error.code === 'EBUSY') {
      if (process.env.CI && process.env.ASTRAL_WINDOWS_HANDLE) {
        const rootName = directory.split(/[\\/]/).find(segment => segment.startsWith('astral 中文 desktop service '))
        // Use the unique ASCII suffix so the utility's path search does not
        // depend on the active Windows console code page.
        const query = rootName ? rootName.slice(-6) : directory
        const result = await execute(process.env.ASTRAL_WINDOWS_HANDLE, ['-accepteula', '-nobanner', query], { windowsHide: true, timeout: 15_000 }).catch(error => ({ stdout: error.stdout || '', stderr: error.stderr || error.message }))
        const report = `Test process: ${process.pid}\nDirectory: ${directory}\n${result.stdout}${result.stderr || ''}`
        console.error(report)
        await writeFile(path.join(process.env.RUNNER_TEMP, 'astral-handles.txt'), report)
      }
      console.error(`Managed Server processes: ${JSON.stringify([...serverPIDs].map(pid => ({ pid, alive: alive(pid) })))}`)
      const { stdout } = await execute('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', 'Get-CimInstance Win32_Process | Where-Object { $_.Name -match "^(astral.*|docker.*|com\\.docker.*|conhost|node|cmd|powershell|wsl|wslhost)\\.exe$" } | Select-Object ProcessId, ParentProcessId, Name, CreationDate | ConvertTo-Json -Compress'], { windowsHide: true, timeout: 10_000 }).catch(() => ({ stdout: 'Process diagnostics unavailable.' }))
      console.error(`Processes present after desktop lifecycle cleanup: ${stdout}`)
    }
    throw error
  }
}

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
  let parentEnded
  let orphanPID
  const serverPIDs = new Set()
  try {
    const missing = new LocalService()
    const missingDirectory = path.join(directory, 'failed startup')
    await assert.rejects(missing.start(path.join(directory, 'missing-binary'), missingDirectory, binaries), /ENOENT/)
    await removeDirectory(missingDirectory, new Set())
    console.log('Failed Server startup released its temporary directory.')
    const token = await service.start(binary, directory, binaries)
    serverPIDs.add(service.child.pid)
    const bootstrap = await fetch(`${service.url}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` } })
    assert.equal(bootstrap.status, 200)
    const first = await bootstrap.json()
    assert.equal(first.computers.length, 1)
    await assertNoLocalService(directory)
    await service.stop()
    assert.equal(service.child.exitCode, 0, 'The managed Server did not shut down gracefully')
    await assertNoLocalService(directory)

    parent = fork(fileURLToPath(import.meta.url), ['--child', directory], { stdio: ['ignore', 'ignore', 'inherit', 'ipc'] })
    parentEnded = new Promise(resolve => { parent.once('exit', resolve); parent.once('error', resolve) })
    const ready = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Managed child did not start')), 30_000)
      parent.once('error', error => { clearTimeout(timeout); reject(error) })
      parent.once('message', value => { clearTimeout(timeout); resolve(value) })
      parent.once('exit', code => { clearTimeout(timeout); reject(new Error(`Managed parent exited early (${code})`)) })
    })
    orphanPID = ready.pid
    serverPIDs.add(orphanPID)
    parent.kill('SIGKILL')
    await waitForParent(parentEnded)
    // A failed HTTP probe only proves that the listener stopped responding.
    // On Windows the process must exit before it releases its cwd handle.
    await waitForExit(orphanPID)
    orphanPID = undefined
    const reopened = new LocalService()
    try {
      const reopenedToken = await reopened.start(binary, directory, binaries)
      serverPIDs.add(reopened.child.pid)
      const response = await fetch(`${reopened.url}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${reopenedToken}` } })
      const next = await response.json()
      assert.equal(next.computers[0].id, first.computers[0].id)
      await assertNoLocalService(directory)
    } finally {
      await reopened.stop()
    }
    for (const pid of serverPIDs) assert.ok(!alive(pid), `Managed Server ${pid} is still running after the lifecycle test`)
    console.log('Desktop service startup, private Owner handoff, graceful stop, parent crash, identity-preserving restart, and absence of a separate local Daemon service passed.')
  } finally {
    parent?.kill('SIGTERM')
    if (parentEnded) await waitForParent(parentEnded)
    await service.stop()
    if (orphanPID && alive(orphanPID)) {
      process.kill(orphanPID, 'SIGKILL')
      await waitForExit(orphanPID)
    }
    await removeDirectory(directory, serverPIDs)
  }
}
