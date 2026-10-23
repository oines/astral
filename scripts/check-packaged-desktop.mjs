import assert from 'node:assert/strict'
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
const executable = process.platform === 'darwin' ? path.join(release, unpacked, 'Astral.app/Contents/MacOS/Astral') : path.join(release, unpacked, process.platform === 'win32' ? 'Astral.exe' : 'astral')
await access(executable)
const temporary = await mkdtemp(path.join(tmpdir(), 'astral 打包 App '))
const data = path.join(temporary, 'Astral 数据')
const runningFile = path.join(data, 'server/running.json')
const environment = { ...process.env, ASTRAL_DESKTOP_DATA_DIR: data, ASTRAL_SERVER_LISTEN: '127.0.0.1:0', XDG_CONFIG_HOME: path.join(temporary, 'xdg-config') }
for (const key of ['ASTRAL_PUBLIC_URL', 'ASTRAL_TLS_CERT', 'ASTRAL_TLS_KEY', 'ELECTRON_RUN_AS_NODE']) delete environment[key]
const args = [`--user-data-dir=${path.join(temporary, 'Electron 资料')}`]
if (process.platform === 'linux' && process.getuid?.() === 0) args.push('--no-sandbox') // Only disposable root CI containers require this.
await mkdir(data, { recursive: true, mode: 0o700 })
const app = spawn(executable, args, { cwd: repository, env: environment, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: false })
let output = ''
let failure
let exit
let running
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
  throw new Error(message)
}
async function rendererStarted() {
  if (process.platform === 'win32') {
    const script = `$smokeProcesses=@(Get-CimInstance Win32_Process); $smokeIds=[Collections.Generic.HashSet[uint32]]::new(); [void]$smokeIds.Add(${app.pid}); for($smokePass=0;$smokePass -lt 8;$smokePass++){foreach($smokeProcess in $smokeProcesses){if($smokeIds.Contains([uint32]$smokeProcess.ParentProcessId)){[void]$smokeIds.Add([uint32]$smokeProcess.ProcessId)}}}; [bool]@($smokeProcesses | Where-Object {$smokeIds.Contains([uint32]$_.ProcessId) -and $_.CommandLine -match '--type=renderer(\\s|$)'}).Count`
    const result = await execute('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script], { windowsHide: true, timeout: 10_000 })
    return result.stdout.trim().toLowerCase() === 'true'
  }
  const result = await execute('ps', ['-axo', 'pid=,ppid=,args='], { timeout: 5000, maxBuffer: 8 << 20 })
  const processes = result.stdout.split('\n').flatMap(line => { const match = /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line); return match ? [{ pid: Number(match[1]), parent: Number(match[2]), args: match[3] }] : [] })
  const ids = new Set([app.pid])
  for (let pass = 0; pass < 8; pass++) for (const process of processes) if (ids.has(process.parent)) ids.add(process.pid)
  return processes.some(process => ids.has(process.pid) && /--type=renderer(?:\s|$)/.test(process.args))
}

try {
  await waitFor(async () => {
    if (failure) throw failure
    if (exit) throw new Error(`Packaged App exited before startup (${JSON.stringify(exit)}).\n${output}`)
    running = await readFile(runningFile, 'utf8').then(JSON.parse).catch(() => undefined)
    if (!running) return false
    const token = (await readFile(path.join(data, 'server/owner.token'), 'utf8')).trim()
    const response = await fetch(`${running.url}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(3000) })
    assert.equal(response.status, 200)
    const bootstrap = await response.json()
    return bootstrap.computers.some(computer => computer.local && computer.online)
  }, 'The packaged App did not start its Server and connect the local Daemon.', 45_000)
  await waitFor(async () => { if (exit) throw new Error(`Packaged App exited before renderer startup.\n${output}`); return rendererStarted() }, 'The actual packaged Electron App did not start a renderer.', 30_000)
  await assert.rejects(readFile(path.join(data, 'computers/local/service/spec.json')), { code: 'ENOENT' })
  app.kill('SIGTERM')
  // The existing Unix SIGTERM handler calls app.quit(). Windows termination is
  // abrupt: this branch deliberately verifies parent-death cleanup, not Quit.
  let timer
  try { await Promise.race([ended, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Packaged App did not exit.')), 30_000) })]) }
  finally { clearTimeout(timer) }
  if (process.platform !== 'win32') assert.equal(exit.code, 0, `Packaged App did not quit normally: ${JSON.stringify(exit)}`)
  await waitFor(() => !alive(running.pid), 'The Go Server survived the packaged Electron App.', 20_000)
  await assert.rejects(readFile(path.join(data, 'computers/local/service/spec.json')), { code: 'ENOENT' })
  console.log(`Actual packaged ${process.platform}/${arch} App startup, renderer, online local Daemon, no local user-service registration, and ${process.platform === 'win32' ? 'crash cleanup' : 'normal app.quit shutdown'} passed.`)
} catch (error) {
  console.error(output)
  throw error
} finally {
  if (!exit && app.pid) { app.kill('SIGKILL'); await ended }
  if (running?.pid) await waitFor(() => !alive(running.pid), `Packaged smoke left a Server running; inspect ${temporary}.`, 20_000)
  await rm(temporary, { recursive: true, force: true })
}
