import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { createRequire } from 'node:module'
import path from 'node:path'
import test from 'node:test'
import { desktopArtifactName, desktopUpdateFeeds, writeDesktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { desktopNames, digest, distributionNames, macSigningReportName } from './release-assets.mjs'
import { compareVersions, createDraft } from './release-draft.mjs'
import { readReleaseVersion } from './release-version.mjs'

// Tiny fixtures and injected HTTP only: no GitHub calls, credentials or builds.
const sourceSHA = 'a'.repeat(40)
const repository = 'oines/astral'
const clone = value => JSON.parse(JSON.stringify(value))
const json = (body, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => clone(body) })

test('release inventories use the native electron-builder Linux target artifact names', () => {
  const linux = desktopNames('0.1.0').filter(name => name.includes('-linux-')).sort()
  assert.deepEqual(linux, [
    'Astral-0.1.0-linux-amd64.deb',
    'Astral-0.1.0-linux-arm64.AppImage',
    'Astral-0.1.0-linux-arm64.deb',
    'Astral-0.1.0-linux-x86_64.AppImage',
  ])
  assert.equal(desktopUpdateFeeds('0.1.0').find(feed => feed.name === 'latest-x64-linux.yml').asset, 'Astral-0.1.0-linux-x86_64.AppImage')
})

test('the installed electron-updater reads all six final-byte feeds using its actual platform naming rules', async t => {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-real-updater-feed-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const version = await readReleaseVersion()
  for (const { asset } of desktopUpdateFeeds(version)) await writeFile(path.join(directory, asset), `Final bytes for ${asset}`)
  await writeDesktopUpdateFeeds(directory, version)
  const require = createRequire(new URL('../apps/desktop/package.json', import.meta.url))
  const { GenericProvider } = require(path.join(path.dirname(require.resolve('electron-updater')), 'providers/GenericProvider.js'))
  const previous = process.env.TEST_UPDATER_ARCH
  try {
    for (const arch of ['x64', 'arm64']) {
      process.env.TEST_UPDATER_ARCH = arch
      for (const platform of ['win32', 'darwin', 'linux']) {
        let requested
        const provider = new GenericProvider({ url: 'https://github.com/oines/astral/releases/latest/download/', channel: `latest-${arch}` }, {}, {
          platform,
          executor: { request: async options => {
            requested = path.basename(options.path.split('?', 1)[0])
            return readFile(path.join(directory, requested), 'utf8')
          } },
        })
        const info = await provider.getLatestVersion()
        const expected = desktopUpdateFeeds(version).find(feed => feed.name === requested)
        assert.ok(expected, `Unexpected channel filename for ${platform}/${arch}: ${requested}`)
        const platformName = { win32: 'win', darwin: 'mac', linux: 'linux' }[platform]
        assert.equal(expected.asset, desktopArtifactName(version, platformName, arch, platform === 'win32' ? 'exe' : platform === 'darwin' ? 'zip' : 'AppImage'))
        assert.equal(info.version, version)
        assert.equal(provider.resolveFiles(info)[0].url.href, `https://github.com/oines/astral/releases/download/v${version}/${expected.asset}`)
      }
    }
  } finally {
    if (previous === undefined) delete process.env.TEST_UPDATER_ARCH
    else process.env.TEST_UPDATER_ARCH = previous
  }
})

async function fixture(t, { candidate = false, unsigned = false, published = false, draft = false, hideTagLookup = false, previous = [], failUpload = false, wrongUploadDigest = false, movedTag = false } = {}) {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-publish-fixture-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const version = await readReleaseVersion()
  const tag = `v${version}`
  const macSigned = !unsigned
  const names = [...distributionNames, ...desktopNames(version), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1']
  for (const name of names) await writeFile(path.join(directory, name), `Small fixture: ${name}\n`)
  await writeFile(path.join(directory, 'checksums.txt'), `${(await Promise.all(distributionNames.map(async name => `${await digest(path.join(directory, name))}  ${name}`))).join('\n')}\n`)
  names.push('checksums.txt')
  if (macSigned) {
    const report = {
      schema_version: 2, version, source_sha: sourceSHA, team_id: '6N6W6Z6739',
      certificate_expires_at: new Date(Date.now() + 366 * 86_400_000).toISOString(), status: 'signed', notarization_status: 'accepted', native_tested_arch: 'arm64',
      checks: { signatures: true, hardened_runtime: true, secure_timestamps: true, embedded_checksums: true, stapling: true, gatekeeper: true, packaged_app_startup_shutdown: true },
      notarizations: [{ kind: 'binaries' }, { kind: 'dmg', arch: 'arm64' }, { kind: 'dmg', arch: 'x64' }].map((item, index) => ({ ...item, id: `12345678-1234-4234-8234-${String(index + 1).padStart(12, '0')}`, status: 'Accepted' })),
      assets: await Promise.all(names.filter(name => name.startsWith('astral-darwin-') || name.includes('-mac-')).map(async name => ({ name, size: (await readFile(path.join(directory, name))).length, sha256: await digest(path.join(directory, name)) }))),
    }
    await writeFile(path.join(directory, macSigningReportName), JSON.stringify(report))
    names.push(macSigningReportName)
  }
  await writeDesktopUpdateFeeds(directory, version)
  names.push(...desktopUpdateFeeds(version).map(feed => feed.name))
  const formats = { schema_version: 1, version, source_sha: sourceSHA, data_formats: { host: 3, core: 10 }, host_schema: 3, core_format: 10, core_module_version: 'fixture', migration_status: 'goose', migration_baselines: { host: 3, core: 10 }, migration_targets: { host: 3, core: 10 } }
  await writeFile(path.join(directory, 'release-formats.json'), JSON.stringify(formats))
  names.push('release-formats.json')
  const purpose = candidate ? (macSigned ? 'mac-signed-candidate' : 'unsigned-candidate') : 'release'
  const notes = `Astral ${version}\n\n<!-- astral-${purpose}:${sourceSHA} -->\n`
  const manifest = {
    schema_version: 1, purpose, public_release_ready: !candidate, version, tag, source_sha: sourceSHA, repository,
    candidate_run_url: `https://github.com/${repository}/actions/runs/123`, ci_run_url: `https://github.com/${repository}/actions/runs/122`,
    data_formats: { host: 3, core: 10 }, host_schema: 3, core_format: 10, core_module_version: 'fixture',
    migration_status: 'goose', migration_baselines: { host: 3, core: 10 }, migration_targets: { host: 3, core: 10 }, desktop_updater: 'electron-updater',
    signing: { macos: macSigned ? 'signed-and-notarized' : 'not-signed-or-notarized', windows: 'not-signed', linux: 'not-signed' },
    distribution_names: distributionNames,
    assets: await Promise.all(names.map(async name => ({ name, size: (await readFile(path.join(directory, name))).length, sha256: await digest(path.join(directory, name)) }))),
  }
  await writeFile(path.join(directory, 'release-manifest.json'), JSON.stringify(manifest))
  await writeFile(path.join(directory, 'RELEASE_NOTES.md'), notes)
  async function refreshSums() {
    const files = (await readdir(directory)).filter(name => name !== 'SHA256SUMS').sort()
    await writeFile(path.join(directory, 'SHA256SUMS'), `${(await Promise.all(files.map(async name => `${await digest(path.join(directory, name))}  ${name}`))).join('\n')}\n`)
  }
  async function updateManifest(change) {
    change(manifest)
    await writeFile(path.join(directory, 'release-manifest.json'), JSON.stringify(manifest))
    await refreshSums()
  }
  await refreshSums()
  const remote = { release: undefined, assets: [], calls: [], output: [] }
  if (published || draft) {
    remote.release = { id: 17, tag_name: tag, target_commitish: sourceSHA, body: notes, draft: !published, prerelease: false, upload_url: `https://uploads.github.com/repos/${repository}/releases/17/assets{?name,label}`, html_url: `https://github.com/${repository}/releases/tag/${tag}` }
    remote.assets = await Promise.all((await readdir(directory)).map(async name => ({ name, size: (await readFile(path.join(directory, name))).length, digest: `sha256:${await digest(path.join(directory, name))}`, state: 'uploaded' })))
    if (draft) remote.assets = remote.assets.slice(0, 1)
  }
  let tagReads = 0
  const environment = {
    GH_TOKEN: 'fixture-token-no-network', GITHUB_REPOSITORY: repository,
    CI: 'true', GITHUB_ACTIONS: 'true', GITHUB_RUN_ID: '123', GITHUB_SERVER_URL: 'https://github.com',
    // A manually selected tag can differ from the workflow_dispatch SHA.
    GITHUB_SHA: 'b'.repeat(40),
  }
  const options = {
    publicRelease: !candidate, environment, log: value => remote.output.push(value),
    fetch: async (input, options = {}) => {
      const url = new URL(input)
      const method = options.method || 'GET'
      const body = typeof options.body === 'string' ? JSON.parse(options.body) : undefined
      remote.calls.push({ method, path: `${url.pathname}${url.search}`, body })
      assert.equal(options.redirect, 'error')
      assert.equal(options.headers.Authorization, 'Bearer fixture-token-no-network')
      if (url.hostname === 'uploads.github.com') {
        assert.equal(method, 'POST')
        const chunks = []
        for await (const chunk of options.body) chunks.push(chunk)
        const bytes = Buffer.concat(chunks)
        if (failUpload) return json({}, 500)
        const asset = { name: url.searchParams.get('name'), size: bytes.length, digest: `sha256:${createHash('sha256').update(bytes).digest('hex')}`, state: 'uploaded' }
        if (wrongUploadDigest) asset.digest = `sha256:${'0'.repeat(64)}`
        remote.assets.push(asset)
        return json(asset)
      }
      assert.equal(url.hostname, 'api.github.com')
      const route = url.pathname.replace(`/repos/${repository}`, '')
      if (route === `/git/ref/tags/${tag}`) {
        tagReads++
        return json({ object: { type: 'commit', sha: movedTag && tagReads > 1 ? 'c'.repeat(40) : sourceSHA } })
      }
      if (route === `/releases/tags/${tag}`) return remote.release && !hideTagLookup ? json(remote.release) : json({}, 404)
      if (route === '/releases' && method === 'POST') {
        remote.release = { ...body, id: 17, upload_url: `https://uploads.github.com/repos/${repository}/releases/17/assets{?name,label}`, html_url: `https://github.com/${repository}/releases/tag/${tag}` }
        return json(remote.release)
      }
      if (route === '/releases' && method === 'GET') {
        const page = Number(url.searchParams.get('page'))
        const releases = [...previous, ...(remote.release ? [remote.release] : [])]
        return json(releases.slice((page - 1) * 100, page * 100))
      }
      if (route === '/releases/17/assets') return json(remote.assets)
      if (route === '/releases/17' && method === 'GET') return json(remote.release)
      if (route === '/releases/17' && method === 'PATCH') {
        remote.release = { ...remote.release, ...body }
        return json(remote.release)
      }
      assert.fail(`Unexpected fake API request: ${method} ${route}`)
    },
  }
  return { directory, manifest, notes, remote, options, updateManifest, refreshSums }
}

test('public publishing verifies every upload before its final publication mutation', async t => {
  const value = await fixture(t, { previous: [{ draft: false, prerelease: false, tag_name: 'v0.0.9' }] })
  const release = await createDraft(value.directory, value.options)
  assert.equal(release.draft, false)
  assert.equal(release.make_latest, 'true')
  assert.equal(value.remote.assets.length, (await readdir(value.directory)).length)
  const mutations = value.remote.calls.filter(call => call.method !== 'GET')
  assert.equal(mutations[0].body.draft, true)
  assert.deepEqual(mutations.at(-1), { method: 'PATCH', path: '/repos/oines/astral/releases/17', body: { draft: false, make_latest: 'true' } })
  assert.equal(mutations.filter(call => call.method === 'PATCH').length, 1)
  assert.ok(value.remote.output[0].includes('Published Astral'))
})

test('older releases publish without taking Latest, including a newer release on the second API page', async t => {
  const previous = Array.from({ length: 100 }, (_, index) => ({ draft: false, prerelease: false, tag_name: `v0.0.${index}` }))
  previous.push({ draft: false, prerelease: false, tag_name: 'v0.10.0' })
  const value = await fixture(t, { previous })
  const release = await createDraft(value.directory, value.options)
  assert.equal(release.make_latest, 'false')
  assert.ok(value.remote.calls.some(call => call.path.endsWith('?per_page=100&page=2')))
  assert.equal(compareVersions('0.1.10', '0.1.9'), 1)
  assert.equal(compareVersions('0.10.0', '0.2.100'), 1)
  assert.equal(compareVersions('1.0.0', '0.999.999'), 1)
  assert.equal(compareVersions('0.1.0', '0.1.0'), 0)
  assert.equal(compareVersions('9007199254740991.0.0', '9007199254740990.999.999'), 1)
})

test('an identical published tag is checked without modifying assets, release notes or Latest', async t => {
  const value = await fixture(t, { published: true })
  const before = clone(value.remote.release)
  await createDraft(value.directory, value.options)
  assert.deepEqual(value.remote.release, before)
  assert.ok(value.remote.calls.every(call => call.method === 'GET'))
  assert.ok(value.remote.output[0].includes('already published'))
})

test('tag lookup 404 resumes a unique paginated draft by ID without creating another release', async t => {
  for (const candidate of [false, true]) {
    const previous = Array.from({ length: 100 }, (_, index) => ({ id: index + 100, draft: true, prerelease: false, tag_name: `v0.0.${index}` }))
    const value = await fixture(t, { candidate, unsigned: candidate, draft: true, hideTagLookup: true, previous })
    const retained = clone(value.remote.assets[0])
    const release = await createDraft(value.directory, value.options)
    assert.equal(release.id, 17)
    assert.equal(release.draft, candidate)
    assert.deepEqual(value.remote.assets[0], retained)
    assert.equal(value.remote.assets.length, (await readdir(value.directory)).length)
    assert.ok(value.remote.calls.some(call => call.path.endsWith('?per_page=100&page=2')))
    assert.ok(value.remote.calls.some(call => call.method === 'GET' && call.path === '/repos/oines/astral/releases/17'))
    assert.ok(!value.remote.calls.some(call => call.method === 'POST' && call.path === '/repos/oines/astral/releases'))
    assert.ok(!value.remote.calls.some(call => call.path.includes(`name=${encodeURIComponent(retained.name)}`)))
  }
})

test('draft fallback rejects duplicate tags across pages instead of choosing the first match', async t => {
  const previous = Array.from({ length: 99 }, (_, index) => ({ id: index + 100, draft: true, prerelease: false, tag_name: `v0.0.${index}` }))
  const value = await fixture(t, { draft: true, hideTagLookup: true, previous })
  previous.push({ ...clone(value.remote.release), id: 18 })
  await assert.rejects(createDraft(value.directory, value.options), /Multiple releases use this tag/)
  assert.ok(value.remote.calls.some(call => call.path.endsWith('?per_page=100&page=2')))
  assert.ok(value.remote.calls.every(call => call.method === 'GET'))
  assert.equal(value.remote.release.draft, true)
})

test('a draft found through release listing must still identify the exact source and notes', async t => {
  for (const change of [
    value => { value.remote.release.target_commitish = 'c'.repeat(40) },
    value => { value.remote.release.body += 'Unrelated draft notes' },
  ]) {
    const value = await fixture(t, { draft: true, hideTagLookup: true })
    change(value)
    await assert.rejects(createDraft(value.directory, value.options), /unrelated source, tag or notes/)
    assert.ok(value.remote.calls.every(call => call.method === 'GET'))
    assert.equal(value.remote.release.draft, true)
  }
})

test('published tags reject different source, notes, assets or upload states without mutations', async t => {
  for (const change of [
    value => { value.remote.release.target_commitish = 'c'.repeat(40) },
    value => { value.remote.release.body += 'Edited published notes' },
    value => { value.remote.assets.pop() },
    value => { value.remote.assets[0].digest = `sha256:${'0'.repeat(64)}` },
    value => { value.remote.assets[0].state = 'new' },
  ]) {
    const value = await fixture(t, { published: true })
    change(value)
    await assert.rejects(createDraft(value.directory, value.options), /unrelated source|inventory is incomplete|does not match local bytes/)
    assert.ok(value.remote.calls.every(call => call.method === 'GET'))
  }
})

test('public mode requires GitHub Actions context, matching run metadata and Goose migration declarations', async t => {
  for (const change of [
    value => { value.options.environment.CI = 'false' },
    value => { value.options.environment.GITHUB_ACTIONS = 'false' },
    value => { value.options.environment.GITHUB_RUN_ID = '124' },
    value => { value.options.environment.GITHUB_REPOSITORY = 'other/repo' },
    value => { value.manifest.ci_run_url = 'https://github.com/other/repo/actions/runs/122' },
    value => { value.manifest.host_schema = 4 },
    value => { value.manifest.data_formats.core = 11 },
    value => { value.manifest.migration_status = 'implemented' },
    value => { value.manifest.signing.macos = 'not-signed-or-notarized' },
  ]) {
    const value = await fixture(t)
    change(value)
    await value.updateManifest(() => {})
    await assert.rejects(createDraft(value.directory, value.options), /matching release manifest|matching release\/CI|data baseline/)
    assert.equal(value.remote.calls.length, 0)
  }
})

test('valid Developer ID signatures can publish while Apple is still processing, without claiming acceptance', async t => {
  const value = await fixture(t)
  const name = path.join(value.directory, macSigningReportName)
  const report = JSON.parse(await readFile(name, 'utf8'))
  report.notarizations[0].status = 'In Progress'
  report.notarizations[1].status = 'Unknown'
  report.notarization_status = 'pending'
  report.checks.stapling = false
  report.checks.gatekeeper = false
  await writeFile(name, JSON.stringify(report))
  await value.updateManifest(manifest => {
    manifest.signing.macos = 'signed-notarization-pending'
  })
  // Rebuild evidence for changed bytes rather than relaxing final-byte checks.
  const record = value.manifest.assets.find(asset => asset.name === macSigningReportName)
  record.size = (await readFile(name)).length
  record.sha256 = await digest(name)
  await value.updateManifest(() => {})
  const release = await createDraft(value.directory, value.options)
  assert.equal(release.draft, false)
})

test('rejected notarizations and false accepted claims fail before contacting GitHub', async t => {
  for (const change of [
    report => { report.notarizations[0].status = 'Invalid' },
    report => { report.notarizations[0].status = 'Rejected' },
    report => { report.notarizations[0].status = 'In Progress' },
  ]) {
    const value = await fixture(t)
    const file = path.join(value.directory, macSigningReportName)
    const report = JSON.parse(await readFile(file, 'utf8'))
    change(report)
    await writeFile(file, JSON.stringify(report))
    const record = value.manifest.assets.find(asset => asset.name === macSigningReportName)
    record.size = (await readFile(file)).length
    record.sha256 = await digest(file)
    await value.updateManifest(() => {})
    await assert.rejects(createDraft(value.directory, value.options), /non-rejected|must accurately match/)
    assert.equal(value.remote.calls.length, 0)
  }
})

test('update feeds reject wrong architecture, mutable URLs, incorrect size or SHA-512 even with matching SHA256SUMS', async t => {
  for (const change of [
    data => { data.files[0].url = data.files[0].url.replace('win-x64', 'win-arm64') },
    data => { data.files[0].url = data.files[0].url.replace(/download\/v[0-9.]+\//, 'latest/download/') },
    data => { data.files[0].size++ },
    data => { data.files[0].sha512 = Buffer.alloc(64).toString('base64') },
  ]) {
    const value = await fixture(t)
    const file = path.join(value.directory, 'latest-x64.yml')
    const data = JSON.parse(await readFile(file, 'utf8'))
    change(data)
    await writeFile(file, JSON.stringify(data))
    const record = value.manifest.assets.find(asset => asset.name === 'latest-x64.yml')
    record.size = (await readFile(file)).length
    record.sha256 = await digest(file)
    await value.updateManifest(() => {})
    await assert.rejects(createDraft(value.directory, value.options), /does not match final platform\/architecture bytes/)
    assert.equal(value.remote.calls.length, 0)
  }
})

test('corrupt final bytes, manifest asset evidence and foreign notes fail before any API request', async t => {
  for (const change of [
    async value => { await writeFile(path.join(value.directory, distributionNames[0]), 'Changed bytes') },
    async value => { await value.updateManifest(manifest => { manifest.assets[0].sha256 = '0'.repeat(64) }) },
    async value => { await value.updateManifest(manifest => { manifest.assets[0].size++ }) },
    async value => { await writeFile(path.join(value.directory, 'RELEASE_NOTES.md'), 'Other source'); await value.refreshSums() },
  ]) {
    const value = await fixture(t)
    await change(value)
    await assert.rejects(createDraft(value.directory, value.options), /Checksum mismatch|Manifest asset inventory|notes do not match/)
    assert.equal(value.remote.calls.length, 0)
  }
})

test('upload errors, wrong remote digests and a moved tag keep the release unpublished', async t => {
  for (const settings of [{ failUpload: true }, { wrongUploadDigest: true }, { movedTag: true }]) {
    const value = await fixture(t, settings)
    await assert.rejects(createDraft(value.directory, value.options), /upload .*failed|does not match local bytes|tag no longer identifies/)
    assert.equal(value.remote.release.draft, true)
    assert.ok(value.remote.calls.every(call => call.method !== 'PATCH'))
  }
})

test('candidate defaults never publish and public mode does not promote a candidate manifest', async t => {
  for (const unsigned of [true, false]) {
    const value = await fixture(t, { candidate: true, unsigned })
    delete value.options.publicRelease
    delete value.options.environment.CI
    const release = await createDraft(value.directory, value.options)
    assert.equal(release.draft, true)
    assert.ok(value.remote.calls.every(call => call.method !== 'PATCH'))
    const publicAttempt = { ...value.options, publicRelease: true }
    await assert.rejects(createDraft(value.directory, publicAttempt), /Public releases require/)
    assert.ok(value.remote.calls.every(call => call.method !== 'PATCH'))
  }
  const value = await fixture(t)
  await assert.rejects(createDraft(value.directory, { ...value.options, publicRelease: false }), /public releases and incomplete signing claims are forbidden/)
  assert.equal(value.remote.calls.length, 0)
})
