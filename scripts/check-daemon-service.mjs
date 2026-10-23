import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { LocalService } from '../apps/desktop/src/main/service.ts'

const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const binaries = path.join(repository, 'dist/bin')
const system = process.platform === 'win32' ? 'windows' : process.platform
const suffix = system === 'windows' ? '.exe' : ''
const binary = path.join(binaries, `astral-${system}-${process.arch === 'x64' ? 'amd64' : process.arch}${suffix}`)

// Hosted Linux runners often have no login session or systemd user manager.
// Do not replace native supervision with a background-process fallback.
if (system === 'linux') {
  try { await execute('systemctl', ['--user', 'show-environment'], { timeout: 5000 }) }
  catch {
    if (process.env.CI) throw new Error('The native Linux CI job must prepare its systemd user session before testing Daemon supervision.')
    console.log('SKIP native Daemon service lifecycle: this Linux session has no systemd user manager. Run in a logged-in Linux session to validate it.')
    process.exit(0)
  }
}

const temporary = await mkdtemp(path.join(tmpdir(), 'astral 原生 service '))
const daemonRoot = path.join(temporary, 'remote 电脑')
const server = new LocalService()
let created = false
let uninstalled = false
let cleanupFailure
const failures = []
process.env.ASTRAL_SERVER_LISTEN = '127.0.0.1:0'
delete process.env.ASTRAL_PUBLIC_URL
delete process.env.ASTRAL_TLS_CERT
delete process.env.ASTRAL_TLS_KEY

async function run(executable, ...args) {
  const result = await execute(executable, args, { cwd: repository, windowsHide: true, timeout: 90_000, maxBuffer: 1 << 20 })
  return result.stdout.trim()
}
async function state() { return JSON.parse(await run(binary, 'computer', 'service', 'status', '--root', daemonRoot)) }
async function api(url, token, body) {
  const response = await fetch(url, { method: body === undefined ? 'GET' : 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) })
  if (!response.ok) throw new Error(`Native service test received HTTP ${response.status}: ${await response.text()}`)
  return response.json()
}
async function waitStatus(credentials, expectedOnline, previousBoot) {
  const deadline = Date.now() + 45_000
  let last
  while (Date.now() < deadline) {
    last = await api(`${server.url}/internal/computers/${credentials.computer_id}/status`, credentials.token)
    if (last.online === expectedOnline && (!expectedOnline || !previousBoot || last.boot_id !== previousBoot)) return last
    await delay(200)
  }
  throw new Error(`Native Daemon did not reach ${expectedOnline ? 'a new online connection' : 'offline'}: ${JSON.stringify(last)}`)
}

async function reportFailureState() {
  // Collect evidence before uninstall removes the task and the temporary root.
  // Credentials, enrollment payloads and Owner handoff files contain secrets.
  try { console.error(`Managed Daemon status: ${await run(binary, 'computer', 'service', 'status', '--root', daemonRoot)}`) }
  catch (error) { console.error('Cannot read managed Daemon status:', error.message) }

  const files = ['daemon.log', 'service-launch.log', path.join('service', 'task.xml')]
  try {
    for (const name of await readdir(path.join(daemonRoot, 'service'))) {
      if (/^Astral Daemon \([a-f0-9]+\)\.ps1$/.test(name)) files.push(path.join('service', name))
    }
  } catch { /* Installation may have failed before creating its definition. */ }
  for (const file of files) {
    try {
      const raw = await readFile(path.join(daemonRoot, file))
      const encoding = raw[0] === 0xff && raw[1] === 0xfe ? 'utf16le' : 'utf8'
      console.error(`--- ${file} ---\n${raw.toString(encoding).slice(-16_384)}`)
    } catch (error) { console.error(`Cannot read ${file}: ${error.message}`) }
  }
  if (system === 'windows') {
    const quote = value => `'${value.replaceAll("'", "''")}'`
    const script = `$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new(); [xml]$definition=Get-Content -LiteralPath ${quote(path.join(daemonRoot, 'service', 'task.xml'))}; $task=Get-ScheduledTask -TaskPath '\\Astral\\' -TaskName $definition.Task.RegistrationInfo.Description; $info=$task | Get-ScheduledTaskInfo; [pscustomobject]@{Name=$task.TaskName;State=$task.State.ToString();UserId=$task.Principal.UserId;LogonType=$task.Principal.LogonType.ToString();RunLevel=$task.Principal.RunLevel.ToString();LastRunTime=$info.LastRunTime.ToString('o');LastTaskResult=('0x{0:X8}' -f $info.LastTaskResult);MissedRuns=$info.NumberOfMissedRuns;CallerSession=(Get-Process -Id $PID).SessionId} | ConvertTo-Json -Compress`
    try { console.error(`Windows task diagnostics: ${await run('powershell.exe', '-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script)}`) }
    catch (error) { console.error('Cannot inspect native Windows task:', error.message) }
    if (process.env.USERNAME) {
      try { console.error(`Current user logon sessions: ${await run('quser.exe', process.env.USERNAME)}`) }
      catch (error) { console.error('Current user logon session query:', error.message) }
    }
  }
}

try {
  const owner = await server.start(binary, path.join(temporary, 'server'), binaries)
  const enrollment = await api(`${server.url}/api/v1/computers/enrollments`, owner, { container: false })
  const enrollmentFile = path.join(temporary, 'enrollment.json')
  await writeFile(enrollmentFile, JSON.stringify({ server_url: server.url, enrollment_id: enrollment.enrollment.id, token: enrollment.token }), { mode: 0o600 })
  created = true
  await run(binary, 'computer', 'connect', '--enrollment-file', enrollmentFile, '--root', daemonRoot)
  const credentialFile = path.join(daemonRoot, 'credentials.json')
  const savedCredentials = await readFile(credentialFile, 'utf8')
  const credentials = JSON.parse(savedCredentials)
  const first = await waitStatus(credentials, true)
  const initialService = await state()
  assert.equal(initialService.state, 'running')
  assert.match(initialService.display_name, /^Astral Daemon \([a-f0-9]+\)$/)

  await run(binary, 'computer', 'service', 'restart', '--root', daemonRoot)
  const restarted = await waitStatus(credentials, true, first.boot_id)
  await run(binary, 'computer', 'service', 'stop', '--root', daemonRoot)
  await waitStatus(credentials, false)
  assert.equal((await state()).state, 'stopped')
  await run(binary, 'computer', 'service', 'start', '--root', daemonRoot)
  const started = await waitStatus(credentials, true, restarted.boot_id)

  const marker = path.join(daemonRoot, 'service-test-data', 'preserved.txt')
  await mkdir(path.dirname(marker), { recursive: true })
  await writeFile(marker, 'Existing Computer files survive a Daemon update.\n')
  const updatedBinary = path.join(temporary, `updated astral${suffix}`)
  await copyFile(binary, updatedBinary)
  if (system !== 'windows') await chmod(updatedBinary, 0o700)
  await run(updatedBinary, 'computer', 'update', '--server', server.url, '--root', daemonRoot)
  const updated = await waitStatus(credentials, true, started.boot_id)
  assert.equal(updated.id, first.id)
  assert.equal(updated.daemon_version, first.daemon_version)
  const definition = JSON.parse(await readFile(path.join(daemonRoot, 'service', 'spec.json'), 'utf8'))
  assert.equal(definition.executable, updatedBinary)
  assert.equal(await readFile(credentialFile, 'utf8'), savedCredentials)
  assert.equal(await readFile(marker, 'utf8'), 'Existing Computer files survive a Daemon update.\n')

  await run(binary, 'computer', 'service', 'uninstall', '--root', daemonRoot)
  uninstalled = true
  await waitStatus(credentials, false)
  assert.equal((await state()).state, 'not-installed')
  assert.equal(await readFile(credentialFile, 'utf8'), savedCredentials)
  console.log(`Native ${system} Daemon enrollment, user service supervision, restart, stop/start, identity-preserving update, and uninstall passed (${initialService.display_name}).`)
} catch (error) {
  failures.push(error)
  await reportFailureState()
} finally {
  if (created && !uninstalled) {
    try { await run(binary, 'computer', 'service', 'uninstall', '--root', daemonRoot) }
    catch (error) { cleanupFailure = error; failures.push(error) }
  }
  try { await server.stop() } catch (error) { failures.push(error) }
  if (cleanupFailure) {
    console.error(`Native service cleanup failed. Test data was retained at ${temporary}; uninstall only that test service before removing it.`)
  } else {
    await rm(temporary, { recursive: true, force: true })
  }
}
if (failures.length) throw new AggregateError(failures, 'Native Daemon service lifecycle failed.')
