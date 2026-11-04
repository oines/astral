import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { collectCandidate, desktopNames, distributionNames } from './release-assets.mjs'
import { desktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { imageRepository, platforms, recipeHash, recipePath } from './computer-image-contract.mjs'
import { verifyReleaseBundle } from './release-draft.mjs'
import { gatherPackageInputs } from './rolling-release-package-inputs.mjs'
import { restoreReleaseInputs, sealReleaseInputs } from './sealed-release-inputs.mjs'

const hash = value => createHash('sha256').update(value).digest('hex')
async function fixture(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'astral-sealed-inputs-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const version = '0.1.14', source = 'a'.repeat(40)
  const environment = { GITHUB_REPOSITORY: 'oines/astral', GITHUB_RUN_ID: '900', GH_TOKEN: 'fixture-only', ASTRAL_RELEASE_VERSION: version }
  const metadata = { schema_version: 1, repository: 'oines/astral', source_sha: source, version, build_run_id: 800, build_run_attempt: 1, build_run_number: 1, build_run_url: 'https://github.com/oines/astral/actions/runs/800', workflow_path: '.github/workflows/prepare-release.yml' }
  await mkdir(path.join(root, '.release-metadata'), { recursive: true })
  await mkdir(path.join(root, 'apps/desktop'), { recursive: true })
  await writeFile(path.join(root, '.release-metadata/release-build.json'), JSON.stringify(metadata))
  for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(root, name), JSON.stringify({ version: '0.1.13' }))
  const signed = Object.fromEntries([...distributionNames, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1'].map(name => [name, `Frozen ${name}`]))
  signed['release-formats.json'] = JSON.stringify({ schema_version: 1, version, source_sha: source })
  signed['checksums.txt'] = distributionNames.map(name => `${hash(signed[name])}  ${name}`).join('\n') + '\n'
  const payloads = new Map([
    ['signed-distribution', signed], ['mac-notary-binaries', { 'binaries.json': JSON.stringify({ version, source_sha: source }) }],
    ...['x64', 'arm64'].map(arch => [`desktop-mac-${arch}`, { ...Object.fromEntries(desktopNames(version).filter(name => name.includes(`-mac-${arch}.`)).map(name => [name, `Frozen ${name}`])), [`notarization/${arch}.json`]: JSON.stringify({ version, source_sha: source, arch }) }]),
    ...['win', 'linux'].map(platform => [`desktop-${platform}`, Object.fromEntries(desktopNames(version).filter(name => name.includes(`-${platform}-`)).map(name => [name, `Frozen ${name}`]))]),
  ])
  // The wake-up run's envelope identifies A while the selected payload source
  // is B. Restoration must bind both identities, without equating them.
  const artifacts = [...payloads.keys()].map((name, index) => ({ name, id: 100 + index, expired: false, digest: `sha256:${String(index + 1).repeat(64)}`, size_in_bytes: 30 + index,
    workflow_run: { id: 900, head_sha: 'b'.repeat(40), head_branch: 'main' } }))
  const downloads = []
  const download = async args => {
    downloads.push(args.artifactID)
    assert.equal(args.expectedSHA, undefined)
    const artifact = artifacts.find(item => item.id === args.artifactID)
    assert.ok(artifact, 'An original immutable artifact ID is required.')
    for (const [name, bytes] of Object.entries(payloads.get(artifact.name))) {
      await mkdir(path.dirname(path.join(args.output, name)), { recursive: true })
      await writeFile(path.join(args.output, name), bytes)
    }
    return artifact
  }
  await gatherPackageInputs({ root, environment, download, fetch: async () => new globalThis.Response(JSON.stringify({ artifacts })), copyNotarization: false })
  downloads.length = 0
  const directory = path.join(root, 'accepted-bundle'), output = path.join(root, 'sealed-metadata'), restored = path.join(root, 'restored-bundle')
  const bytes = new Map(Object.entries(signed))
  for (const [name, files] of payloads) if (name.startsWith('desktop-')) for (const [file, value] of Object.entries(files)) if (!file.startsWith('notarization/')) bytes.set(file, value)
  for (const feed of desktopUpdateFeeds(version)) bytes.set(feed.name, JSON.stringify({ immutable: feed.asset, version }))
  bytes.set('mac-signing-report.json', 'Frozen aggregate evidence')
  bytes.set('RELEASE_NOTES.md', 'Frozen release notes')
  const manifest = { schema_version: 1, repository: 'oines/astral', source_sha: source, version, tag: `v${version}`, purpose: 'release', public_release_ready: true,
    build_provenance: metadata, ...Object.fromEntries(['candidate_run_url', 'packaging_run_url', 'ci_run_url'].map(key => [key, 'https://github.com/oines/astral/actions/runs/900'])) }
  bytes.set('release-manifest.json', JSON.stringify(manifest))
  bytes.set('SHA256SUMS', [...bytes].map(([name, value]) => `${hash(value)}  ${name}`).join('\n') + '\n')
  assert.equal(bytes.size, 35)
  await mkdir(directory)
  for (const [name, value] of bytes) await writeFile(path.join(directory, name), value)
  // Existing release-publish tests cover the full public manifest/signing
  // validator. This oracle independently compares all 35 original byte strings
  // to prove reconstruction cannot rewrite or generate any public metadata.
  const verify = async target => {
    assert.deepEqual((await readdir(target)).sort(), [...bytes.keys()].sort())
    for (const [name, original] of bytes) assert.equal(await readFile(path.join(target, name), 'utf8'), original, name)
    return { manifest: JSON.parse(await readFile(path.join(target, 'release-manifest.json'), 'utf8')), files: [...bytes.keys()], sealSHA256: hash(bytes.get('SHA256SUMS')) }
  }
  const seal = () => sealReleaseInputs({ directory, output, root, environment, verify })
  const restore = result => restoreReleaseInputs({ input: output, output: restored, root, environment: { ...environment, ASTRAL_RELEASE_BUNDLE_SHA256: result.bundle_sha256, ASTRAL_RELEASE_INPUTS_SHA256: result.inputs_sha256 }, verify, download,
    fetch: () => assert.fail('Restoration cannot select a newer same-name artifact.') })
  return { root, directory, output, restored, environment, bytes, payloads, artifacts, downloads, download, verify, seal, restore }
}

test('seal uploads only 17 original metadata files plus separately attested artifact references, then restores exactly the same 35 assets', async t => {
  const f = await fixture(t)
  const sealed = await f.seal()
  assert.equal(sealed.public_assets, 35)
  assert.equal(sealed.metadata_files, 18)
  const names = await readdir(f.output)
  assert.ok(!names.some(name => distributionNames.includes(name) || desktopNames('0.1.14').includes(name)))
  assert.ok(names.includes('package-inputs.json'))
  const originalDescriptor = await readFile(path.join(f.root, '.release-metadata/package-inputs.json'))
  assert.deepEqual(await readFile(path.join(f.output, 'package-inputs.json')), originalDescriptor)
  const restored = await f.restore(sealed)
  assert.equal(restored.public_assets, 35)
  assert.deepEqual(restored.artifact_ids, sealed.artifact_ids)
  assert.deepEqual(f.downloads, sealed.artifact_ids)
  await f.verify(f.restored)
  assert.deepEqual(await readFile(path.join(f.root, '.release-metadata/package-inputs.json')), originalDescriptor, 'Restoration must not replace or regenerate frozen references.')
})

test('wake-up A with a later selected source B restores B without confusing its artifact envelope SHA with the source SHA', async t => {
  const f = await fixture(t)
  const sealed = await f.seal()
  const references = JSON.parse(await readFile(path.join(f.output, 'package-inputs.json'), 'utf8'))
  assert.notEqual(references.source_sha, references.artifacts[0].workflow_run.head_sha)
  const restored = await f.restore(sealed)
  assert.equal(restored.source_sha, 'a'.repeat(40))
  assert.ok(f.artifacts.every(artifact => artifact.workflow_run.head_sha === 'b'.repeat(40)))
})

test('a real collector and the production bundle verifier accept the unchanged reconstructed bundle', async t => {
  const f = await fixture(t)
  const version = '0.1.14', source = 'a'.repeat(40), runURL = 'https://github.com/oines/astral/actions/runs/900'
  const recipeSHA = recipeHash(await readFile(recipePath))
  const formats = {
    schema_version: 1, version, source_sha: source, core_schema: 12, storage_migrations: { core_sqlite: { baseline: 11, target: 12 } }, data_formats: { host: 3, core: 11 },
    host_schema: 3, core_format: 11, core_module_version: 'fixture', migration_status: 'goose', migration_baselines: { host: 3, core: 11 }, migration_targets: { host: 3, core: 11 },
    computer_environment: { schema_version: 1, image: imageRepository, tag: `env-${recipeSHA.slice(0, 12)}-456-1`, digest: `sha256:${'d'.repeat(64)}`, recipe_sha256: recipeSHA,
      platforms, source_sha: 'c'.repeat(40), build_run_url: 'https://github.com/oines/astral/actions/runs/456', created_at: '2026-10-27T00:00:00.000Z' },
  }
  const input = path.join(f.root, 'release-inputs')
  f.payloads.get('signed-distribution')['release-formats.json'] = JSON.stringify(formats)
  await writeFile(path.join(input, 'signed-distribution/release-formats.json'), JSON.stringify(formats))
  const report = {
    schema_version: 3, version, source_sha: source, team_id: '6N6W6Z6739', certificate_expires_at: '2099-01-01T00:00:00.000Z', status: 'signed', notarization_status: 'accepted', native_tested_arches: ['x64', 'arm64'],
    checks: { signatures: true, hardened_runtime: true, secure_timestamps: true, embedded_checksums: true, stapling: true, gatekeeper: true, packaged_app_startup_shutdown: true },
    notarizations: [{ kind: 'binaries' }, { kind: 'dmg', arch: 'arm64' }, { kind: 'dmg', arch: 'x64' }].map((value, index) => ({ ...value, id: `12345678-1234-4234-8234-${String(index + 1).padStart(12, '0')}`, status: 'Accepted' })),
    assets: [...f.bytes].filter(([name]) => name.startsWith('astral-darwin-') || name.includes('-mac-')).map(([name, value]) => ({ name, size: Buffer.byteLength(value), sha256: hash(value) })),
  }
  await writeFile(path.join(input, 'mac-signing-report.json'), JSON.stringify(report))
  const directory = path.join(f.root, 'valid-accepted'), output = path.join(f.root, 'valid-metadata'), restored = path.join(f.root, 'valid-restored')
  const metadata = JSON.parse(await readFile(path.join(f.root, '.release-metadata/release-build.json'), 'utf8'))
  const previousVersion = process.env.ASTRAL_RELEASE_VERSION
  try {
    process.env.ASTRAL_RELEASE_VERSION = version
    await collectCandidate({ input, output: directory, sourceSHA: source, repository: 'oines/astral', runURL, ciRunURL: runURL, tag: `v${version}`, macSigned: true, publicRelease: true, buildMetadata: metadata })
  } finally {
    if (previousVersion === undefined) delete process.env.ASTRAL_RELEASE_VERSION
    else process.env.ASTRAL_RELEASE_VERSION = previousVersion
  }
  const environment = { ...f.environment, CI: 'true', GITHUB_ACTIONS: 'true' }
  const sealed = await sealReleaseInputs({ directory, output, root: f.root, environment })
  const publicationEnvironment = { ...environment, ASTRAL_RELEASE_BUNDLE_SHA256: sealed.bundle_sha256, ASTRAL_RELEASE_INPUTS_SHA256: sealed.inputs_sha256 }
  await restoreReleaseInputs({ input: output, output: restored, root: f.root, environment: publicationEnvironment, download: f.download,
    fetch: () => assert.fail('Reconstruction uses the original IDs, never a new artifact inventory.') })
  const verified = await verifyReleaseBundle(restored, { environment: publicationEnvironment, requireSeal: true })
  assert.equal(verified.files.length, 35)
  for (const name of verified.files) assert.deepEqual(await readFile(path.join(restored, name)), await readFile(path.join(directory, name)), name)
})

test('an altered descriptor, index or small metadata file is rejected before downloading any large artifact', async t => {
  for (const name of ['package-inputs.json', 'SHA256SUMS', 'RELEASE_NOTES.md']) {
    const f = await fixture(t)
    const sealed = await f.seal()
    await writeFile(path.join(f.output, name), 'changed after sealing')
    await assert.rejects(f.restore(sealed), /verified digests|accepted checksum index/)
    assert.deepEqual(f.downloads, [])
  }
})

test('missing metadata, unexpected large payloads and missing descriptor attestation digests never restore', async t => {
  const f = await fixture(t)
  const sealed = await f.seal()
  await assert.rejects(restoreReleaseInputs({ input: f.output, output: f.restored, root: f.root, environment: { ...f.environment, ASTRAL_RELEASE_BUNDLE_SHA256: sealed.bundle_sha256 } }), /both independently verified/)
  await writeFile(path.join(f.output, distributionNames[0]), 'unexpected duplicate payload')
  await assert.rejects(f.restore(sealed), /Unexpected or missing/)
  await rm(path.join(f.output, distributionNames[0]))
  await rm(path.join(f.output, 'RELEASE_NOTES.md'))
  await assert.rejects(f.restore(sealed), /Unexpected or missing/)
  assert.deepEqual(f.downloads, [])
})

test('replaced API artifact identities or wrong final payload bytes cannot be restored even with an unchanged sealed metadata artifact', async t => {
  for (const change of [
    f => { f.artifacts[0].id = 999 },
    f => { f.artifacts[0].digest = `sha256:${'f'.repeat(64)}` },
    f => { f.artifacts[0].workflow_run.head_sha = 'c'.repeat(40) },
    f => { f.payloads.get('desktop-win')[desktopNames('0.1.14').find(name => name.includes('-win-'))] = 'different installer bytes' },
  ]) {
    const f = await fixture(t)
    const sealed = await f.seal()
    change(f)
    await assert.rejects(f.restore(sealed), /original immutable artifact ID|changed between|differ from the sealed release index/)
    await assert.rejects(readdir(f.restored), /ENOENT/)
  }
})

test('retry requires the exact original descriptor and refuses a different producer or packaging run before network access', async t => {
  for (const field of ['source_sha', 'packaging_run_id', 'build_provenance']) {
    const f = await fixture(t)
    const sealed = await f.seal()
    const file = path.join(f.output, 'package-inputs.json')
    const value = JSON.parse(await readFile(file, 'utf8'))
    if (field === 'source_sha') value.source_sha = 'c'.repeat(40)
    if (field === 'packaging_run_id') value.packaging_run_id = 901
    if (field === 'build_provenance') value.build_provenance.build_run_id = 801
    await writeFile(file, JSON.stringify(value))
    // Even an accidental caller update to the expected local descriptor digest
    // cannot bypass the manifest's exact producer and packaging identity.
    await assert.rejects(f.restore({ ...sealed, inputs_sha256: hash(await readFile(file)) }), /exact repository, source, producer or packaging run/)
    assert.deepEqual(f.downloads, [])
  }
})
