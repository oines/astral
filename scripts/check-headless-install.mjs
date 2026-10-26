// Linux CI only. Uses real dist/bin programs and systemd --user; the temporary
// curl wrapper supplies a local, synthetic ready release. This does not test
// publisher signatures or authorize the production candidate for publication.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { createReadStream, constants } from 'node:fs'
import { access, copyFile, link, lstat, mkdir, mkdtemp, readFile, realpath, rename, rm, stat, unlink, writeFile } from 'node:fs/promises'
import { homedir, tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { validateVersion } from './release-version.mjs'
import { webFixtureCookie } from './web-auth-fixture.mjs'

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
const files = [...names, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'checksums.txt']
let temporary
let installRoot
let environment
let payload
let metadata
let tag
let version
let sourceSHA

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
  await publishFixture()
  // This executable only exists on the installer's temporary PATH. Production
  // still accepts exactly the fixed GitHub repository and HTTPS asset URLs.
  await writeFile(path.join(wrappers, 'curl'), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
const url = args.find(arg => arg.startsWith('https://'));
const output = args.indexOf('-o');
const tag = process.env.ASTRAL_INSTALL_FIXTURE_TAG;
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
      return { pid, identity, master, computerID: local.id }
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
async function reportServiceState() {
  // Do not dump journals or bootstrap bodies, which may contain private data.
  try {
    const state = await command('systemctl', ['--user', 'show', service, '--property=ExecMainCode,ExecMainStatus,Result'])
    console.error(`Headless service state:\n${state.stdout.trim()}`)
  } catch { console.error('Headless service state could not be read.') }
}
async function cleanup() {
  if (!temporary) return
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
    assert.equal((await stat(nativeBinary)).mode, originalNativeMode, 'Installer fixtures changed the original distribution executable permissions')
    console.log('Native headless user-service install, HTTP/account readiness, identity-preserving reinstall, checksum rejection before stop, and failed-start program/service recovery passed. Synthetic local release fixtures do not establish publisher signing.')
  } catch (error) {
    await reportServiceState()
    throw error
  } finally {
    await cleanup()
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1 })
