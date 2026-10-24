import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { desktopNames, digest, distributionNames } from './release-assets.mjs'
import { isDirectInvocation, repositoryRoot, validateTag, validateVersion } from './release-version.mjs'

// Kilobyte fixtures only: no dependencies, network, build, real Astral execution,
// credentials, tags or releases. This does not establish native install/update acceptance.
export async function checkReleaseTools() {
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-release-fixture-'))
  const realManifests = await Promise.all(['package.json', 'apps/desktop/package.json'].map(name => readFile(path.join(repositoryRoot, name), 'utf8')))
  try {
    for (const name of ['scripts', 'apps/desktop', 'input']) await mkdir(path.join(temporary, name), { recursive: true })
    for (const name of ['release-version.mjs', 'release-assets.mjs', 'release-draft.mjs']) await copyFile(path.join(repositoryRoot, 'scripts', name), path.join(temporary, 'scripts', name))
    for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(temporary, name), JSON.stringify({ name: 'fixture', version: '0.1.0' }))
    const invoke = (script, args = []) => execFileSync(process.execPath, [path.join(temporary, 'scripts', script), ...args], { cwd: temporary, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000, maxBuffer: 64 << 10 })
    const rejects = (script, args, message) => assert.throws(() => invoke(script, args), error => error.status !== 0 && error.stderr.toString().includes(message))
    for (const value of ['2026.1.1', '2026.10.1', '2026.12.29']) assert.equal(validateTag(`v${value}`, validateVersion(value)), `v${value}`)
    for (const value of ['0.1.0', '2026.01.1', '2026.13.1', '2026.10.0', '2026.10.1-beta', '2026.10.9007199254740992']) assert.throws(() => validateVersion(value))
    invoke('release-version.mjs', ['set', '2026.10.1'])
    assert.equal(invoke('release-version.mjs', ['check', '--tag', 'v2026.10.1']).trim(), '2026.10.1')
    for (const name of ['package.json', 'apps/desktop/package.json']) assert.equal(JSON.parse(await readFile(path.join(temporary, name), 'utf8')).version, '2026.10.1')
    rejects('release-version.mjs', ['check', '--tag', 'v2026.10.2'], 'Expected tag')
    rejects('release-version.mjs', ['check', '--tag'], 'Usage:')

    const sourceSHA = '1'.repeat(40)
    const packageNames = [...distributionNames, ...desktopNames('2026.10.1'), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh']
    assert.equal(distributionNames.length, 6)
    assert.equal(desktopNames('2026.10.1').length, 12)
    for (const name of packageNames) await writeFile(path.join(temporary, 'input', name), `Small fixture only: ${name}\n`)
    await writeFile(path.join(temporary, 'input', 'checksums.txt'), 'stale build checksum: must be recalculated\n')
    const formats = { schema_version: 1, version: '2026.10.1', source_sha: sourceSHA, host_schema: 3, core_format: 10, data_formats: { host: 3, core: 10 }, core_module_version: 'fixture', migration_status: 'not-implemented' }
    const writeFormats = async value => writeFile(path.join(temporary, 'input', 'release-formats.json'), JSON.stringify(value))
    await writeFormats(formats)
    const baseArgs = ['candidate', '--input', 'input', '--sha', sourceSHA, '--tag', 'v2026.10.1', '--repository', 'oines/astral', '--run-url', 'https://github.com/oines/astral/actions/runs/1', '--ci-run-url', 'https://github.com/oines/astral/actions/runs/2']
    invoke('release-assets.mjs', [...baseArgs, '--output', 'complete-output'])
    const manifest = JSON.parse(await readFile(path.join(temporary, 'complete-output', 'release-manifest.json'), 'utf8'))
    assert.equal(manifest.public_release_ready, false)
    assert.equal(manifest.purpose, 'unsigned-candidate')
    assert.deepEqual(manifest.assets.map(asset => asset.name).sort(), [...packageNames, 'checksums.txt'].sort())
    for (const asset of manifest.assets) {
      assert.equal(await digest(path.join(temporary, 'complete-output', asset.name)), asset.sha256)
      assert.equal((await readFile(path.join(temporary, 'complete-output', asset.name))).length, asset.size)
    }
    const distributionSums = (await readFile(path.join(temporary, 'complete-output', 'checksums.txt'), 'utf8')).trim().split('\n')
    assert.equal(distributionSums.length, 6)
    for (const line of distributionSums) {
      const [expected, name] = line.split('  ')
      assert(distributionNames.includes(name))
      assert.equal(await digest(path.join(temporary, 'complete-output', name)), expected)
    }
    const allSums = (await readFile(path.join(temporary, 'complete-output', 'SHA256SUMS'), 'utf8')).trim().split('\n')
    assert.equal(allSums.length, packageNames.length + 3)
    for (const line of allSums) {
      const [expected, name] = line.split('  ')
      assert.equal(await digest(path.join(temporary, 'complete-output', name)), expected)
    }
    rejects('release-assets.mjs', [...baseArgs, '--output', 'complete-output'], 'must be empty')

    const missingName = 'Astral-2026.10.1-win-arm64.exe'
    const missingContents = await readFile(path.join(temporary, 'input', missingName))
    await rm(path.join(temporary, 'input', missingName))
    rejects('release-assets.mjs', [...baseArgs, '--output', 'missing-output'], 'Missing required release artifact')
    await writeFile(path.join(temporary, 'input', missingName), missingContents)
    await mkdir(path.join(temporary, 'input', 'duplicate'))
    const duplicate = path.join(temporary, 'input', 'duplicate', distributionNames[0])
    await writeFile(duplicate, 'different bytes under the same release name')
    rejects('release-assets.mjs', [...baseArgs, '--output', 'duplicate-output'], 'Different artifact inputs share the name')
    await rm(duplicate)
    await writeFile(path.join(temporary, 'input', 'latest-mac.yml'), 'not a wired production update feed')
    rejects('release-assets.mjs', [...baseArgs, '--output', 'metadata-output'], 'Unexpected candidate input')
    await rm(path.join(temporary, 'input', 'latest-mac.yml'))
    await writeFormats({ ...formats, source_sha: '2'.repeat(40) })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-sha-output'], 'metadata does not match')
    await writeFormats({ ...formats, version: '2026.10.2' })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-version-output'], 'metadata does not match')
    await writeFormats({ ...formats, data_formats: { host: 4, core: 10 } })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-formats-output'], 'metadata does not match')
    await writeFile(path.join(temporary, 'apps/desktop/package.json'), JSON.stringify({ version: '2026.10.2' }))
    rejects('release-version.mjs', ['check'], 'versions differ')
    const afterManifests = await Promise.all(['package.json', 'apps/desktop/package.json'].map(name => readFile(path.join(repositoryRoot, name), 'utf8')))
    assert.deepEqual(afterManifests, realManifests, 'Real workspace manifests must not change')
    console.log('Release tools: calendar versions, 6+12 package inventory, exact hashes and rejection fixtures passed. Native installation/updating was not exercised.')
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
}

if (await isDirectInvocation(import.meta.url)) {
  checkReleaseTools().catch(error => { console.error(error.message); process.exitCode = 1 })
}
