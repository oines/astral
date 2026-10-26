import assert from 'node:assert/strict'
import { webFixtureCookie } from './web-auth-fixture.mjs'
import { rendererAuthentication } from './desktop-renderer-auth.mjs'
import { execFile, spawn } from 'node:child_process'
import { access, mkdir, mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

// Packaged startup applies the real operating-system login preference. Run only
// under a disposable CI user, never against a developer's existing login items.
if (!process.env.CI) throw new Error('Run this packaged-App smoke under a disposable CI user; it must not change an existing user’s Astral login preference.')
if (process.platform === 'linux' && !process.env.DISPLAY && !process.env.WAYLAND_DISPLAY) throw new Error('A Linux graphical session is required; run this smoke with xvfb-run -a.')

const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const release = path.join(repository, 'apps/desktop/release')
const arch = process.arch
const unpacked = process.platform === 'darwin' ? (arch === 'arm64' ? 'mac-arm64' : 'mac') : `${process.platform === 'win32' ? 'win' : 'linux'}${arch === 'x64' ? '' : `-${arch}`}-unpacked`
const executable = process.env.ASTRAL_PACKAGED_APP_EXECUTABLE || (process.platform === 'darwin' ? path.join(release, unpacked, 'Astral.app/Contents/MacOS/Astral') : path.join(release, unpacked, process.platform === 'win32' ? 'Astral.exe' : 'astral'))
await access(executable)
console.log(`Packaged App executable: ${executable}`)
const temporary = await mkdtemp(path.join(tmpdir(), 'astral 打包 App '))
const data = path.join(temporary, 'Astral 数据')
const runningFile = path.join(data, 'server/running.json')
const environment = { ...process.env, ASTRAL_DESKTOP_DATA_DIR: data, ASTRAL_SERVER_LISTEN: '127.0.0.1:0', XDG_CONFIG_HOME: path.join(temporary, 'xdg-config') }
for (const key of ['ASTRAL_PUBLIC_URLS', 'ASTRAL_ALLOW_REMOTE_MANAGEMENT', 'ELECTRON_RUN_AS_NODE']) delete environment[key]
const electronProfile = path.join(temporary, 'Electron 资料')
// A loopback-only ephemeral CDP endpoint exists only in this disposable smoke.
// It proves the real renderer receives native authentication without exposing
// Desktop credentials or changing production startup switches.
const args = [`--user-data-dir=${electronProfile}`, '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1']
if (process.platform === 'linux' && process.getuid?.() === 0) args.push('--no-sandbox') // Only disposable root CI containers require this.
await mkdir(data, { recursive: true, mode: 0o700 })
const app = spawn(executable, args, { cwd: repository, env: environment, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: false })
let output = ''
let failure
let exit
let running
let rendererDiagnostic = 'The managed renderer has not been discovered.'
const windowsDescendants = new Map()
let windowsAppBirth
app.stdout.on('data', chunk => { output = (output + chunk).slice(-6000) })
app.stderr.on('data', chunk => { output = (output + chunk).slice(-6000) })
app.on('error', error => { failure = error })
const ended = new Promise(resolve => app.once('exit', (code, signal) => { exit = { code, signal }; resolve() }))

function alive(pid) {
  try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error }
}
async function waitFor(check, message, timeout = 30_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await check()) return
    await delay(250)
  }
  throw new Error(typeof message === 'function' ? message() : message)
}
async function rendererStarted() {
  if (process.platform === 'win32') return windowsRendererStarted()
  const result = await execute('ps', ['-axo', 'pid=,ppid=,args='], { timeout: 5000, maxBuffer: 8 << 20 })
  const processes = result.stdout.split('\n').flatMap(line => { const match = /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line); return match ? [{ pid: Number(match[1]), parent: Number(match[2]), args: match[3] }] : [] })
  const ids = new Set([app.pid])
  for (let pass = 0; pass < 8; pass++) for (const process of processes) if (ids.has(process.parent)) ids.add(process.pid)
  return processes.some(process => ids.has(process.pid) && /--type=renderer(?:\s|$)/.test(process.args))
}

async function windowsSnapshot(script, timeout = 30_000) {
  const { stdout } = await execute('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', `$ErrorActionPreference = 'Stop'\n${script}`], { windowsHide: true, timeout })
  return JSON.parse(stdout)
}

async function windowsRendererStarted() {
  // Chromium children use the packaged executable's name. Asking WMI for every
  // process and command line can exceed the startup budget on a cold CI runner.
  const executableName = path.basename(executable).replaceAll('\\', '\\\\').replaceAll("'", "\\'")
  const filter = `ProcessId = ${app.pid} OR Name = '${executableName}'`.replaceAll("'", "''")
  const snapshot = await windowsSnapshot(`
$smokeProcesses = @(Get-CimInstance Win32_Process -Filter '${filter}' -Property ProcessId, ParentProcessId, CreationDate, CommandLine)
$smokeById = @{}
foreach ($smokeProcess in $smokeProcesses) { $smokeById[[uint32]$smokeProcess.ProcessId] = $smokeProcess }
$smokeApp = $smokeById[[uint32]${app.pid}]
if ($null -eq $smokeApp -or $null -eq $smokeApp.CreationDate) { throw 'The packaged App has no readable process identity.' }
$smokeIds = [Collections.Generic.HashSet[uint32]]::new()
[void]$smokeIds.Add(${app.pid})
for ($smokePass = 0; $smokePass -lt 8; $smokePass++) {
  foreach ($smokeProcess in $smokeProcesses) {
    $smokeParent = $smokeById[[uint32]$smokeProcess.ParentProcessId]
    if ($null -eq $smokeParent -or -not $smokeIds.Contains([uint32]$smokeParent.ProcessId)) { continue }
    if ($null -eq $smokeProcess.CreationDate) { throw 'A packaged App descendant has no readable creation date.' }
    if ($smokeProcess.CreationDate -ge $smokeParent.CreationDate) { [void]$smokeIds.Add([uint32]$smokeProcess.ProcessId) }
  }
}
$smokeTree = @($smokeProcesses | Where-Object { $smokeIds.Contains([uint32]$_.ProcessId) -and $_.ProcessId -ne ${app.pid} })
[pscustomobject]@{
  birth = $smokeApp.CreationDate.ToUniversalTime().Ticks.ToString([Globalization.CultureInfo]::InvariantCulture)
  renderer = [bool]@($smokeTree | Where-Object { $_.CommandLine -match '--type=renderer(\\s|$)' }).Count
  descendants = @($smokeTree | ForEach-Object {
    [pscustomobject]@{ pid = [int]$_.ProcessId; birth = $_.CreationDate.ToUniversalTime().Ticks.ToString([Globalization.CultureInfo]::InvariantCulture) }
  })
} | ConvertTo-Json -Depth 3 -Compress
`)
  if (failure) throw failure
  if (exit) throw new Error(`Packaged App exited during renderer discovery.\n${output}`)
  if (windowsAppBirth && windowsAppBirth !== snapshot.birth) throw new Error('The packaged App PID was reused before descendant discovery.')
  windowsAppBirth = snapshot.birth
  for (const { pid, birth } of snapshot.descendants) windowsDescendants.set(pid, birth)
  return snapshot.renderer
}

async function pruneWindowsDescendants(timeout) {
  const filter = [...windowsDescendants.keys()].map(pid => `ProcessId = ${pid}`).join(' OR ')
  const snapshot = await windowsSnapshot(`
$smokeProcesses = @(Get-CimInstance Win32_Process -Filter '${filter}' -Property ProcessId, CreationDate)
$smokeLive = @($smokeProcesses | ForEach-Object {
  if ($null -eq $_.CreationDate) { throw 'A recorded process has no readable creation date.' }
  [pscustomobject]@{ pid = [int]$_.ProcessId; birth = $_.CreationDate.ToUniversalTime().Ticks.ToString([Globalization.CultureInfo]::InvariantCulture) }
})
ConvertTo-Json -InputObject $smokeLive -Compress
`, timeout)
  const live = new Map(snapshot.map(({ pid, birth }) => [pid, birth]))
  // Missing or replaced identities have exited. A reused PID is another process.
  for (const [pid, birth] of windowsDescendants) if (live.get(pid) !== birth) windowsDescendants.delete(pid)
}

async function waitForWindowsDescendants() {
  if (process.platform !== 'win32') return
  const deadline = Date.now() + 20_000
  while (windowsDescendants.size > 0 && Date.now() < deadline) {
    await pruneWindowsDescendants(Math.max(1, Math.min(10_000, deadline - Date.now())))
    if (windowsDescendants.size === 0) return
    await delay(250)
  }
  if (windowsDescendants.size > 0) throw new Error(`The packaged App left descendant processes running: ${[...windowsDescendants.keys()].join(', ')}. Inspect ${temporary}.`)
}

try {
  await waitFor(async () => {
    if (failure) throw failure
    if (exit) throw new Error(`Packaged App exited before startup (${JSON.stringify(exit)}).\n${output}`)
    running = await readFile(runningFile, 'utf8').then(JSON.parse).catch(() => undefined)
    if (!running) return false
    const cookie = await webFixtureCookie(path.join(data,'server'),running.url)
    let response
    try {
      // Bootstrap's optional Docker probe has its own three-second budget.
      // A cold Windows CLI must not consume the entire client's budget too.
      response = await fetch(`${running.url}/api/v1/bootstrap`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(10_000) })
    } catch (error) {
      if (error.name === 'TimeoutError' || ['ECONNREFUSED', 'ECONNRESET', 'ETIMEDOUT', 'EPIPE'].includes(error.cause?.code)) return false
      throw error
    }
    assert.equal(response.status, 200)
    const bootstrap = await response.json()
    assert.equal(bootstrap.onboarding.completed, false, 'The packaged App smoke must use a fresh onboarding profile')
    return bootstrap.computers.some(computer => computer.local && computer.online)
  }, 'The packaged App did not start its Server and connect the local Daemon.', 45_000)
  await waitFor(async () => { if (exit) throw new Error(`Packaged App exited before renderer startup.\n${output}`); return rendererStarted() }, 'The actual packaged Electron App did not start a renderer.', 30_000)
  await waitFor(async () => {
    if (failure) throw failure
    if (exit) throw new Error(`Packaged App exited before renderer authentication.\n${output}`)
    return rendererAuthentication(electronProfile, running.url, { diagnose: message => { rendererDiagnostic = message } })
  }, () => `The actual Desktop renderer did not authenticate with its native identity.\nLast renderer state: ${rendererDiagnostic}`, 30_000)
  console.log('Actual Desktop renderer /auth/status confirms authenticated=true and desktop=true.')
  await assert.rejects(readFile(path.join(data, 'computers/local/service/spec.json')), { code: 'ENOENT' })
  console.log(`Requesting ${process.platform === 'win32' ? 'crash cleanup' : 'normal App quit'} with SIGTERM (App PID ${app.pid}, Server PID ${running.pid}).`)
  if (process.platform === 'win32') await rendererStarted()
  assert.ok(app.kill('SIGTERM'), 'The packaged App could not receive the shutdown request.')
  // The existing Unix SIGTERM handler calls app.quit(). Windows termination is
  // abrupt: this branch deliberately verifies parent-death cleanup, not Quit.
  let timer
  try { await Promise.race([ended, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`Packaged App did not exit; Go Server PID ${running.pid} is ${alive(running.pid) ? 'still running' : 'already stopped'}.`)), 30_000) })]) }
  finally { clearTimeout(timer) }
  if (process.platform !== 'win32') assert.equal(exit.code, 0, `Packaged App did not quit normally: ${JSON.stringify(exit)}`)
  await waitFor(() => !alive(running.pid), 'The Go Server survived the packaged Electron App.', 20_000)
  await waitForWindowsDescendants()
  await assert.rejects(readFile(path.join(data, 'computers/local/service/spec.json')), { code: 'ENOENT' })
  console.log(`Actual packaged ${process.platform}/${arch} App startup, authenticated native renderer, online local Daemon, no local user-service registration, and ${process.platform === 'win32' ? 'crash cleanup' : 'normal app.quit shutdown'} passed.`)
} catch (error) {
  console.error(output)
  throw error
} finally {
  if (!exit && app.pid) {
    if (process.platform === 'win32') await rendererStarted().catch(error => console.error(`Could not refresh packaged App descendants before cleanup: ${error.message}`))
    app.kill('SIGKILL')
    await ended
  }
  if (running?.pid) await waitFor(() => !alive(running.pid), `Packaged smoke left a Server running; inspect ${temporary}.`, 20_000)
  await waitForWindowsDescendants()
  await rm(temporary, { recursive: true, force: true, ...(process.platform === 'win32' ? { maxRetries: 5, retryDelay: 200 } : {}) })
}
