// Linux CI only. Uses real dist/bin programs and systemd --user; the temporary
// curl wrapper supplies a local, synthetic ready release. This does not test
// publisher signatures or authorize the production candidate for publication.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { createReadStream, constants } from 'node:fs'
import { access, copyFile, link, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, stat, unlink, writeFile } from 'node:fs/promises'
import { homedir, tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { validateVersion } from './release-version.mjs'
import { webFixtureCookie } from './web-auth-fixture.mjs'
import { headlessUpdateTransport } from './headless-update-transport.mjs'

const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const distribution = path.join(repository, 'dist/bin')
const installer = path.join(repository, 'scripts/install-server.sh')
const service = 'astral-server.service'
const unitDirectory = path.join(process.env.XDG_CONFIG_HOME || path.join(homedir(), '.config'), 'systemd/user')
const unit = path.join(unitDirectory, service)
const enabledUnit = path.join(unitDirectory, 'default.target.wants', service)
const nativeName = `astral-linux-${process.arch === 'x64' ? 'amd64' : process.arch}`
const nativeBinary = path.join(distribution, nativeName)
const names = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))
const files = [...names, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'checksums.txt', 'install-server.sh']
let temporary
let installRoot
let environment
let payload
let metadata
let tag
let version
let sourceSHA
let fixtureTransport
let updateJob

async function command(executable, args, options = {}) {
  try {
    return await execute(executable, args, { cwd: repository, encoding: 'utf8', timeout: 20_000, maxBuffer: 2 << 20, ...options })
  } catch (error) {
    // Never serialize an exec error: it may retain complete stdout/stderr.
    throw new Error(`${path.basename(executable)} failed (${error.code ?? 'unknown'}).`, { cause: error })
  }
}
async function exists(file) {
  try { await lstat(file); return true }
  catch (error) { if (error.code === 'ENOENT') return false; throw error }
}
async function loadState() {
  try {
    return (await execute('systemctl', ['--user', 'show', service, '--property=LoadState', '--value'], { encoding: 'utf8', timeout: 10_000 })).stdout.trim()
  } catch (error) {
    // systemd versions differ in whether querying a missing unit is exit 0 or 4.
    if (error.stdout?.trim() === 'not-found') return 'not-found'
    throw new Error('Cannot establish whether astral-server.service is already loaded; refusing to modify it.', { cause: error })
  }
}
async function hash(file) {
  const digest = createHash('sha256')
  for await (const chunk of createReadStream(file)) digest.update(chunk)
  return digest.digest('hex')
}
async function sharedCopy(source, target) {
  try { await link(source, target) }
  catch (error) {
    if (!['EXDEV', 'EPERM', 'ENOTSUP', 'EMLINK'].includes(error.code)) throw error
    await copyFile(source, target, constants.COPYFILE_FICLONE)
  }
}
async function atomicWrite(file, text, options) {
  const pending = `${file}.pending`
  await writeFile(pending, text, options)
  await rename(pending, file)
}
async function publishFixture() {
  const binaries = await Promise.all(names.map(async name => ({ name, size: (await stat(path.join(payload, name))).size, sha256: await hash(path.join(payload, name)) })))
  await atomicWrite(path.join(payload, 'checksums.txt'), `${binaries.map(asset => `${asset.sha256}  ${asset.name}`).join('\n')}\n`)
  const assets = await Promise.all(files.map(async name => ({ name, size: (await stat(path.join(payload, name))).size, sha256: await hash(path.join(payload, name)) })))
  await atomicWrite(path.join(payload, 'release-manifest.json'), `${JSON.stringify({
    schema_version: 1,
    purpose: 'release',
    public_release_ready: true,
    version,
    tag,
    source_sha: sourceSHA,
    data_formats: metadata.data_formats,
    host_schema: metadata.data_formats.host,
    core_format: metadata.data_formats.core,
    migration_status: metadata.migration_status,
    migration_baselines: metadata.migration_baselines,
    migration_targets: metadata.migration_targets,
    distribution_names: names,
    assets,
  })}\n`)
  const checked = [...assets, { name: 'release-manifest.json', sha256: await hash(path.join(payload, 'release-manifest.json')) }]
  await atomicWrite(path.join(payload, 'SHA256SUMS'), `${checked.map(asset => `${asset.sha256}  ${asset.name}`).join('\n')}\n`)
  await atomicWrite(path.join(payload, 'release.json'), JSON.stringify({ tag_name: tag, draft: false, prerelease: false }))
}
async function prepareFixture() {
  payload = path.join(temporary, 'release')
  const wrappers = path.join(temporary, 'commands')
  await mkdir(payload)
  await mkdir(wrappers)
  for (const name of [...names, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt']) await sharedCopy(path.join(distribution, name), path.join(payload, name))
  await sharedCopy(installer, path.join(payload, 'install-server.sh'))
  await publishFixture()
  // This executable only exists on the installer's temporary PATH. Production
  // still accepts exactly the fixed GitHub repository and HTTPS asset URLs.
  await writeFile(path.join(wrappers, 'curl'), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
const url = args.find(arg => arg.startsWith('https://'));
const output = args.indexOf('-o');
const tag = JSON.parse(fs.readFileSync(path.join(process.env.ASTRAL_INSTALL_FIXTURE_DIRECTORY, 'release.json'), 'utf8')).tag_name;
let name;
if (url === 'https://api.github.com/repos/oines/astral/releases/latest' || url === 'https://api.github.com/repos/oines/astral/releases/tags/' + tag) name = 'release.json';
else {
  const prefix = 'https://github.com/oines/astral/releases/download/' + tag + '/';
  if (!url || !url.startsWith(prefix)) process.exit(64);
  name = url.slice(prefix.length);
  if (!/^[A-Za-z0-9_.-]+$/.test(name)) process.exit(64);
}
if (output < 0 || !args[output + 1]) process.exit(64);
const source = path.join(process.env.ASTRAL_INSTALL_FIXTURE_DIRECTORY, name);
// Downloads need independent inodes: the real installer chmods its native
// binary, which must never change dist/bin permissions or an older release.
fs.copyFileSync(source, args[output + 1], fs.constants.COPYFILE_FICLONE);
`, { mode: 0o700 })
  environment = { ...process.env, PATH: `${wrappers}${path.delimiter}${process.env.PATH || ''}`, ASTRAL_INSTALL_FIXTURE_TAG: tag, ASTRAL_INSTALL_FIXTURE_DIRECTORY: payload }
}
async function install(expectedSuccess = true) {
  let result
  try {
    result = await execute('bash', [installer, '--root', installRoot], { cwd: repository, env: environment, encoding: 'utf8', timeout: 130_000, maxBuffer: 2 << 20 })
  } catch (error) {
    if (expectedSuccess) {
      const diagnostic = (error.stderr || '').split('\n').filter(line => /^(Astral Server:|ExecMainCode=|ExecMainStatus=|Result=)/.test(line)).join('\n').slice(-4000)
      throw new Error(`Headless installer unexpectedly failed. ${diagnostic || 'No safe installer state diagnostic was available.'}`, { cause: error })
    }
    assert.equal(typeof error.code, 'number', 'Installer did not report an ordinary failure')
    // Return only an error classification; no captured output reaches CI logs.
    return { failed: true, checksumFailure: /checksum mismatch/.test(error.stderr || ''), startupFailure: /did not become ready|exited during startup/.test(error.stderr || '') }
  }
  assert.ok(expectedSuccess, 'Installer accepted a deliberately invalid release')
  assert.ok(result.stdout.includes(`Astral Server ${version} is running:`), 'Installer did not report successful startup')
  return { failed: false }
}
async function snapshot() {
  const deadline = Date.now() + 35_000
  while (Date.now() < deadline) {
    try {
      const ready = JSON.parse(await readFile(path.join(installRoot, 'run/ready.json'), 'utf8'))
      const pid = Number((await command('systemctl', ['--user', 'show', service, '--property=MainPID', '--value'])).stdout.trim())
      if (pid < 1 || ready.pid !== pid) { await delay(150); continue }
      const health = await fetch(`${ready.url}/health`, { signal: AbortSignal.timeout(3000) })
      if (!health.ok) { await delay(150); continue }
      const status = await health.json()
      if (status.status !== 'ok' || status.version !== version) { await delay(150); continue }
      const identity = await readFile(path.join(installRoot, 'data/server/computer-identity/identity.pem'))
      const cookie = await webFixtureCookie(path.join(installRoot,'data/server'),ready.url)
      const master = await readFile(path.join(installRoot, 'data/server/master.key'))
      const response = await fetch(`${ready.url}/api/v1/bootstrap`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(3000) })
      if (!response.ok) { await delay(150); continue }
      const bootstrap = await response.json()
      const local = bootstrap.computers.find(computer => computer.kind === 'local')
      if (!local?.id || !local.online) { await delay(150); continue }
      return { pid, identity, master, computerID: local.id, url: ready.url }
    } catch { /* Startup/reconnection may still be in progress; do not log secrets. */ }
    await delay(150)
  }
  throw new Error('Installed Server did not expose its actual healthy, authenticated, online Local Computer state.')
}
function sameIdentity(before, after) {
  assert.ok(before.identity.equals(after.identity), 'Server security identity changed')
  assert.ok(before.master.equals(after.master), 'Credential encryption key changed')
  assert.ok(before.computerID === after.computerID, 'Local Computer identity changed')
}
async function owner(ready, method, route, body) {
  const cookie = await webFixtureCookie(path.join(installRoot, 'data/server'), ready.url)
  const response = await fetch(`${ready.url}/api/v1/${route}`, {
    method, headers: { Cookie: cookie, Origin: ready.url, 'Content-Type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(15_000),
  })
  assert.ok(response.ok, `Headless update Owner API ${route} failed (HTTP ${response.status}).`)
  return response.json()
}
async function retainedUpdateSession(ready, previous) {
  if (previous) {
    const session = await owner(ready, 'GET', `sessions/${previous.id}`)
    return Object.fromEntries(Object.keys(previous).map(key => [key, session[key]]))
  }
  const provider = await owner(ready, 'POST', 'providers', { name: 'Headless update fixture', protocol: 'openai-completions', dialect: 'openai', base_url: 'http://127.0.0.1:9' })
  const model = await owner(ready, 'POST', 'models', { name: 'Headless update model', provider_id: provider.id, model_id: 'ci-update', recommendations: { context_window: 128000, max_output_tokens: 1024 }, overrides: {} })
  const computer = (await owner(ready, 'GET', 'bootstrap')).computers.find(item => item.local && item.online)
  assert.ok(computer, 'Headless update fixture needs an online Local Computer.')
  const bot = await owner(ready, 'POST', 'onboarding/complete', { model_id: model.id, computer_id: computer.id })
  const session = await owner(ready, 'POST', 'sessions', { agent_id: bot.id, title: 'Retained headless update Session' })
  assert.equal(session.status, 'idle')
  return Object.fromEntries(['id', 'title', 'agent_id', 'computer_id', 'profile_id', 'rollout_id'].map(key => [key, session[key]]))
}
async function managedWebUpdate() {
  const targetVersion = version
  const targetTag = tag
  const oldBinary = path.join(temporary, 'old-version-astral')
  // One cached native build changes only the version string; it is not a
  // fabricated historical schema or a cross-platform distribution build.
  await command('go', ['build', '-trimpath', '-ldflags', '-X github.com/oines/astral/internal/cli.Version=0.0.0', '-o', oldBinary, './cmd/astral'], { env: { ...process.env, GOWORK: 'off' }, timeout: 300_000 })
  await copyFile(oldBinary, path.join(payload, `${nativeName}.pending`))
  await rename(path.join(payload, `${nativeName}.pending`), path.join(payload, nativeName))
  version = '0.0.0'; tag = 'v0.0.0'; environment.ASTRAL_INSTALL_FIXTURE_TAG = tag
  await publishFixture()
  fixtureTransport = await headlessUpdateTransport({ temporary, payload, tag: () => tag, environment, command })
  await install()
  const before = await snapshot()
  const marker = path.join(installRoot, 'data/server/managed-update-marker')
  await writeFile(marker, 'Retain live Server data across detached updates.\n', { mode: 0o600 })
  const session = await retainedUpdateSession(before)
  version = targetVersion; tag = targetTag; environment.ASTRAL_INSTALL_FIXTURE_TAG = tag
  await copyFile(nativeBinary, path.join(payload, `${nativeName}.pending`))
  await rename(path.join(payload, `${nativeName}.pending`), path.join(payload, nativeName))
  await publishFixture()
  const requestsBeforeState = fixtureTransport.requests.length
  const initial = await owner(before, 'GET', 'updates')
  assert.equal(initial.manager, 'systemd-user', 'The actual installed service was not recognized as managed.')
  assert.equal(initial.current_version, '0.0.0')
  assert.equal(fixtureTransport.requests.length, requestsBeforeState, 'Reading update state contacted a release server.')
  const checked = await owner(before, 'POST', 'updates/check')
  assert.equal(checked.phase, 'available')
  assert.equal(checked.version, version)
  assert.equal(fixtureTransport.requests.filter(url => url.endsWith('/install-server.sh') || names.some(name => url.endsWith(`/${name}`))).length, 0, 'Checking metadata downloaded executable bytes.')
  assert.equal((await command('systemctl', ['--user', 'show', service, '--property=MainPID', '--value'])).stdout.trim(), String(before.pid), 'Checking metadata stopped the old Server.')
  const applied = await owner(before, 'POST', 'updates/apply')
  assert.match(applied.operation_id, /^[a-f0-9]{32}$/)
  assert.equal(applied.operation_state, 'running')
  updateJob = `astral-server-update-${applied.operation_id}.service`
  let completion
  const deadline = Date.now() + 150_000
  while (Date.now() < deadline) {
    try {
      const ready = JSON.parse(await readFile(path.join(installRoot, 'run/ready.json'), 'utf8'))
      const health = await fetch(`${ready.url}/health`, { signal: AbortSignal.timeout(3000) }).then(response => response.json())
      // The test cookie helper opens the live store. Never use it while an old
      // ready file remains during offline migration or service replacement.
      if (ready.pid === before.pid || health.version !== version || health.status !== 'ok') { await delay(150); continue }
      const status = await owner(ready, 'GET', 'updates')
      if (status.operation_id === applied.operation_id && status.operation_state === 'failed') throw new Error(`Independent headless update failed: ${status.error}`)
      if (status.operation_id === applied.operation_id && status.operation_state === 'complete') { completion = status; break }
    } catch (error) {
      if (error.message.startsWith('Independent headless update failed:')) throw error
      // A stopped HTTP listener is neither failure nor completion.
    }
    await delay(150)
  }
  assert.ok(completion, 'The actual detached update did not report its terminal result after Server restart.')
  assert.equal(completion.current_version, version)
  assert.equal(completion.operation_version, version)
  assert.equal(completion.phase, 'current')
  const after = await snapshot()
  assert.notEqual(after.pid, before.pid, 'The web update did not replace the actual Server process.')
  sameIdentity(before, after)
  assert.equal(await readFile(marker, 'utf8'), 'Retain live Server data across detached updates.\n')
  assert.deepEqual(await retainedUpdateSession(after, session), session, 'The detached update changed the persisted Session/model selection.')
  assert.equal((await readdir(path.join(installRoot, 'updates'))).filter(name => /^[a-f0-9]{32}$/.test(name)).length, 0, 'Terminal state appeared before update staging cleanup.')
  assert.ok(fixtureTransport.requests.some(url => url.endsWith('/install-server.sh')), 'The independent runner did not download the checksum-verified official installer.')
  console.log('Headless Owner API: metadata-only check, independent systemd update, new Server PID, durable completion, retained Session and identities passed.')
}

async function reportServiceState() {
  // Do not dump journals or bootstrap bodies, which may contain private data.
  try {
    const state = await command('systemctl', ['--user', 'show', service, '--property=ExecMainCode,ExecMainStatus,Result'])
    console.error(`Headless service state:\n${state.stdout.trim()}`)
  } catch { console.error('Headless service state could not be read.') }
}
async function cleanup() {
  if (!temporary) return
  if (updateJob) await command('systemctl', ['--user', 'stop', updateJob], { timeout: 210_000 }).catch(() => {})
  if (await exists(unit)) {
    const definition = await readFile(unit, 'utf8')
    if (!definition.split('\n').includes(`# Astral Server installer root: ${installRoot}`)) {
      throw new Error('Service ownership changed; refusing to stop or remove a unit belonging to somebody else. Test files were retained.')
    }
    try {
      await command('systemctl', ['--user', 'stop', service], { timeout: 45_000 })
      await command('systemctl', ['--user', 'disable', service])
      const current = await readFile(unit, 'utf8')
      assert.ok(current.split('\n').includes(`# Astral Server installer root: ${installRoot}`), 'Service ownership changed during cleanup')
      await unlink(unit)
      await command('systemctl', ['--user', 'daemon-reload'])
      await execute('systemctl', ['--user', 'reset-failed', service], { timeout: 10_000 }).catch(() => {})
    } catch {
      throw new Error('Test service cleanup failed. Its program/data directory was retained; inspect astral-server.service.')
    }
  } else {
    const loaded = await loadState()
    if (loaded !== 'not-found') {
      throw new Error('A service remains loaded without the test ownership marker; refusing destructive cleanup.')
    }
  }
  await rm(temporary, { recursive: true, force: true })
}

async function main() {
  if (process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('Headless update acceptance requires a disposable GitHub Actions user; never run it on a developer account.')
  if (process.platform !== 'linux') throw new Error('Headless service verification requires native Linux; do not run it on another platform.')
  assert.ok(['x64', 'arm64'].includes(process.arch), 'Unsupported Linux architecture')
  await command('systemctl', ['--user', 'show-environment'])
  if (await exists(unit) || await exists(enabledUnit)) throw new Error('An existing astral-server.service unit or enablement link is present; refusing to overwrite it.')
  const loaded = await loadState()
  assert.equal(loaded, 'not-found', 'An existing Astral Server unit is loaded; refusing to modify it')
  await access(nativeBinary, constants.X_OK)
  const originalNativeMode = (await stat(nativeBinary)).mode
  metadata = JSON.parse((await command(nativeBinary, ['version', '--json'])).stdout)
  version = validateVersion(metadata.version)
  const packageVersion = JSON.parse(await readFile(path.join(repository, 'package.json'), 'utf8')).version
  assert.equal(version, packageVersion, 'Actual native executable must match the package version')
  assert.ok(Number.isSafeInteger(metadata.data_formats?.host) && metadata.data_formats.host > 0 && Number.isSafeInteger(metadata.data_formats?.core) && metadata.data_formats.core > 0, 'Actual executable did not report valid data formats')
  tag = `v${version}`
  sourceSHA = (await command('git', ['rev-parse', 'HEAD'])).stdout.trim()
  assert.match(sourceSHA, /^[a-f0-9]{40}$/)
  temporary = await realpath(await mkdtemp(path.join(tmpdir(), 'astral-headless-ci-')))
  installRoot = path.join(temporary, 'Server 中文 space $%')
  try {
    await prepareFixture()
    await managedWebUpdate()
    await install()
    const first = await snapshot()
    console.log('Headless fresh installation and authenticated Local Computer readiness passed.')
    await install()
    const second = await snapshot()
    assert.notEqual(second.pid, first.pid, 'Repeat installation did not restart the actual service')
    sameIdentity(first, second)
    console.log('Headless same-format reinstallation preserved Web account, Server identity, encryption key and Local Computer identity.')

    const sumFile = path.join(payload, 'SHA256SUMS')
    const goodSums = await readFile(sumFile, 'utf8')
    await atomicWrite(sumFile, goodSums.split('\n').map(line => line.endsWith(`  ${nativeName}`) ? `${'0'.repeat(64)}  ${nativeName}` : line).join('\n'))
    const rejected = await install(false)
    assert.ok(rejected.checksumFailure, 'Corrupted release failed for an unexpected reason')
    const afterReject = await snapshot()
    assert.equal(afterReject.pid, second.pid, 'Checksum rejection stopped or restarted the old Server')
    sameIdentity(second, afterReject)
    console.log('Headless bad-checksum rejection kept the original Server process and identity.')

    // A deliberately broken new payload delegates only version queries to the
    // actual native program. Its serve path exits, exercising installer recovery.
    // Replace the fixture inode atomically: never modify hard-linked dist files.
    const quote = value => `'${value.replaceAll("'", "'\\''")}'`
    await atomicWrite(path.join(payload, nativeName), `#!/bin/sh\nif [ "$1" = version ]; then exec ${quote(nativeBinary)} "$@"; fi\nprintf 'Deliberate headless CI startup failure.\\n' >&2\nexit 77\n`, { mode: 0o700 })
    await publishFixture()
    const broken = await install(false)
    assert.ok(broken.startupFailure, 'Broken startup failed for an unexpected reason')
    const recovered = await snapshot()
    assert.notEqual(recovered.pid, second.pid, 'Recovery did not restart the previous Server')
    sameIdentity(second, recovered)

    // Leave the old service stopped while preparing a genuine interrupted
    // migration: starting it here would consume the journal before installation.
    // Dropping only the ledgers makes the real migration CLI adopt each current
    // baseline again, with verified full snapshots rather than invented backups.
    await command('systemctl', ['--user', 'stop', service], { timeout: 45_000 })
    const data = path.join(installRoot, 'data/server')
    const resultFile = path.join(data, 'last-upgrade.json')
    const markerFile = path.join(data, 'journal-recovery-marker.txt')
    const marker = 'Keep data written after the previous committed upgrade.\n'
    const adoptBaseline = async () => {
      await command('python3', ['-c', `import pathlib,sqlite3,sys
for name in ('astral.db','core.db'):
    with sqlite3.connect(pathlib.Path(sys.argv[1])/name) as db:
        db.execute('DROP TABLE goose_db_version')
`, data])
      await command(nativeBinary, ['migrate', '--data', data], { timeout: 120_000 })
      const record = JSON.parse(await readFile(resultFile, 'utf8'))
      assert.equal(record.schema, 1, 'Migration CLI did not record a committed snapshot')
      assert.ok(path.isAbsolute(record.backup), 'Migration CLI did not record an absolute snapshot path')
      return record
    }
    const previousUpgrade = await adoptBaseline()
    await writeFile(markerFile, marker, { mode: 0o600 })
    const interruptedUpgrade = await adoptBaseline()
    assert.notEqual(interruptedUpgrade.backup, previousUpgrade.backup, 'Migration fixture reused a snapshot')
    assert.equal(await exists(path.join(previousUpgrade.backup, 'files/journal-recovery-marker.txt')), false, 'Earlier snapshot unexpectedly contains the later data marker')
    assert.equal(await readFile(path.join(interruptedUpgrade.backup, 'files/journal-recovery-marker.txt'), 'utf8'), marker, 'Interrupted snapshot omitted the later data marker')
    const snapshotRecord = JSON.parse(await readFile(path.join(interruptedUpgrade.backup, 'files/last-upgrade.json'), 'utf8'))
    assert.equal(snapshotRecord.backup, previousUpgrade.backup, 'Interrupted snapshot does not contain the previous upgrade record')
    // Simulate a crash after Commit wrote last-upgrade.json, but before its
    // journal removal persisted. Recovering this snapshot restores the earlier
    // record while retaining the later data, which must become this attempt's
    // baseline rather than cause another restore to the older snapshot.
    await writeFile(path.join(data, 'migration-state.json'), `${JSON.stringify({ schema: 1, backup: interruptedUpgrade.backup })}\n`, { mode: 0o600 })
    await atomicWrite(path.join(payload, nativeName), `#!/bin/sh\ncase "$1" in\n  version|migrate) exec ${quote(nativeBinary)} "$@" ;;\n  serve) ${quote(nativeBinary)} migrate recover --data ${quote(data)} || exit $? ;;\nesac\nprintf 'Deliberate headless CI startup failure after journal recovery.\\n' >&2\nexit 77\n`, { mode: 0o700 })
    await publishFixture()
    const interruptedFailure = await install(false)
    assert.ok(interruptedFailure.startupFailure, 'Interrupted-migration startup failed for an unexpected reason')
    assert.equal(await exists(path.join(data, 'migration-state.json')), false, 'Installer did not finish the earlier journal recovery')
    assert.equal(await exists(markerFile), true, 'Failed installation restored an extra generation and lost later data')
    assert.equal(await readFile(markerFile, 'utf8'), marker, 'Failed installation changed data retained by journal recovery')
    const restoredRecord = JSON.parse(await readFile(resultFile, 'utf8'))
    assert.equal(restoredRecord.backup, previousUpgrade.backup, 'Failed installation restored beyond the recovered baseline')
    // The installer preserves a previously stopped service's state. Start the
    // restored old unit explicitly; the prior case covers automatic restart of
    // a service that was running when installation began.
    await command('systemctl', ['--user', 'start', service])
    const afterJournalRecovery = await snapshot()
    sameIdentity(recovered, afterJournalRecovery)
    assert.equal(await readFile(markerFile, 'utf8'), marker, 'Restored old Server lost the retained data marker')
    assert.equal((await stat(nativeBinary)).mode, originalNativeMode, 'Installer fixtures changed the original distribution executable permissions')
    console.log('Native headless user-service install, HTTP/account readiness, identity-preserving reinstall, checksum rejection before stop, failed-start recovery, and interrupted-journal recovery without an extra rollback passed. Synthetic local release fixtures do not establish publisher signing.')
  } catch (error) {
    await reportServiceState()
    throw error
  } finally {
    try { await cleanup() }
    finally { await fixtureTransport?.close() }
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1 })
