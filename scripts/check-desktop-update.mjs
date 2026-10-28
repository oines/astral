// Disposable native CI users only. This installs and replaces a real application.
// Default: a copied fixture uses a loopback feed for candidate-byte acceptance.
// --published-from vX.Y.Z: an untouched official old App uses official Latest.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile, spawn } from 'node:child_process'
import { createReadStream } from 'node:fs'
import { access, chmod, copyFile, cp, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { homedir, tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { evaluateRenderer, managedRendererEndpoint, rendererAuthentication, rendererUpdateMethod, rendererUpdateProgress, startRendererUpdate } from './desktop-renderer-auth.mjs'
import { desktopArtifactName, desktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { readReleaseVersion } from './release-version.mjs'
import { publishedUpdateInputs } from './published-desktop-update.mjs'
import { webFixtureCookie } from './web-auth-fixture.mjs'
import { assertWindowsProcessOwner, sameWindowsProcess, stopWindowsProcessTree, windowsManagedProcessIdentity, windowsProcessIdentity } from './desktop-process-identity.mjs'

if (process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('Real Desktop update acceptance requires a disposable GitHub Actions user; never run on a developer account.')
if (!['darwin', 'win32', 'linux'].includes(process.platform) || !['arm64', 'x64'].includes(process.arch)) throw new Error('Unsupported native update platform.')
const args = process.argv.slice(2)
assert.ok(args.length === 0 || args.length === 2 && args[0] === '--published-from', 'Usage: check-desktop-update.mjs [--published-from vMAJOR.MINOR.PATCH]')
if (args.length) assert.match(args[1], /^v\d+\.\d+\.\d+$/, 'Pass a nonempty published version tag.')
const publishedFrom = args[1]
const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const desktop = path.join(repository, 'apps/desktop')
const version = await readReleaseVersion()
const architecture = process.arch
const platform = { darwin: 'mac', win32: 'win', linux: 'linux' }[process.platform]
const appData = process.platform === 'darwin' ? path.join(homedir(), 'Library/Application Support') : process.platform === 'win32' ? process.env.APPDATA : process.env.XDG_CONFIG_HOME || path.join(homedir(), '.config')
assert.ok(appData, 'Missing native application data location')
const data = path.join(appData, 'astral/schema-3-core-11')
const profile = path.join(appData, 'Astral')
for (const existing of [data, profile]) {
  try { await access(existing); throw new Error('Existing Astral data/profile is present; refusing to run the replacement test.') } catch (error) { if (error.code !== 'ENOENT') throw error }
}
// electron-builder's safe-path policy explicitly permits the native temp root.
const temporary = await realpath(await mkdtemp(path.join(tmpdir(), 'astral-update-acceptance-')))
const fixtureApp = path.join(temporary, 'fixture-app')
const output = path.join(temporary, 'fixture-packages')
const installed = path.join(temporary, 'installed')
const targetName = desktopArtifactName(version, platform, architecture, process.platform === 'darwin' ? 'zip' : process.platform === 'win32' ? 'exe' : 'AppImage')
const target = path.join(desktop, 'release', targetName)
const targetBytes = publishedFrom ? undefined : await readFile(target)
const feedName = desktopUpdateFeeds(version).find(feed => feed.asset === targetName)?.name
assert.ok(feedName, 'No native update metadata contract')
const checksum = targetBytes && createHash('sha512').update(targetBytes).digest('base64')
const logs = []
let app
let oldServer
let newServer
let updatedAppPID
let oldWindowsApp
let oldWindowsServer
let newWindowsApp
let newWindowsServer
let executable
let installedRoot
let published
let fromVersion = '0.0.0'
let expectedAsar
let retain = false
let rendererDiagnostic = 'The managed renderer has not been discovered.'
let feedRequests = 0
let packageRequests = 0
let completedPackages = 0
const command = (program, args, options = {}) => execute(program, args, { cwd: repository, encoding: 'utf8', timeout: 600_000, maxBuffer: 2 << 20, ...options })
const alive = pid => { try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error } }
const server = publishedFrom ? undefined : createServer((request, response) => {
  const name = new URL(request.url, 'http://127.0.0.1').pathname.slice(1)
  if (name === feedName) {
    feedRequests++
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ version, files: [{ url: `http://127.0.0.1:${server.address().port}/${targetName}`, sha512: checksum, size: targetBytes.length }] }))
  } else if (name === targetName) {
    packageRequests++
    response.on('finish', () => { completedPackages++ })
    response.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Length': targetBytes.length })
    createReadStream(target).pipe(response)
  } else { response.writeHead(404); response.end() }
})
if (server) await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const fixtureFeed = server && `http://127.0.0.1:${server.address().port}/`

async function waitFor(read, message, timeout = 120_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const value = await read()
    if (value) return value
    await delay(300)
  }
  throw new Error(typeof message === 'function' ? message() : message)
}
async function running() {
  const ready = await readFile(path.join(data, 'server/running.json'), 'utf8').then(JSON.parse).catch(() => undefined)
  if (!ready || !alive(ready.pid)) return undefined
  const health = await fetch(`${ready.url}/health`, { signal: AbortSignal.timeout(3000) }).then(response => response.json()).catch(() => undefined)
  return health?.status === 'ok' ? { ...ready, healthVersion: health.version } : undefined
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
function windowsIdentityDiagnostic(label, expected, observed) {
  const message = `Windows update process identity ${label}: ${JSON.stringify({ expected, observed })}`
  logs.push(`${message}\n`)
  console.log(message)
}
function windowsServerExecutable() {
  return path.join(installedRoot, 'resources/bin', `astral-windows-${architecture === 'x64' ? 'amd64' : 'arm64'}.exe`)
}
async function windowsManagedProcesses(ready, options) {
  return windowsManagedProcessIdentity(ready.pid, windowsServerExecutable(), executable, options)
}
async function windowsProcessExited(expected) {
  const observed = await windowsProcessIdentity(expected.pid)
  if (sameWindowsProcess(expected, observed)) return false
  windowsIdentityDiagnostic(observed ? 'PID reused; original process exited' : 'original process exited', expected, observed)
  return true
}
async function cleanupWindowsProcesses(ready) {
  let current
  if (ready) current = await windowsManagedProcesses(ready)
  const identities = [current?.managedApp, newWindowsApp, oldWindowsApp, current?.managedServer, newWindowsServer, oldWindowsServer].filter(Boolean)
  const unique = new Map(identities.map(value => [`${value.pid}:${value.birth}`, value]))
  if (app && unique.size === 0) throw new Error('No verified Windows identity is available for cleanup; retaining the fixture')
  for (const expected of unique.values()) {
    const result = await stopWindowsProcessTree(expected)
    windowsIdentityDiagnostic(result.stopped ? 'cleanup held process tree' : 'cleanup skipped absent/recycled identity', expected, result.current)
  }
  if (current) await waitFor(() => windowsProcessExited(current.managedServer), 'The recorded Windows update fixture Server did not stop during cleanup.', 30_000)
  if (installedRoot) await command(path.join(installedRoot, 'Uninstall Astral.exe'), ['/S']).catch(() => {})
}
async function expandZIP(archive, directory) {
  await mkdir(directory, { recursive: true })
  if (process.platform === 'darwin') await command('/usr/bin/ditto', ['-x', '-k', archive, directory])
  else {
    const quote = value => `'${value.replace(/'/g, "''")}'`
    const script = `$ErrorActionPreference = 'Stop'; Expand-Archive -LiteralPath ${quote(archive)} -DestinationPath ${quote(directory)}`
    await command('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64')])
  }
}
async function verifyMacSignature(bundle) {
  await command('/usr/bin/codesign', ['--verify', '--deep', '--strict', bundle])
  const signature = await command('/usr/bin/codesign', ['--display', '--verbose=4', bundle])
  assert.ok(signature.stderr.includes('TeamIdentifier=6N6W6Z6739') && signature.stderr.includes('Authority=Developer ID Application: '), 'The Mac App does not retain the official Developer ID signature.')
}
async function retainedSession(ready, previous) {
  const cookie = await webFixtureCookie(path.join(data, 'server'), ready.url)
  const json = async (route, body) => {
    const response = await fetch(`${ready.url}/api/v1/${route}`, {
      ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }),
      headers: { Cookie: cookie, Origin: ready.url, 'Content-Type': 'application/json' }, signal: AbortSignal.timeout(10_000),
    })
    assert.ok(response.ok, `Published update fixture API failed at ${route} (HTTP ${response.status}).`)
    return response.json()
  }
  let session
  if (previous) session = await json(`sessions/${encodeURIComponent(previous.id)}`)
  else {
    const provider = await json('providers', { name: 'CI update provider', protocol: 'openai-completions', dialect: 'openai', base_url: 'http://127.0.0.1:9' })
    const model = await json('models', { name: 'CI update model', provider_id: provider.id, model_id: 'ci-update', recommendations: { context_window: 128000, max_output_tokens: 1024 }, overrides: {} })
    const computer = (await json('bootstrap')).computers.find(item => item.local && item.online && !item.revoked)
    assert.ok(computer, 'Published update fixture needs an online Local Computer.')
    const bot = await json('onboarding/complete', { model_id: model.id, computer_id: computer.id })
    // Initialize persistent Host/Core records without submitting a message,
    // starting a Run, discovering models or contacting a model service.
    session = await json('sessions', { agent_id: bot.id, title: `Retained public ${fromVersion} session` })
  }
  assert.equal(session.status, 'idle')
  assert.ok(!session.run_id && !session.last_run && !session.attempt && session.id && session.rollout_id, 'The retained fixture is not an idle persisted Session.')
  const model = (await json('models')).find(item => item.id === session.profile_id)
  const provider = model && (await json('providers')).find(item => item.id === model.provider_id)
  assert.equal(model?.model_id, 'ci-update', 'The persisted model profile was lost.')
  assert.equal(provider?.base_url, 'http://127.0.0.1:9', 'The persisted provider connection was lost.')
  return Object.fromEntries(['id', 'title', 'agent_id', 'computer_id', 'profile_id', 'rollout_id'].map(key => [key, session[key]]))
}

try {
  let oldPackage
  let oldAppDirectory
  if (publishedFrom) {
    const sourceSHA = (await command('git', ['rev-parse', 'HEAD'])).stdout.trim()
    published = await publishedUpdateInputs({ fromTag: publishedFrom, version, sourceSHA, platform, architecture, directory: path.join(temporary, 'published-downloads') })
    fromVersion = published.fromVersion
    oldPackage = published.oldPackage
    if (process.platform === 'darwin') {
      await expandZIP(oldPackage, output)
      oldAppDirectory = path.join(output, 'Astral.app')
    }
    if (published.expectedPackage) {
      const expected = path.join(temporary, 'expected-published-target')
      await expandZIP(published.expectedPackage, expected)
      expectedAsar = path.join(expected, process.platform === 'darwin' ? 'Astral.app/Contents/Resources/app.asar' : 'resources/app.asar')
    }
  } else {
    await mkdir(fixtureApp)
    await cp(path.join(desktop, 'out'), path.join(fixtureApp, 'out'), { recursive: true })
    const packageJSON = JSON.parse(await readFile(path.join(desktop, 'package.json'), 'utf8'))
    packageJSON.version = fromVersion
    await writeFile(path.join(fixtureApp, 'package.json'), JSON.stringify(packageJSON))
    await symlink(path.join(desktop, 'node_modules'), path.join(fixtureApp, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir')
    const main = path.join(fixtureApp, 'out/main/index.js')
    const source = await readFile(main, 'utf8')
    const productionFeed = 'https://github.com/oines/astral/releases/latest/download/'
    assert.equal(source.split(productionFeed).length, 2, 'Fixture must replace exactly one compiled update source')
    await writeFile(main, source.replace(productionFeed, fixtureFeed))
    const builder = ['--config.directories.app=' + fixtureApp, '--config.directories.output=' + output, '--publish', 'never', `--${architecture}`]
    if (process.platform === 'darwin') builder.push('--config', 'electron-builder.release.cjs', '--mac', '--dir')
    else if (process.platform === 'win32') builder.push('--win', 'nsis')
    else builder.push('--linux', 'AppImage')
    // Invoke the installed CLI through Node directly: Windows command shims can
    // reinterpret quoted paths and resolve pnpm relative to the wrong directory.
    const require = createRequire(new URL('../apps/desktop/package.json', import.meta.url))
    await command(process.execPath, [require.resolve('electron-builder/cli.js'), ...builder], { cwd: desktop })
    oldAppDirectory = path.join(output, architecture === 'arm64' ? 'mac-arm64/Astral.app' : 'mac/Astral.app')
    if (process.platform !== 'darwin') oldPackage = path.join(output, desktopArtifactName(fromVersion, platform, architecture, process.platform === 'win32' ? 'exe' : 'AppImage'))
  }
  if (process.platform === 'darwin') {
    installedRoot = path.join(installed, 'Astral.app')
    await mkdir(installed)
    await command('/usr/bin/ditto', [oldAppDirectory, installedRoot])
    await verifyMacSignature(installedRoot)
    executable = path.join(installedRoot, 'Contents/MacOS/Astral')
  } else if (process.platform === 'win32') {
    installedRoot = path.join(installed, 'Astral')
    await command(oldPackage, ['/S', `/D=${installedRoot}`])
    executable = path.join(installedRoot, 'Astral.exe')
  } else {
    await mkdir(installed)
    executable = path.join(installed, 'Astral.AppImage')
    await copyFile(oldPackage, executable)
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
  if (process.platform === 'win32') oldWindowsApp = assertWindowsProcessOwner(await windowsProcessIdentity(app.pid), executable, process.pid)
  oldServer = await waitFor(running, 'The old Desktop fixture did not start its real Server.')
  if (process.platform === 'win32') {
    oldWindowsServer = assertWindowsProcessOwner(await windowsProcessIdentity(oldServer.pid), windowsServerExecutable(), oldWindowsApp.pid)
    windowsIdentityDiagnostic('before update', oldWindowsApp, oldWindowsServer)
  }
  if (published) assert.equal(oldServer.healthVersion, fromVersion, 'The old official package did not run its matching Server version.')
  const before = await waitFor(() => fingerprint(oldServer), 'The fixture Local Computer did not connect.')
  const marker = path.join(data, 'server/update-acceptance-marker')
  await writeFile(marker, `retained-data-${fromVersion}`)
  const endpoint = await waitFor(
    async () => {
      const options = { diagnose: value => { rendererDiagnostic = value }, allowLegacyUpdates: Boolean(publishedFrom) }
      if (!await rendererAuthentication(profile, oldServer.url, options)) return false
      return managedRendererEndpoint(profile, oldServer.url, options)
    },
    () => `The old Desktop fixture did not load its native update bridge.\nLast renderer state: ${rendererDiagnostic}`,
  )
  const oldState = await evaluateRenderer(endpoint, 'window.astralDesktop.updates.state()')
  assert.equal(oldState.currentVersion, fromVersion)
  assert.notEqual(oldState.phase, 'unavailable', `The fixture does not support native updating: ${oldState.unavailable}`)
  const session = published ? await retainedSession(oldServer) : undefined
  const updateMethod = await rendererUpdateMethod(endpoint, { allowLegacy: Boolean(publishedFrom) })
  const checkOnly = updateMethod === 'update'
  const expectTarget = state => {
    rendererDiagnostic = JSON.stringify(state)
    if (state.phase === 'error') throw new Error(`Real updater failed: ${state.error}`)
    if (state.version) assert.equal(state.version, version, 'Latest changed during the update; refusing a different release.')
    return state
  }
  const checked = expectTarget(await evaluateRenderer(endpoint, 'window.astralDesktop.updates.check()'))
  if (checkOnly) {
    assert.equal(checked.phase, 'available', 'Checking metadata must finish with an available update, without downloading it.')
    assert.equal(checked.version, version)
    if (server) {
      assert.ok(feedRequests > 0, 'The real updater did not request candidate metadata.')
      assert.equal(packageRequests, 0, 'The candidate downloaded its installer before the explicit update action.')
    }
    const previousChecks = feedRequests
    const rechecked = expectTarget(await evaluateRenderer(endpoint, 'window.astralDesktop.updates.check()'))
    assert.equal(rechecked.phase, 'available', 'An available update must permit another metadata check.')
    if (server) {
      assert.ok(feedRequests > previousChecks, 'An available update did not request fresh metadata when checked again.')
      assert.equal(packageRequests, 0, 'Repeated checks downloaded the installer before the explicit update action.')
    }
    assert.ok(alive(oldServer.pid) && (await running())?.pid === oldServer.pid, 'A metadata-only check stopped or replaced the managed Server.')
  } else {
    await waitFor(async () => expectTarget(await evaluateRenderer(endpoint, 'window.astralDesktop.updates.state()')).phase === 'ready',
      'The untouched old updater did not finish automatically downloading the final release.', published ? 10 * 60_000 : 120_000)
  }
  const checksBeforeUpdate = feedRequests
  await startRendererUpdate(endpoint, updateMethod, { diagnose: value => { rendererDiagnostic = value } })
  newServer = await waitFor(async () => {
    const ready = await running()
    if (ready && process.platform === 'win32' && ready.healthVersion === version) {
      const candidate = await windowsManagedProcesses(ready, { allowMissing: true })
      if (candidate && !sameWindowsProcess(oldWindowsServer, candidate.managedServer)) {
        const { managedServer, managedApp } = candidate
        assert.equal(sameWindowsProcess(oldWindowsApp, managedApp), false, 'The original Desktop identity survived the replacement')
        newWindowsServer = managedServer
        newWindowsApp = managedApp
        windowsIdentityDiagnostic('replacement', newWindowsApp, newWindowsServer)
        return ready
      }
    } else if (ready && process.platform !== 'win32' && ready.pid !== oldServer.pid) return ready
    const progress = await rendererUpdateProgress(endpoint, { afterUpdateDispatch: true, allowLegacyUpdates: updateMethod === 'install', diagnose: value => { rendererDiagnostic = value } })
    if (progress) expectTarget(progress)
    return undefined
  }, 'The updated Desktop did not download, install and relaunch its Server.', (checkOnly ? (published ? 10 * 60_000 : 120_000) : 0) + 180_000)
  if (server && checkOnly) {
    assert.ok(feedRequests > checksBeforeUpdate, 'The explicit update did not recheck Latest before downloading.')
    assert.ok(packageRequests > 0 && completedPackages > 0, 'The explicit update did not finish serving the final installer bytes.')
  }
  assert.equal(newServer.healthVersion, version, 'The replacement did not launch the target Server version.')
  updatedAppPID = process.platform === 'win32' ? newWindowsApp.pid : await parentPID(newServer.pid)
  if (process.platform !== 'win32') assert.notEqual(updatedAppPID, app.pid, 'The original Desktop process survived the replacement')
  await waitFor(() => app.exitCode !== null || app.signalCode !== null, 'The original Desktop process did not exit after installation.')
  if (process.platform === 'win32') await waitFor(() => windowsProcessExited(oldWindowsServer), () => `The original Windows Server identity survived automatic replacement: ${JSON.stringify(oldWindowsServer)}`, 30_000)
  else assert.equal(alive(oldServer.pid), false, 'The old Server survived automatic replacement')
  const after = await waitFor(() => fingerprint(newServer), 'The updated Local Computer did not reconnect.')
  assert.equal(before.local, after.local)
  assert.ok(before.master.equals(after.master), 'The update changed the encryption key')
  assert.ok(before.identity.equals(after.identity), 'The update changed the pairing identity')
  assert.equal(await readFile(marker, 'utf8'), `retained-data-${fromVersion}`)
  if (session) assert.deepEqual(await retainedSession(newServer, session), session, 'The upgrade changed or lost the persisted Session/model selection.')
  const hash = file => readFile(file).then(bytes => createHash('sha256').update(bytes).digest('hex'))
  let installedSHA256
  if (process.platform === 'linux') {
    installedSHA256 = await hash(executable)
    assert.equal(installedSHA256, published ? published.target.assets.get(targetName).sha256 : await hash(target), 'Installed AppImage bytes differ from the final release')
  } else {
    const directory = process.platform === 'darwin' ? (architecture === 'arm64' ? 'mac-arm64/Astral.app/Contents/Resources' : 'mac/Astral.app/Contents/Resources') : (architecture === 'arm64' ? 'win-arm64-unpacked/resources' : 'win-unpacked/resources')
    const installedAsar = path.join(installedRoot, process.platform === 'darwin' ? 'Contents/Resources/app.asar' : 'resources/app.asar')
    installedSHA256 = await hash(installedAsar)
    assert.equal(installedSHA256, await hash(expectedAsar || path.join(desktop, 'release', directory, 'app.asar')), 'Installed app.asar differs from the final release')
  }
  if (published) {
    let serverSHA256
    if (process.platform !== 'linux') {
      const name = `astral-${process.platform === 'darwin' ? 'darwin' : 'windows'}-${architecture === 'x64' ? 'amd64' : architecture}${process.platform === 'win32' ? '.exe' : ''}`
      const resources = path.join(installedRoot, process.platform === 'darwin' ? 'Contents/Resources' : 'resources')
      serverSHA256 = await hash(path.join(resources, 'bin', name))
      assert.equal(serverSHA256, published.target.assets.get(name)?.sha256, 'Installed Server bytes differ from the published program.')
    }
    if (process.platform === 'darwin') await verifyMacSignature(installedRoot)
    const report = {
      platform: process.platform, architecture, from_version: fromVersion, version,
      old_source_sha: published.previous.manifest.source_sha, source_sha: published.target.manifest.source_sha,
      old_package: path.basename(published.oldPackage), old_package_sha256: published.previous.assets.get(path.basename(published.oldPackage)).sha256,
      target_package: targetName, target_package_sha256: published.target.assets.get(targetName).sha256,
      installed_content: process.platform === 'linux' ? 'AppImage' : 'app.asar', installed_sha256: installedSHA256,
      server_binary_sha256: serverSHA256,
      old_app_exited: true, old_server_exited: true, local_computer_id: after.local, session_id: session.id,
      encryption_key_retained: true, pairing_identity_retained: true, session_retained: true, data_marker_retained: true,
      update_source: 'official-latest',
    }
    await writeFile(path.join(process.env.RUNNER_TEMP, 'astral-published-desktop-update.json'), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 })
  }
  console.log(`Actual ${process.platform}/${architecture} Desktop ${fromVersion} → ${version}: final release download, old Server exit, installer replacement, relaunch and retained data passed. ${published ? 'The untouched previously published App used its fixed official Latest feed; persisted Session and model selection were retained.' : 'This tests the updater mechanism with the same Go payload; versioned schema conversion is verified separately.'}`)
} catch (error) {
  retain = true
  console.error(`Update acceptance failed; fixture retained at ${temporary}.`)
  // Logs can include private loopback addresses; keep them on the disposable runner.
  await writeFile(path.join(temporary, 'app-output.log'), logs.join('').slice(-20_000))
  await writeFile(path.join(process.env.RUNNER_TEMP, 'astral-update-acceptance.log'), `Fixture: ${temporary}\nLast renderer state: ${rendererDiagnostic}\n${logs.join('').slice(-20_000)}`)
  throw error
} finally {
  const ready = await running().catch(() => undefined)
  if (process.platform === 'win32') {
    try {
      await cleanupWindowsProcesses(ready)
    } catch (error) {
      retain = true
      console.error(`Windows identity-preserving cleanup failed: ${error.message}`)
      process.exitCode = 1
    }
  } else {
    const cleanupAppPID = updatedAppPID || (ready ? await parentPID(ready.pid).catch(() => undefined) : undefined)
    // Every program belongs to this initially-empty disposable CI data root.
    if (cleanupAppPID && alive(cleanupAppPID)) process.kill(cleanupAppPID, 'SIGTERM')
    if (app?.pid && alive(app.pid)) app.kill('SIGTERM')
  }
  if (process.platform !== 'win32' && ready && alive(ready.pid)) {
    const alreadyFailed = retain
    try { await waitFor(() => !alive(ready.pid), 'The update fixture Server did not stop during cleanup.', 30_000) }
    catch (error) {
      retain = true
      if (!alreadyFailed) { console.error(error.message); process.exitCode = 1 }
    }
  }
  if (server) await new Promise(resolve => server.close(resolve))
  if (!retain) {
    await delay(1500)
    await rm(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
    await rm(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
    await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 })
  }
}
