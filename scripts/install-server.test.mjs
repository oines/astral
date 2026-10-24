import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
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

function manifest(version) {
  const names = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))
  return {
    schema_version: 1, purpose: 'release', public_release_ready: true,
    version, tag: `v${version}`, source_sha: 'a'.repeat(40),
    host_schema: 3, core_format: 10, data_formats: { host: 3, core: 10 },
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
