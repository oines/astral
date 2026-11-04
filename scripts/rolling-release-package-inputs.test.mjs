import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { desktopNames, distributionNames } from './release-assets.mjs'
import { gatherPackageInputs } from './rolling-release-package-inputs.mjs'

const hash = value => createHash('sha256').update(value).digest('hex')
async function fixture(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'astral-package-gather-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const version = '0.1.14', source = 'a'.repeat(40)
  const environment = { GITHUB_REPOSITORY: 'oines/astral', GITHUB_RUN_ID: '900', GH_TOKEN: 'fixture-only', ASTRAL_RELEASE_VERSION: version }
  await mkdir(path.join(root, '.release-metadata'), { recursive: true })
  await mkdir(path.join(root, 'apps/desktop'), { recursive: true })
  for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(root, name), JSON.stringify({ version: '0.1.13' }))
  const metadata = { schema_version: 1, repository: 'oines/astral', source_sha: source, version, build_run_id: 800, build_run_attempt: 1, build_run_number: 1, build_run_url: 'https://github.com/oines/astral/actions/runs/800', workflow_path: '.github/workflows/prepare-release.yml' }
  await writeFile(path.join(root, '.release-metadata/release-build.json'), JSON.stringify(metadata))
  const signed = Object.fromEntries([...distributionNames, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1'].map(name => [name, `Fixture ${name}`]))
  signed['release-formats.json'] = JSON.stringify({ schema_version: 1, version, source_sha: source })
  signed['checksums.txt'] = distributionNames.map(name => `${hash(signed[name])}  ${name}`).join('\n') + '\n'
  const payloads = new Map([
    ['signed-distribution', signed], ['mac-notary-binaries', { 'binaries.json': JSON.stringify({ version, source_sha: source }) }],
    ...['x64', 'arm64'].map(arch => [`desktop-mac-${arch}`, { ...Object.fromEntries(desktopNames(version).filter(name => name.includes(`-mac-${arch}.`)).map(name => [name, `Fixture ${name}`])), [`notarization/${arch}.json`]: JSON.stringify({ version, source_sha: source, arch }) }]),
    ...['win', 'linux'].map(platform => [`desktop-${platform}`, Object.fromEntries(desktopNames(version).filter(name => name.includes(`-${platform}-`)).map(name => [name, `Fixture ${name}`]))]),
  ])
  const artifacts = [...payloads.keys()].map((name, index) => ({ name, id: 100 + index, expired: false, digest: `sha256:${String(index + 1).repeat(64)}`, size_in_bytes: index + 30, workflow_run: { id: 900, head_sha: 'b'.repeat(40) } }))
  const downloaded = []
  const options = { root, environment,
    fetch: async (url, args) => {
      assert.equal(url, 'https://api.github.com/repos/oines/astral/actions/runs/900/artifacts?per_page=100&page=1')
      assert.equal(args.headers.Authorization, 'Bearer fixture-only')
      return new globalThis.Response(JSON.stringify({ artifacts }))
    },
    download: async args => {
      downloaded.push(args)
      assert.equal(args.runID, 900)
      assert.equal(args.expectedSHA, undefined, 'Same consumer artifacts use payload provenance, not workflow_run default-main head SHA')
      const artifact = artifacts.find(item => item.id === args.artifactID)
      for (const [name, bytes] of Object.entries(payloads.get(artifact.name))) {
        await mkdir(path.dirname(path.join(args.output, name)), { recursive: true })
        await writeFile(path.join(args.output, name), bytes)
      }
      return artifact
    },
  }
  return { root, version, source, artifacts, payloads, options, downloaded }
}

test('gathers six exact artifacts and keeps partial reports out of final collector inputs', async t => {
  const f = await fixture(t)
  const result = await gatherPackageInputs(f.options)
  assert.equal(result.source_sha, f.source)
  assert.equal(result.notarization_partials, 3)
  assert.equal(f.downloaded.length, 6)
  assert.deepEqual(JSON.parse(await readFile(path.join(f.root, '.release-metadata/package-inputs.json'), 'utf8')), result.references)
  assert.deepEqual((await readdir(path.join(f.root, 'release-inputs'))).sort(), ['desktop-linux', 'desktop-mac-arm64', 'desktop-mac-x64', 'desktop-win', 'signed-distribution'])
  const partials = await readdir(path.join(f.root, 'apps/desktop/release/notarization'))
  assert.deepEqual(partials.sort(), ['arm64.json', 'binaries.json', 'x64.json'])
  for (const arch of ['x64', 'arm64']) {
    assert.ok(!(await readdir(path.join(f.root, 'release-inputs', `desktop-mac-${arch}`))).includes('notarization'))
    for (const name of desktopNames(f.version).filter(value => value.includes(`-mac-${arch}.`))) assert.equal(await readFile(path.join(f.root, 'apps/desktop/release', name), 'utf8'), `Fixture ${name}`)
  }
})

test('sealed references restore only original IDs without selecting same-name replacement artifacts', async t => {
  const f = await fixture(t)
  const sealed = await gatherPackageInputs(f.options)
  f.downloaded.length = 0
  const result = await gatherPackageInputs({ ...f.options, references: sealed.references, output: path.join(f.root, 'restored-inputs'), copyNotarization: false,
    fetch: () => assert.fail('Restoration must not select artifacts from a mutable name inventory.') })
  assert.deepEqual(result.references, sealed.references)
  assert.deepEqual(f.downloaded.map(value => value.artifactID), sealed.artifact_ids)
  assert.ok(f.downloaded.every(value => value.expectedSHA === undefined))
})

test('sealed package identities reject replaced digests, source envelopes, IDs, expiry and actual selected-source claims', async t => {
  for (const change of [
    f => { f.artifacts[0].digest = `sha256:${'f'.repeat(64)}` },
    f => { f.artifacts[0].workflow_run.head_sha = 'c'.repeat(40) },
    f => { f.artifacts[0].workflow_run.id = 901 },
    f => { f.artifacts[0].expired = true },
    f => { f.payloads.get('signed-distribution')['release-formats.json'] = JSON.stringify({ schema_version: 1, version: f.version, source_sha: 'c'.repeat(40) }) },
  ]) {
    const f = await fixture(t)
    const sealed = await gatherPackageInputs(f.options)
    change(f)
    await assert.rejects(gatherPackageInputs({ ...f.options, references: sealed.references, output: path.join(f.root, 'restored-inputs'), copyNotarization: false }), /changed between|frozen release/)
    await assert.rejects(readdir(path.join(f.root, 'restored-inputs')), /ENOENT/)
  }
})

test('failed concurrent package downloads are all joined before their private directory is removed', async t => {
  const f = await fixture(t)
  const download = f.options.download
  let active = 0, completed = 0
  const result = gatherPackageInputs({ ...f.options, download: async args => {
    active++
    try {
      if (args.artifactID === f.artifacts[0].id) throw new Error('synthetic first download failure')
      await new Promise(resolve => setTimeout(resolve, 20))
      return await download(args)
    } finally { active--; completed++ }
  } })
  await assert.rejects(result, /synthetic first download failure/)
  assert.equal(active, 0)
  assert.equal(completed, 6)
})

test('missing, duplicate, expired and foreign-run artifacts are rejected before download', async t => {
  for (const change of [
    f => { f.artifacts.pop() }, f => { f.artifacts.push({ ...f.artifacts[0], id: 999 }) },
    f => { f.artifacts[0].expired = true }, f => { f.artifacts[0].workflow_run.id = 901 },
  ]) {
    const f = await fixture(t); change(f)
    await assert.rejects(gatherPackageInputs(f.options), /Missing, ambiguous or foreign/)
    assert.equal(f.downloaded.length, 0)
  }
})

test('unexpected package files and wrong actual source or version fail before copying', async t => {
  for (const change of [
    f => { f.payloads.get('desktop-win')['unexpected.json'] = '{}' },
    f => { f.payloads.get('signed-distribution')['release-formats.json'] = JSON.stringify({ schema_version: 1, version: f.version, source_sha: 'c'.repeat(40) }) },
    f => { f.payloads.get('desktop-mac-x64')['notarization/x64.json'] = JSON.stringify({ version: '0.1.15', source_sha: f.source, arch: 'x64' }) },
    f => { f.payloads.get('signed-distribution')[distributionNames[0]] = 'Changed signed program' },
  ]) {
    const f = await fixture(t); change(f)
    await assert.rejects(gatherPackageInputs(f.options), /payload|frozen release|frozen checksums/)
    await assert.rejects(readdir(path.join(f.root, 'release-inputs')), /ENOENT/)
  }
})

test('a pre-existing different Mac partial is preserved rather than overwritten', async t => {
  const f = await fixture(t)
  const target = path.join(f.root, 'apps/desktop/release/notarization/binaries.json')
  await mkdir(path.dirname(target), { recursive: true }); await writeFile(target, 'Preserve original')
  await assert.rejects(gatherPackageInputs(f.options), /different file already occupies/)
  assert.equal(await readFile(target, 'utf8'), 'Preserve original')
})

test('a nonempty release-inputs destination cannot mix old files into a new bundle', async t => {
  const f = await fixture(t)
  await mkdir(path.join(f.root, 'release-inputs')); await writeFile(path.join(f.root, 'release-inputs/sentinel'), 'keep')
  await assert.rejects(gatherPackageInputs(f.options), /new empty directory/)
  assert.equal(f.downloaded.length, 0)
  assert.equal(await readFile(path.join(f.root, 'release-inputs/sentinel'), 'utf8'), 'keep')
})
