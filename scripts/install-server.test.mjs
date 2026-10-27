import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execute = promisify(execFile)
const installer = fileURLToPath(new URL('./install-server.sh', import.meta.url))
const source = await readFile(installer, 'utf8')
const supported = { skip: process.platform === 'win32' }
const accepted = ['0.0.0', '0.1.0', '1.0.0', '1.13.0', '0.1.1024', '9007199254740991.0.0', '0.9007199254740991.0', '0.0.9007199254740991', '9007199254740991.9007199254740991.9007199254740991']
const rejected = ['', '1.2', '1.2.3.4', '01.2.3', '1.02.3', '1.2.03', '1.2.3-beta.1', '1.2.3+build', '-1.2.3', '1.-2.3', '1.2.-3', 'v1.2.3', '1.2.3\n', '1.2.3 ', '١.2.3', '9007199254740992.0.0', '0.9007199254740992.0', '0.0.9007199254740992', '18446744073709551616.0.0']

async function fixture(t) {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-installer-version-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  return directory
}

async function run(command, args) {
  try { return { code: 0, ...await execute(command, args, { encoding: 'utf8', timeout: 5000 }) } }
  catch (error) {
    if (typeof error.code !== 'number') throw error
    return { code: error.code, stdout: error.stdout, stderr: error.stderr }
  }
}

// Run the installer's actual embedded validators against tiny JSON files,
// without its network, executable startup or systemd installation paths.
function pythonBlock(marker) {
  const markerAt = source.indexOf(marker)
  assert.ok(markerAt >= 0, `Missing installer validator: ${marker}`)
  const start = source.indexOf("<<'PY'\n", markerAt)
  const end = source.indexOf('\nPY\n', start)
  assert.ok(start >= 0 && end > start, `Missing Python body: ${marker}`)
  return source.slice(start + "<<'PY'\n".length, end)
}
const releaseValidator = pythonBlock('tag=$(python3 - ')
const manifestValidator = pythonBlock('metadata=$(python3 - ')
const forwardFormatValidator = pythonBlock('python3 - "$old_formats" "$new_formats"')

function manifest(version) {
  const names = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))
  return {
    schema_version: 1, purpose: 'release', public_release_ready: true,
    version, tag: `v${version}`, source_sha: 'a'.repeat(40),
    host_schema: 3, core_format: 11, data_formats: { host: 3, core: 11 },
    migration_status: 'goose', migration_baselines: { host: 3, core: 11 }, migration_targets: { host: 3, core: 11 },
    distribution_names: names,
    assets: [...names, 'checksums.txt', 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt'].map(name => ({ name, size: 1, sha256: 'b'.repeat(64) })),
  }
}

test('installer dry-run accepts ordinary numeric tags and rejects suffixes, leading zeroes and overflow', supported, async t => {
  const directory = await fixture(t)
  for (const version of accepted) {
    const result = await run('bash', [installer, '--tag', `v${version}`, '--root', path.join(directory, 'server'), '--dry-run'])
    assert.equal(result.code, 0, `${version}: ${result.stderr}`)
    assert.ok(result.stdout.includes(`release v${version}`))
  }
  for (const version of rejected) {
    const result = await run('bash', [installer, '--tag', `v${version}`, '--root', path.join(directory, 'server'), '--dry-run'])
    assert.notEqual(result.code, 0, `Accepted invalid tag v${version}`)
    assert.match(result.stderr, /vMAJOR\.MINOR\.PATCH/)
  }
  const latest = await run('bash', [installer, '--tag', 'latest', '--root', path.join(directory, 'server'), '--dry-run'])
  assert.equal(latest.code, 0, latest.stderr)
})

test('release API and manifest validators use the same version bounds as the command line', supported, async t => {
  const directory = await fixture(t)
  const releaseFile = path.join(directory, 'release.json')
  const manifestFile = path.join(directory, 'manifest.json')
  for (const version of [...accepted, ...rejected]) {
    await writeFile(releaseFile, JSON.stringify({ tag_name: `v${version}`, draft: false, prerelease: false }))
    await writeFile(manifestFile, JSON.stringify(manifest(version)))
    for (const [validator, file] of [[releaseValidator, releaseFile], [manifestValidator, manifestFile]]) {
      const result = await run('python3', ['-c', validator, file, `v${version}`])
      if (accepted.includes(version)) assert.equal(result.code, 0, `${version}: ${result.stderr}`)
      else assert.notEqual(result.code, 0, `Accepted invalid version ${version}`)
    }
  }
})

test('ordinary release versions retain published, stable-latest and public-ready gates', supported, async t => {
  const directory = await fixture(t)
  const file = path.join(directory, 'release.json')
  for (const release of [
    { tag_name: 'v0.1.0', draft: true, prerelease: false },
    { tag_name: 'v0.1.0', draft: false, prerelease: true },
  ]) {
    await writeFile(file, JSON.stringify(release))
    assert.notEqual((await run('python3', ['-c', releaseValidator, file, 'latest'])).code, 0)
  }
  for (const changes of [{ public_release_ready: false }, { purpose: 'unsigned-candidate' }]) {
    await writeFile(file, JSON.stringify({ ...manifest('0.1.0'), ...changes }))
    const result = await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /public_release_ready/)
  }
})

test('installer accepts the current Core baseline and rejects the previous baseline', supported, async t => {
  const directory = await fixture(t)
  const file = path.join(directory, 'manifest.json')
  const current = manifest('0.1.0')
  await writeFile(file, JSON.stringify(current))
  assert.equal((await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])).code, 0)
  for (const value of [
    { ...current, migration_baselines: { host: 3, core: 10 } },
    { ...current, core_format: 10, data_formats: { host: 3, core: 10 }, migration_targets: { host: 3, core: 10 } },
  ]) {
    await writeFile(file, JSON.stringify(value))
    const result = await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /Invalid Goose migration ranges/)
  }
})

test('forward-format preflight rejects private Core formats and downgrades before updating a service', supported, async () => {
  for (const [previous, next] of [['3 11', '3 11'], ['3 11', '3 12'], ['3 12', '4 14']]) {
    const result = await run('python3', ['-c', forwardFormatValidator, previous, next])
    assert.equal(result.code, 0, `${previous} → ${next}: ${result.stderr}`)
  }
  for (const [previous, next] of [['3 10', '3 11'], ['2 11', '3 11'], ['3 12', '3 11'], ['4 11', '3 11'], ['3', '3 11'], ['3 11 1', '3 11']]) {
    const result = await run('python3', ['-c', forwardFormatValidator, previous, next])
    assert.notEqual(result.code, 0, `Accepted unsupported forward formats ${previous} → ${next}`)
    assert.match(result.stderr, /outside the supported forward migration chain/)
  }
})

test('HTTP deployment keeps remote management off and accepts intentional opt-in', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  const args = [installer, '--root', root, '--dry-run']
  const defaults = await run('bash', args)
  assert.equal(defaults.code, 0, defaults.stderr)
  assert.match(defaults.stdout, /Listen: 0\.0\.0\.0:4545\nRemote management: false/)
  const remote = await run('bash', [...args, '--listen', '0.0.0.0:4545', '--public-url', 'http://100.80.1.2:4545', '--allow-remote-management'])
  assert.equal(remote.code, 0, remote.stderr)
  assert.match(remote.stdout, /Remote management: true/)
  for (const extra of [['--tls-cert', '/tmp/cert'], ['--allow-remote-management=maybe'], ['--public-url', 'http://user:password@host:4545']]) {
    assert.notEqual((await run('bash', [...args, ...extra])).code, 0)
  }
})

test('reinstallation preserves saved network choices, including false, unless explicitly replaced', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  await mkdir(path.join(root, 'data/server'), { recursive: true })
  const settings = path.join(root, 'data/server/network.json')
  const original = JSON.stringify({ listen: '127.0.0.1:8888', public_urls: ['http://host:8888'], allow_remote_management: false })
  await writeFile(settings, original)
  const args = [installer, '--root', root, '--dry-run']
  const saved = await run('bash', args)
  assert.equal(saved.code, 0, saved.stderr)
  assert.match(saved.stdout, /Listen: 127\.0\.0\.1:8888\nRemote management: false/)
  const explicit = await run('bash', [...args, '--listen', '0.0.0.0:9999', '--allow-remote-management=true'])
  assert.equal(explicit.code, 0, explicit.stderr)
  assert.match(explicit.stdout, /Listen: 0\.0\.0\.0:9999\nRemote management: true/)
  assert.equal(await readFile(settings, 'utf8'), original, 'dry-run changed saved preferences')
})

test('download-only can obtain the new maintenance program before migrating an older installation', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'legacy-server')
  await mkdir(root)
  const old = JSON.stringify({ listen: '127.0.0.1:4545', tls_cert: '/old/cert', tls_key: '/old/key' })
  await writeFile(path.join(root, 'install-config.json'), old)
  const args = [installer, '--root', root, '--dry-run']
  assert.notEqual((await run('bash', args)).code, 0, 'ordinary update skipped required offline migration')
  const download = await run('bash', [...args, '--download-only', path.join(directory, 'distribution')])
  assert.equal(download.code, 0, download.stderr)
  assert.match(download.stdout, /Download and verify only/)
  assert.equal(await readFile(path.join(root, 'install-config.json'), 'utf8'), old)
})
