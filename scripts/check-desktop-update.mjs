// Disposable native CI users only. This installs and replaces a real application.
// The production source/feed never changes: only a copied fixture's compiled
// main module points at a loopback server serving the final release bytes.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile, spawn } from 'node:child_process'
import { createReadStream } from 'node:fs'
import { access, chmod, copyFile, cp, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { homedir, tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { evaluateRenderer } from './desktop-renderer-auth.mjs'
import { desktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { readReleaseVersion } from './release-version.mjs'
import { webFixtureCookie } from './web-auth-fixture.mjs'

if (process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('Real Desktop update acceptance requires a disposable GitHub Actions user; never run on a developer account.')
if (!['darwin', 'win32', 'linux'].includes(process.platform) || !['arm64', 'x64'].includes(process.arch)) throw new Error('Unsupported native update platform.')
const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const desktop = path.join(repository, 'apps/desktop')
const version = await readReleaseVersion()
const architecture = process.arch
const platform = { darwin: 'mac', win32: 'win', linux: 'linux' }[process.platform]
const appData = process.platform === 'darwin' ? path.join(homedir(), 'Library/Application Support') : process.platform === 'win32' ? process.env.APPDATA : process.env.XDG_CONFIG_HOME || path.join(homedir(), '.config')
assert.ok(appData, 'Missing native application data location')
const data = path.join(appData, 'astral/schema-3')
const profile = path.join(appData, 'Astral')
for (const existing of [data, profile]) {
  try { await access(existing); throw new Error('Existing Astral data/profile is present; refusing to run the replacement test.') } catch (error) { if (error.code !== 'ENOENT') throw error }
}
// electron-builder's safe-path policy explicitly permits the native temp root.
const temporary = await realpath(await mkdtemp(path.join(tmpdir(), 'astral-update-acceptance-')))
const fixtureApp = path.join(temporary, 'fixture-app')
const output = path.join(temporary, 'fixture-packages')
const installed = path.join(temporary, 'installed')
const targetName = `Astral-${version}-${platform}-${architecture}.${process.platform === 'darwin' ? 'zip' : process.platform === 'win32' ? 'exe' : 'AppImage'}`
const target = path.join(desktop, 'release', targetName)
const targetBytes = await readFile(target)
const feedName = desktopUpdateFeeds(version).find(feed => feed.asset === targetName)?.name
assert.ok(feedName, 'No native update metadata contract')
const checksum = createHash('sha512').update(targetBytes).digest('base64')
const logs = []
let app
let oldServer
let newServer
let updatedAppPID
let executable
let installedRoot
let retain = false
const command = (program, args, options = {}) => execute(program, args, { cwd: repository, encoding: 'utf8', timeout: 600_000, maxBuffer: 2 << 20, ...options })
const alive = pid => { try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error } }
const server = createServer((request, response) => {
  const name = new URL(request.url, 'http://127.0.0.1').pathname.slice(1)
  if (name === feedName) {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ version, files: [{ url: `http://127.0.0.1:${server.address().port}/${targetName}`, sha512: checksum, size: targetBytes.length }] }))
  } else if (name === targetName) {
    response.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Length': targetBytes.length })
    createReadStream(target).pipe(response)
  } else { response.writeHead(404); response.end() }
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const fixtureFeed = `http://127.0.0.1:${server.address().port}/`

async function waitFor(read, message, timeout = 120_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const value = await read()
    if (value) return value
    await delay(300)
  }
  throw new Error(message)
}
async function running() {
  const ready = await readFile(path.join(data, 'server/running.json'), 'utf8').then(JSON.parse).catch(() => undefined)
  if (!ready || !alive(ready.pid)) return undefined
  const health = await fetch(`${ready.url}/health`, { signal: AbortSignal.timeout(3000) }).then(response => response.json()).catch(() => undefined)
  return health?.status === 'ok' ? ready : undefined
}
async function renderer() {
  const port = await readFile(path.join(profile, 'DevToolsActivePort'), 'utf8').then(raw => Number(raw.split('\n')[0])).catch(() => undefined)
  if (!port) return undefined
  const targets = await fetch(`http://127.0.0.1:${port}/json/list`).then(response => response.json()).catch(() => [])
  const page = targets.find(item => item.type === 'page' && item.url.startsWith(oldServer.url))
  return page?.webSocketDebuggerUrl
}
async function fingerprint(ready) {
  const cookie = await webFixtureCookie(path.join(data, 'server'), ready.url)
  const response = await fetch(`${ready.url}/api/v1/bootstrap`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(5000) })
  assert.ok(response.ok)
  const bootstrap = await response.json()
  const local = bootstrap.computers.find(computer => computer.local && computer.online)
  if (!local) return undefined
  return { local: local.id, master: await readFile(path.join(data, 'server/master.key')), identity: await readFile(path.join(data, 'server/computer-identity/identity.pem')) }
}
async function parentPID(pid) {
  const result = process.platform === 'win32'
    ? await command('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', `(Get-CimInstance Win32_Process -Filter 'ProcessId = ${pid}').ParentProcessId`])
    : await command('ps', ['-o', 'ppid=', '-p', String(pid)])
  const parent = Number(result.stdout.trim())
  assert.ok(Number.isSafeInteger(parent) && parent > 1 && parent !== process.pid, 'The managed Server has no safe Desktop parent identity')
  return parent
}

try {
  await mkdir(fixtureApp)
  await cp(path.join(desktop, 'out'), path.join(fixtureApp, 'out'), { recursive: true })
  const packageJSON = JSON.parse(await readFile(path.join(desktop, 'package.json'), 'utf8'))
  packageJSON.version = '0.0.0'
  await writeFile(path.join(fixtureApp, 'package.json'), JSON.stringify(packageJSON))
  await symlink(path.join(desktop, 'node_modules'), path.join(fixtureApp, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir')
  const main = path.join(fixtureApp, 'out/main/index.js')
  const source = await readFile(main, 'utf8')
  const productionFeed = 'https://github.com/oines/astral/releases/latest/download/'
  assert.equal(source.split(productionFeed).length, 2, 'Fixture must replace exactly one compiled update source')
  await writeFile(main, source.replace(productionFeed, fixtureFeed))
  const builder = ['--filter', '@astral/desktop', 'exec', 'electron-builder', '--config.directories.app=' + fixtureApp, '--config.directories.output=' + output, '--publish', 'never', `--${architecture}`]
  if (process.platform === 'darwin') builder.push('--config', 'electron-builder.release.cjs', '--mac', '--dir')
  else if (process.platform === 'win32') builder.push('--win', 'nsis')
  else builder.push('--linux', 'AppImage')
  // Windows pnpm is a command shim, so invoke it through cmd only with fixed arguments.
  if (process.platform === 'win32') {
    assert.ok(builder.every(value => !/["%!\r\n]/.test(value)), 'Unsupported shell character in the controlled CI fixture path')
    const line = ['pnpm', ...builder].map(value => `"${value}"`).join(' ')
    await command('cmd.exe', ['/d', '/s', '/c', `"${line}"`], { windowsVerbatimArguments: true })
  } else await command('pnpm', builder)
  if (process.platform === 'darwin') {
    installedRoot = path.join(installed, 'Astral.app')
    await mkdir(installed)
    const appDirectory = architecture === 'arm64' ? 'mac-arm64' : 'mac'
    await command('/usr/bin/ditto', [path.join(output, appDirectory, 'Astral.app'), installedRoot])
    executable = path.join(installedRoot, 'Contents/MacOS/Astral')
  } else if (process.platform === 'win32') {
    installedRoot = path.join(installed, 'Astral')
    await command(path.join(output, `Astral-0.0.0-win-${architecture}.exe`), ['/S', `/D=${installedRoot}`])
    executable = path.join(installedRoot, 'Astral.exe')
  } else {
    await mkdir(installed)
    executable = path.join(installed, 'Astral.AppImage')
    await copyFile(path.join(output, `Astral-0.0.0-linux-${architecture}.AppImage`), executable)
    await chmod(executable, 0o755)
  }
  const environment = { ...process.env, ASTRAL_SERVER_LISTEN: '127.0.0.1:0' }
  for (const key of ['ELECTRON_RUN_AS_NODE', 'ASTRAL_DESKTOP_DATA_DIR', 'ASTRAL_PUBLIC_URLS', 'ASTRAL_ALLOW_REMOTE_MANAGEMENT']) delete environment[key]
  if (process.platform === 'linux') environment.APPIMAGE_EXTRACT_AND_RUN = '1'
  // Use the same native profile on both launches, including its instance lock.
  app = spawn(executable, ['--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1'], { cwd: repository, env: environment, stdio: ['ignore', 'pipe', 'pipe'] })
  app.stdout.on('data', chunk => logs.push(String(chunk)))
  app.stderr.on('data', chunk => logs.push(String(chunk)))
  app.on('error', error => logs.push(error.message))
  oldServer = await waitFor(running, 'The old Desktop fixture did not start its real Server.')
  const before = await waitFor(() => fingerprint(oldServer), 'The fixture Local Computer did not connect.')
  const marker = path.join(data, 'server/update-acceptance-marker')
  await writeFile(marker, 'retained-data-0.0.0')
  const endpoint = await waitFor(renderer, 'The old Desktop fixture did not expose its native renderer.')
  const oldState = await evaluateRenderer(endpoint, 'window.astralDesktop.updates.state()')
  assert.equal(oldState.currentVersion, '0.0.0')
  assert.notEqual(oldState.phase, 'unavailable', `The fixture does not support native updating: ${oldState.unavailable}`)
  await evaluateRenderer(endpoint, 'window.astralDesktop.updates.check()')
  await waitFor(async () => {
    const state = await evaluateRenderer(endpoint, 'window.astralDesktop.updates.state()')
    if (state.phase === 'error') throw new Error(`Real updater download failed: ${state.error}`)
    return state.phase === 'ready'
  }, 'The real updater did not finish downloading the final release.')
  // The native IPC closes when Electron exits; that close is expected here.
  await evaluateRenderer(endpoint, 'window.astralDesktop.updates.install().then(() => true)').catch(() => undefined)
  newServer = await waitFor(async () => {
    const ready = await running()
    return ready && ready.pid !== oldServer.pid ? ready : undefined
  }, 'The updated Desktop did not relaunch its Server.', 180_000)
  updatedAppPID = await parentPID(newServer.pid)
  assert.notEqual(updatedAppPID, app.pid, 'The original Desktop process survived the replacement')
  await waitFor(() => app.exitCode !== null || app.signalCode !== null, 'The original Desktop process did not exit after installation.')
  assert.equal(alive(oldServer.pid), false, 'The old Server survived automatic replacement')
  const after = await waitFor(() => fingerprint(newServer), 'The updated Local Computer did not reconnect.')
  assert.equal(before.local, after.local)
  assert.ok(before.master.equals(after.master), 'The update changed the encryption key')
  assert.ok(before.identity.equals(after.identity), 'The update changed the pairing identity')
  assert.equal(await readFile(marker, 'utf8'), 'retained-data-0.0.0')
  const hash = file => readFile(file).then(bytes => createHash('sha256').update(bytes).digest('hex'))
  if (process.platform === 'linux') assert.equal(await hash(executable), await hash(target), 'Installed AppImage bytes differ from the final release')
  else {
    const directory = process.platform === 'darwin' ? (architecture === 'arm64' ? 'mac-arm64/Astral.app/Contents/Resources' : 'mac/Astral.app/Contents/Resources') : (architecture === 'arm64' ? 'win-arm64-unpacked/resources' : 'win-unpacked/resources')
    const installedAsar = path.join(installedRoot, process.platform === 'darwin' ? 'Contents/Resources/app.asar' : 'resources/app.asar')
    assert.equal(await hash(installedAsar), await hash(path.join(desktop, 'release', directory, 'app.asar')), 'Installed app.asar differs from the final release')
  }
  console.log(`Actual ${process.platform}/${architecture} Desktop 0.0.0 → ${version}: final release download, old Server exit, installer replacement, relaunch and retained data passed. This tests the updater mechanism with the same Go payload; versioned schema conversion is verified separately.`)
} catch (error) {
  retain = true
  console.error(`Update acceptance failed; fixture retained at ${temporary}.`)
  // Logs can include private loopback addresses; keep them on the disposable runner.
  await writeFile(path.join(temporary, 'app-output.log'), logs.join('').slice(-20_000))
  await writeFile(path.join(process.env.RUNNER_TEMP, 'astral-update-acceptance.log'), `Fixture: ${temporary}\n${logs.join('').slice(-20_000)}`)
  throw error
} finally {
  const ready = await running().catch(() => undefined)
  const cleanupAppPID = updatedAppPID || (ready ? await parentPID(ready.pid).catch(() => undefined) : undefined)
  if (process.platform === 'win32') {
    if (cleanupAppPID && alive(cleanupAppPID)) await command('taskkill.exe', ['/PID', String(cleanupAppPID), '/T', '/F']).catch(() => {})
    if (app?.pid && alive(app.pid)) await command('taskkill.exe', ['/PID', String(app.pid), '/T', '/F']).catch(() => {})
    if (installedRoot) await command(path.join(installedRoot, 'Uninstall Astral.exe'), ['/S']).catch(() => {})
  } else {
    // Every program belongs to this initially-empty disposable CI data root.
    if (cleanupAppPID && alive(cleanupAppPID)) process.kill(cleanupAppPID, 'SIGTERM')
    if (app?.pid && alive(app.pid)) app.kill('SIGTERM')
  }
  if (ready && alive(ready.pid)) {
    const alreadyFailed = retain
    try { await waitFor(() => !alive(ready.pid), 'The update fixture Server did not stop during cleanup.', 30_000) }
    catch (error) {
      retain = true
      if (!alreadyFailed) { console.error(error.message); process.exitCode = 1 }
    }
  }
  await new Promise(resolve => server.close(resolve))
  if (!retain) {
    await delay(1500)
    await rm(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
    await rm(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
    await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
  }
}
