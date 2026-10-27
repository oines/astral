import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { desktopArtifactName, desktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { downloadPublishedAsset, publishedUpdateInputs, readPublishedRelease } from './published-desktop-update.mjs'

const repository = 'https://github.com/oines/astral'
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const sourceSHA = 'a'.repeat(40)

function release(version, files, change = () => {}) {
  const base = `${repository}/releases/download/v${version}`
  const assets = [...files].map(([name, bytes]) => ({ name, size: bytes.length, sha256: sha256(bytes) }))
  const manifest = { schema_version: 1, purpose: 'release', public_release_ready: true, version, tag: `v${version}`, source_sha: sourceSHA, repository: 'oines/astral', assets: assets.map(asset => ({ ...asset })) }
  change(manifest)
  const bytes = Buffer.from(JSON.stringify(manifest))
  const sums = [...assets.map(asset => `${asset.sha256}  ${asset.name}`), `${sha256(bytes)}  release-manifest.json`].join('\n')
  return new Map([[`${base}/SHA256SUMS`, Buffer.from(sums)], [`${base}/release-manifest.json`, bytes], ...[...files].map(([name, value]) => [`${base}/${name}`, value])])
}

function transport(files) {
  const requested = []
  const request = async (url, options) => {
    requested.push(url)
    assert.equal(options.credentials, 'omit')
    assert.ok(url.startsWith(`${repository}/releases/`), 'Tests must not need arbitrary API or download URLs')
    const response = new globalThis.Response(files.get(url), { status: files.has(url) ? 200 : 404 })
    Object.defineProperty(response, 'url', { value: url })
    return response
  }
  return { request, requested }
}

async function directory(t) {
  const target = await mkdtemp(path.join(tmpdir(), 'astral-published-update-test-'))
  t.after(() => rm(target, { recursive: true, force: true }))
  return target
}

function inputs(platform = 'linux', architecture = 'x64', feedChange = () => {}) {
  const oldName = desktopArtifactName('0.1.0', platform, architecture, { mac: 'zip', win: 'exe', linux: 'AppImage' }[platform])
  const targetName = desktopArtifactName('0.1.1', platform, architecture, { mac: 'zip', win: 'exe', linux: 'AppImage' }[platform])
  const feedName = desktopUpdateFeeds('0.1.1').find(feed => feed.asset === targetName).name
  const targetBytes = Buffer.from('the immutable new package')
  const feed = { version: '0.1.1', files: [{ url: `${repository}/releases/download/v0.1.1/${targetName}`, size: targetBytes.length, sha512: createHash('sha512').update(targetBytes).digest('base64') }] }
  feedChange(feed)
  const feedBytes = Buffer.from(JSON.stringify(feed))
  const targetFiles = new Map([[targetName, targetBytes], [feedName, feedBytes]])
  if (platform === 'win') targetFiles.set(desktopArtifactName('0.1.1', platform, architecture, 'zip'), Buffer.from('expected published ZIP'))
  const files = new Map([
    ...release('0.1.0', new Map([[oldName, Buffer.from('the untouched old package')]])),
    ...release('0.1.1', targetFiles), [`${repository}/releases/latest/download/${feedName}`, feedBytes],
  ])
  return { files, oldName, targetName, ...transport(files) }
}

test('public metadata comes from fixed official tag URLs and is checked against SHA256SUMS', async () => {
  const files = release('0.1.0', new Map([['fixture.zip', Buffer.from('old')]]))
  const client = transport(files)
  const value = await readPublishedRelease('0.1.0', client.request)
  assert.equal(value.manifest.source_sha, sourceSHA)
  assert.equal(value.assets.get('fixture.zip').size, 3)
  assert.equal(client.requested.length, 2)
  files.set(`${repository}/releases/download/v0.1.0/release-manifest.json`, Buffer.from('{}'))
  await assert.rejects(readPublishedRelease('0.1.0', client.request), /manifest checksum mismatch/)
})

test('a checksummed candidate, different source claims and ambiguous assets are not accepted as the old public release', async t => {
  for (const [name, change] of [
    ['candidate', value => { value.purpose = 'unsigned-candidate' }],
    ['not ready', value => { value.public_release_ready = false }],
    ['wrong version', value => { value.version = '0.1.1' }],
    ['other repository', value => { value.repository = 'other/astral' }],
    ['invalid source', value => { value.source_sha = 'not-a-commit' }],
    ['duplicate asset', value => { value.assets.push(value.assets[0]) }],
    ['empty asset', value => { value.assets[0].size = 0 }],
  ]) await t.test(name, async () => {
    const files = release('0.1.0', new Map([['fixture.zip', Buffer.from('old')]]), change)
    await assert.rejects(readPublishedRelease('0.1.0', transport(files).request), /official public version|inventory is inconsistent/)
  })
})

test('completed old-package downloads require exact size and digest; rejected partial files are removed', async t => {
  const target = await directory(t)
  const files = release('0.1.0', new Map([['fixture.zip', Buffer.from('correct')]]))
  const client = transport(files)
  const record = await readPublishedRelease('0.1.0', client.request)
  const downloaded = await downloadPublishedAsset(record, 'fixture.zip', target, client.request)
  assert.equal(await readFile(downloaded, 'utf8'), 'correct')
  await rm(downloaded)
  for (const bad of ['changed', 'short', 'much longer than declared']) {
    files.set(`${record.base}/fixture.zip`, Buffer.from(bad))
    await assert.rejects(downloadPublishedAsset(record, 'fixture.zip', target, client.request), /checksum mismatch|incomplete|exceeds/)
    assert.deepEqual(await readdir(target), [])
  }
  await assert.rejects(downloadPublishedAsset(record, '../fixture.zip', target, client.request), /Invalid published asset name/)
})

test('real old packages and expected installed contents use the native immutable asset names on all platforms', async t => {
  for (const platform of ['mac', 'win', 'linux']) for (const architecture of ['x64', 'arm64']) await t.test(`${platform}/${architecture}`, async t => {
    const target = await directory(t)
    const client = inputs(platform, architecture)
    const value = await publishedUpdateInputs({ fromTag: 'v0.1.0', version: '0.1.1', sourceSHA, platform, architecture, directory: target, request: client.request })
    assert.equal(path.basename(value.oldPackage), client.oldName)
    assert.equal(await readFile(value.oldPackage, 'utf8'), 'the untouched old package')
    assert.equal(value.targetName, client.targetName)
    if (platform === 'linux') assert.equal(value.expectedPackage, undefined)
    else assert.equal(path.basename(value.expectedPackage), desktopArtifactName('0.1.1', platform, architecture, 'zip'))
    assert.ok(client.requested.every(url => url.startsWith('https://github.com/oines/astral/releases/')))
  })
})

test('equal versions, different release source, stale Latest and foreign updater targets fail before old-package installation', async t => {
  const target = await directory(t)
  for (const [name, change, options, error] of [
    ['equal source', undefined, { fromTag: 'v0.1.1' }, /must be older/],
    ['wrong source', undefined, { sourceSHA: 'b'.repeat(40) }, /exact checked-out release source/],
    ['stale Latest', value => { value.version = '0.1.0' }, {}, /Latest does not identify this release/],
    ['foreign URL', value => { value.files[0].url = value.files[0].url.replace('/oines/', '/other/') }, {}, /exact official versioned asset/],
    ['wrong size', value => { value.files[0].size++ }, {}, /size is inconsistent/],
  ]) await t.test(name, async () => {
    const client = inputs('linux', 'x64', change)
    await assert.rejects(publishedUpdateInputs({ fromTag: 'v0.1.0', version: '0.1.1', sourceSHA, platform: 'linux', architecture: 'x64', directory: target, request: client.request, ...options }), error)
    assert.ok(!client.requested.some(url => url.endsWith(client.oldName)), 'A bad target must fail before downloading the old application')
    assert.deepEqual(await readdir(target), [])
  })
})

test('the real published-update command refuses a developer account before any download or installation', () => {
  const environment = { ...process.env }
  for (const name of ['CI', 'GITHUB_ACTIONS', 'RUNNER_TEMP']) delete environment[name]
  const command = spawnSync(process.execPath, [fileURLToPath(new URL('./check-desktop-update.mjs', import.meta.url)), '--published-from', 'v0.1.0'], { env: environment, encoding: 'utf8', timeout: 10_000 })
  assert.equal(command.status, 1)
  assert.match(command.stderr, /requires a disposable GitHub Actions user; never run on a developer account/)
})
