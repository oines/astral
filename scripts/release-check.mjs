import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { desktopNames, digest, distributionNames, macSigningReportName } from './release-assets.mjs'
import { isDirectInvocation, repositoryRoot, validateTag, validateVersion } from './release-version.mjs'

// Kilobyte fixtures only: no dependencies, network, build, real Astral execution,
// credentials, tags or releases. This does not establish native install/update acceptance.
export async function checkReleaseTools() {
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-release-fixture-'))
  const realManifests = await Promise.all(['package.json', 'apps/desktop/package.json'].map(name => readFile(path.join(repositoryRoot, name), 'utf8')))
  try {
    for (const name of ['scripts', 'apps/desktop', 'input']) await mkdir(path.join(temporary, name), { recursive: true })
    for (const name of ['release-version.mjs', 'release-assets.mjs', 'release-draft.mjs']) await copyFile(path.join(repositoryRoot, 'scripts', name), path.join(temporary, 'scripts', name))
    for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(temporary, name), JSON.stringify({ name: 'fixture', version: '0.0.0' }))
    const invoke = (script, args = []) => execFileSync(process.execPath, [path.join(temporary, 'scripts', script), ...args], { cwd: temporary, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000, maxBuffer: 64 << 10 })
    const rejects = (script, args, message) => assert.throws(() => invoke(script, args), error => error.status !== 0 && error.stderr.toString().includes(message))
    for (const value of ['0.0.0', '0.1.0', '0.1.1', '0.1.100', '1.0.0', '1.14.2', '9007199254740991.0.0']) assert.equal(validateTag(`v${value}`, validateVersion(value)), `v${value}`)
    for (const value of [undefined, null, 0, '0.01.0', '01.1.0', '0.1.00', '0.1.-1', 'v0.1.0', '0.1.0-beta', '0.1.0+build', '9007199254740992.0.0', '0.9007199254740992.0', '0.1.9007199254740992']) assert.throws(() => validateVersion(value))
    invoke('release-version.mjs', ['set', '0.1.0'])
    assert.equal(invoke('release-version.mjs', ['check', '--tag', 'v0.1.0']).trim(), '0.1.0')
    for (const name of ['package.json', 'apps/desktop/package.json']) assert.equal(JSON.parse(await readFile(path.join(temporary, name), 'utf8')).version, '0.1.0')
    rejects('release-version.mjs', ['check', '--tag', 'v0.1.1'], 'Expected tag')
    rejects('release-version.mjs', ['check', '--tag'], 'Usage:')

    const sourceSHA = '1'.repeat(40)
    const packageNames = [...distributionNames, ...desktopNames('0.1.0'), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1']
    assert.equal(distributionNames.length, 6)
    assert.equal(desktopNames('0.1.0').length, 12)
    for (const name of packageNames) await writeFile(path.join(temporary, 'input', name), `Small fixture only: ${name}\n`)
    await writeFile(path.join(temporary, 'input', 'checksums.txt'), 'stale build checksum: must be recalculated\n')
    const formats = { schema_version: 1, version: '0.1.0', source_sha: sourceSHA, host_schema: 3, core_format: 10, data_formats: { host: 3, core: 10 }, core_module_version: 'fixture', migration_status: 'not-implemented' }
    const writeFormats = async value => writeFile(path.join(temporary, 'input', 'release-formats.json'), JSON.stringify(value))
    await writeFormats(formats)
    const baseArgs = ['candidate', '--input', 'input', '--sha', sourceSHA, '--tag', 'v0.1.0', '--repository', 'oines/astral', '--run-url', 'https://github.com/oines/astral/actions/runs/1', '--ci-run-url', 'https://github.com/oines/astral/actions/runs/2']
    const publicArgs = ['public', ...baseArgs.slice(1)]
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

    const missingName = 'Astral-0.1.0-win-arm64.exe'
    const missingContents = await readFile(path.join(temporary, 'input', missingName))
    await rm(path.join(temporary, 'input', missingName))
    rejects('release-assets.mjs', [...baseArgs, '--output', 'missing-output'], 'Missing required release artifact')
    rejects('release-assets.mjs', [...publicArgs, '--output', 'public-missing-output'], 'Missing required release artifact')
    await writeFile(path.join(temporary, 'input', missingName), missingContents)
    await mkdir(path.join(temporary, 'input', 'duplicate'))
    const duplicate = path.join(temporary, 'input', 'duplicate', distributionNames[0])
    await writeFile(duplicate, 'different bytes under the same release name')
    rejects('release-assets.mjs', [...baseArgs, '--output', 'duplicate-output'], 'Different artifact inputs share the name')
    rejects('release-assets.mjs', [...publicArgs, '--output', 'public-duplicate-output'], 'Different artifact inputs share the name')
    await rm(duplicate)
    await writeFile(path.join(temporary, 'input', 'latest-mac.yml'), 'not a wired production update feed')
    rejects('release-assets.mjs', [...baseArgs, '--output', 'metadata-output'], 'Unexpected candidate input')
    rejects('release-assets.mjs', [...publicArgs, '--output', 'public-metadata-output'], 'Unexpected release input')
    await rm(path.join(temporary, 'input', 'latest-mac.yml'))
    await writeFormats({ ...formats, source_sha: '2'.repeat(40) })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-sha-output'], 'metadata does not match')
    await writeFormats({ ...formats, version: '0.1.1' })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-version-output'], 'metadata does not match')
    await writeFormats({ ...formats, data_formats: { host: 4, core: 10 } })
    rejects('release-assets.mjs', [...baseArgs, '--output', 'wrong-formats-output'], 'metadata does not match')
    await writeFormats(formats)

    rejects('release-assets.mjs', [...baseArgs, '--mac-signed', '--output', 'missing-signing-report'], 'Missing required release artifact: mac-signing-report.json')
    // Public mode has no unsigned fallback, even without an explicit --mac-signed flag.
    rejects('release-assets.mjs', [...publicArgs, '--output', 'public-missing-signing-report'], 'Missing required release artifact: mac-signing-report.json')
    const macNames = packageNames.filter(name => name.startsWith('astral-darwin-') || name.includes('-mac-'))
    const signedReport = {
      schema_version: 1, version: '0.1.0', source_sha: sourceSHA, team_id: '6N6W6Z6739',
      certificate_expires_at: new Date(Date.now() + 366 * 24 * 60 * 60 * 1000).toISOString(),
      status: 'accepted', native_tested_arch: 'arm64',
      checks: { signatures: true, hardened_runtime: true, secure_timestamps: true, embedded_checksums: true, stapling: true, gatekeeper: true, packaged_app_startup_shutdown: true },
      notarizations: [
        { kind: 'binaries' }, { kind: 'dmg', arch: 'arm64' }, { kind: 'dmg', arch: 'x64' },
      ].map((item, index) => ({ ...item, id: `12345678-1234-4234-8234-${String(index + 1).padStart(12, '0')}`, status: 'Accepted' })),
      assets: await Promise.all(macNames.map(async name => ({ name, size: (await readFile(path.join(temporary, 'input', name))).length, sha256: await digest(path.join(temporary, 'input', name)) }))),
    }
    const writeSigningReport = report => writeFile(path.join(temporary, 'input', macSigningReportName), JSON.stringify(report))
    await writeSigningReport(signedReport)
    rejects('release-assets.mjs', [...baseArgs, '--output', 'implicit-signing-output'], 'Unexpected candidate input mac-signing-report.json')
    invoke('release-assets.mjs', [...baseArgs, '--mac-signed', '--output', 'signed-output'])
    const signedManifest = JSON.parse(await readFile(path.join(temporary, 'signed-output', 'release-manifest.json'), 'utf8'))
    assert.equal(signedManifest.public_release_ready, false)
    assert.equal(signedManifest.purpose, 'mac-signed-candidate')
    assert.deepEqual(signedManifest.signing, { macos: 'signed-and-notarized', windows: 'not-signed', linux: 'not-signed' })
    assert.equal(signedManifest.migration_status, 'not-implemented')
    assert.equal(signedManifest.desktop_updater, 'not-implemented')
    assert.deepEqual(signedManifest.assets.map(asset => asset.name).sort(), [...packageNames, 'checksums.txt', macSigningReportName].sort())
    for (const asset of signedManifest.assets) {
      assert.equal(await digest(path.join(temporary, 'signed-output', asset.name)), asset.sha256)
      assert.equal((await readFile(path.join(temporary, 'signed-output', asset.name))).length, asset.size)
    }
    const signedSums = (await readFile(path.join(temporary, 'signed-output', 'SHA256SUMS'), 'utf8')).trim().split('\n')
    assert.equal(signedSums.length, packageNames.length + 4)
    assert(signedSums.some(line => line.endsWith(`  ${macSigningReportName}`)))
    for (const line of signedSums) {
      const [expected, name] = line.split('  ')
      assert.equal(await digest(path.join(temporary, 'signed-output', name)), expected)
    }
    const signedNotes = await readFile(path.join(temporary, 'signed-output', 'RELEASE_NOTES.md'), 'utf8')
    assert(signedNotes.includes(`<!-- astral-mac-signed-candidate:${sourceSHA} -->`))
    assert(!signedNotes.includes('<!-- astral-unsigned-candidate:'))
    assert(signedNotes.includes('verified signed and notarized'))
    assert(signedNotes.includes('not implemented; public-release gate remains pending'))

    assert(invoke('release-assets.mjs', [...publicArgs, '--output', 'public-output']).includes('public_release_ready=true'))
    const publicManifest = JSON.parse(await readFile(path.join(temporary, 'public-output', 'release-manifest.json'), 'utf8'))
    assert.equal(publicManifest.purpose, 'release')
    assert.equal(publicManifest.public_release_ready, true)
    assert.equal(publicManifest.source_sha, sourceSHA)
    assert.equal(publicManifest.version, '0.1.0')
    assert.equal(publicManifest.tag, 'v0.1.0')
    assert.equal(publicManifest.repository, 'oines/astral')
    assert.equal(publicManifest.candidate_run_url, 'https://github.com/oines/astral/actions/runs/1')
    assert.equal(publicManifest.ci_run_url, 'https://github.com/oines/astral/actions/runs/2')
    assert.equal(publicManifest.host_schema, 3)
    assert.equal(publicManifest.core_format, 10)
    assert.deepEqual(publicManifest.data_formats, { host: 3, core: 10 })
    assert.equal(publicManifest.core_module_version, 'fixture')
    assert.equal(publicManifest.migration_status, 'not-implemented')
    assert.equal(publicManifest.desktop_updater, 'not-implemented')
    assert.deepEqual(publicManifest.signing, signedManifest.signing)
    assert.deepEqual(publicManifest.distribution_names, distributionNames)
    assert.deepEqual(publicManifest.assets, signedManifest.assets)
    for (const asset of publicManifest.assets) {
      assert.equal(await digest(path.join(temporary, 'public-output', asset.name)), asset.sha256)
      assert.equal((await readFile(path.join(temporary, 'public-output', asset.name))).length, asset.size)
    }
    const publicSums = (await readFile(path.join(temporary, 'public-output', 'SHA256SUMS'), 'utf8')).trim().split('\n')
    assert.equal(publicSums.length, packageNames.length + 4)
    assert.deepEqual(publicSums.map(line => line.split('  ')[1]).sort(), [...packageNames, macSigningReportName, 'checksums.txt', 'release-manifest.json', 'RELEASE_NOTES.md'].sort())
    for (const line of publicSums) {
      const [expected, name] = line.split('  ')
      assert.equal(await digest(path.join(temporary, 'public-output', name)), expected)
    }
    const publicNotes = await readFile(path.join(temporary, 'public-output', 'RELEASE_NOTES.md'), 'utf8')
    for (const text of ['macOS:', 'Windows:', 'Linux Desktop:', 'Linux VPS:', 'signed and notarized', 'Windows packages are not code-signed', 'Linux packages are not code-signed', 'Manual updates are supported only while the data formats remain the same', 'one-click updates are not implemented', 'Versioned data migrations and their rollback/backup acceptance are not implemented', 'Existing data in those formats can be retained', 'Earlier private data formats are outside this supported baseline', 'Goose migrations', `<!-- astral-release:${sourceSHA} -->`]) assert(publicNotes.includes(text), `Missing public release note: ${text}`)
    for (const text of ['review-only', 'Draft Release', 'not approved for public distribution', 'public-release gate remains pending', 'Do not publish']) assert(!publicNotes.includes(text), `Draft-only language in public release notes: ${text}`)
    rejects('release-assets.mjs', [...publicArgs, '--output', 'public-output'], 'must be empty')

    for (const [name, value, message] of [
      ['wrong-source', { ...formats, source_sha: '2'.repeat(40) }, 'metadata does not match'],
      ['wrong-version', { ...formats, version: '0.1.1' }, 'metadata does not match'],
      ['inconsistent-format', { ...formats, data_formats: { host: 4, core: 10 } }, 'metadata does not match'],
      ['invented-migrations', { ...formats, migration_status: 'implemented' }, 'metadata does not match'],
      ['changed-host', { ...formats, host_schema: 4, data_formats: { host: 4, core: 10 } }, 'initial host schema 3 / core format 10 baseline'],
      ['changed-core', { ...formats, core_format: 11, data_formats: { host: 3, core: 11 } }, 'initial host schema 3 / core format 10 baseline'],
    ]) {
      await writeFormats(value)
      rejects('release-assets.mjs', [...publicArgs, '--output', `public-${name}`], message)
      // Drafts can inspect consistent future formats without claiming public readiness.
      if (name.startsWith('changed-')) invoke('release-assets.mjs', [...baseArgs, '--mac-signed', '--output', `draft-${name}`])
    }
    await writeFormats(formats)
    for (const [name, change, message] of [
      ['wrong-report-sha', report => { report.source_sha = '2'.repeat(40) }, 'metadata does not match'],
      ['wrong-report-version', report => { report.version = '0.1.1' }, 'metadata does not match'],
      ['wrong-report-team', report => { report.team_id = 'K38CJP7Z2L' }, 'metadata does not match'],
      ['wrong-report-status', report => { report.status = 'pending' }, 'metadata does not match'],
      ['wrong-report-date', report => { report.certificate_expires_at = '2099-02-30T00:00:00Z' }, 'metadata does not match'],
      ['expired-report', report => { report.certificate_expires_at = '2000-01-01T00:00:00Z' }, 'metadata does not match'],
      ['wrong-native-arch', report => { report.native_tested_arch = 'universal' }, 'metadata does not match'],
      ['failed-check', report => { report.checks.stapling = false }, 'must pass every required'],
      ['fake-check', report => { report.checks.signatures = 'true' }, 'must pass every required'],
      ['missing-check', report => { delete report.checks.packaged_app_startup_shutdown }, 'must pass every required'],
      ['wrong-report-hash', report => { report.assets[0].sha256 = '0'.repeat(64) }, 'does not match final artifact bytes'],
      ['wrong-report-size', report => { report.assets[0].size++ }, 'does not match final artifact bytes'],
      ['missing-report-asset', report => { report.assets.pop() }, 'exactly the two Darwin binaries'],
      ['duplicate-report-asset', report => { report.assets[1] = report.assets[0] }, 'exactly the two Darwin binaries'],
      ['missing-notarization', report => { report.notarizations.pop() }, 'exactly three distinct accepted'],
      ['failed-notarization', report => { report.notarizations[0].status = 'Invalid' }, 'exactly three distinct accepted'],
      ['wrong-notarization-uuid', report => { report.notarizations[0].id = 'not-a-uuid' }, 'exactly three distinct accepted'],
      ['duplicate-notarization', report => { report.notarizations[1].id = report.notarizations[0].id }, 'exactly three distinct accepted'],
      ['wrong-notarization-arch', report => { report.notarizations[1].arch = 'x64' }, 'exactly three distinct accepted'],
      ['wrong-notarization-kind', report => { report.notarizations[1].kind = 'app' }, 'exactly three distinct accepted'],
    ]) {
      const report = JSON.parse(JSON.stringify(signedReport))
      change(report)
      await writeSigningReport(report)
      rejects('release-assets.mjs', [...baseArgs, '--mac-signed', '--output', name], message)
      rejects('release-assets.mjs', [...publicArgs, '--output', `public-${name}`], message)
    }
    await writeSigningReport(signedReport)
    // An invalid API origin proves draft validation completed without making any network request.
    const draftPreflight = directory => execFileSync(process.execPath, [path.join(temporary, 'scripts', 'release-draft.mjs'), directory], {
      cwd: temporary, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000,
      env: { ...process.env, GH_TOKEN: 'fixture-no-network', GITHUB_REPOSITORY: 'oines/astral', GITHUB_API_URL: 'https://fixtures.invalid' },
    })
    for (const directory of ['complete-output', 'signed-output']) assert.throws(() => draftPreflight(directory), error => error.stderr.toString().includes('configured only for github.com'))
    assert.throws(() => draftPreflight('public-output'), error => error.stderr.toString().includes('public releases and incomplete signing claims are forbidden'))
    await writeFile(path.join(temporary, 'signed-output', 'release-manifest.json'), JSON.stringify({ ...signedManifest, public_release_ready: true }))
    assert.throws(() => draftPreflight('signed-output'), error => error.stderr.toString().includes('public releases and incomplete signing claims are forbidden'))
    await writeFile(path.join(temporary, 'signed-output', 'release-manifest.json'), JSON.stringify({ ...signedManifest, purpose: 'public-release' }))
    assert.throws(() => draftPreflight('signed-output'), error => error.stderr.toString().includes('public releases and incomplete signing claims are forbidden'))
    await writeFile(path.join(temporary, 'apps/desktop/package.json'), JSON.stringify({ version: '0.1.1' }))
    rejects('release-version.mjs', ['check'], 'versions differ')
    const afterManifests = await Promise.all(['package.json', 'apps/desktop/package.json'].map(name => readFile(path.join(repositoryRoot, name), 'utf8')))
    assert.deepEqual(afterManifests, realManifests, 'Real workspace manifests must not change')
    console.log('Release tools: rolling versions, unsigned and Mac signed draft/public inventories, initial public format baseline, final-byte signing reports, hashes and rejection fixtures passed. Fixtures do not establish actual CI, Apple acceptance or native installation/updating.')
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
}

if (await isDirectInvocation(import.meta.url)) {
  checkReleaseTools().catch(error => { console.error(error.message); process.exitCode = 1 })
}
