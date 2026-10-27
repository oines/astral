// Test transport only. All URLs are fixed official releases; the installed App
// keeps its original signature, code and production update source.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, open, rm } from 'node:fs/promises'
import path from 'node:path'
import { desktopArtifactName, desktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { compareVersions } from './release-draft.mjs'
import { validateTag, validateVersion } from './release-version.mjs'

const repository = 'https://github.com/oines/astral'
const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const safeName = name => typeof name === 'string' && /^[A-Za-z0-9._-]+$/.test(name) && name !== '.' && name !== '..'

async function responseFor(url, request, timeout = 60_000) {
  const response = await request(url, {
    redirect: 'follow', credentials: 'omit', headers: { 'Cache-Control': 'no-cache' },
    signal: AbortSignal.timeout(timeout),
  })
  assert.ok(response.ok, `Published release download failed (HTTP ${response.status}).`)
  const final = new URL(response.url)
  assert.ok(final.protocol === 'https:' && !final.username && !final.password &&
    ['github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'].includes(final.hostname), 'Unexpected published asset download origin.')
  assert.ok(response.body, 'The published asset response has no body.')
  return response
}

async function smallFile(url, request) {
  const response = await responseFor(url, request)
  const chunks = []
  let size = 0
  for await (const chunk of response.body) {
    size += chunk.length
    assert.ok(size <= (1 << 20), 'Published release metadata exceeds its size budget.')
    chunks.push(chunk)
  }
  return Buffer.concat(chunks)
}

export async function readPublishedRelease(version, request = globalThis.fetch) {
  validateVersion(version)
  const base = `${repository}/releases/download/v${version}`
  const [sumBytes, manifestBytes] = await Promise.all([
    smallFile(`${base}/SHA256SUMS`, request), smallFile(`${base}/release-manifest.json`, request),
  ])
  const checksums = new Map()
  for (const line of sumBytes.toString('utf8').trim().split('\n')) {
    const match = /^([a-f0-9]{64}) {2}([A-Za-z0-9._-]+)$/.exec(line)
    assert.ok(match && safeName(match[2]) && !checksums.has(match[2]), 'Invalid or duplicate published SHA256SUMS entry.')
    checksums.set(match[2], match[1])
  }
  assert.equal(hash(manifestBytes), checksums.get('release-manifest.json'), 'Published manifest checksum mismatch.')
  const manifest = JSON.parse(manifestBytes)
  assert.ok(manifest.schema_version === 1 && manifest.purpose === 'release' && manifest.public_release_ready === true &&
    manifest.version === version && manifest.tag === `v${version}` && manifest.repository === 'oines/astral' &&
    /^[a-f0-9]{40}$/.test(manifest.source_sha), 'The published manifest does not identify the expected official public version.')
  assert.ok(Array.isArray(manifest.assets) && manifest.assets.length > 0, 'The published asset inventory is missing.')
  const assets = new Map()
  for (const asset of manifest.assets) {
    assert.ok(safeName(asset?.name) && !assets.has(asset.name) && Number.isSafeInteger(asset.size) && asset.size > 0 &&
      /^[a-f0-9]{64}$/.test(asset.sha256) && checksums.get(asset.name) === asset.sha256, 'The published asset inventory is inconsistent.')
    assets.set(asset.name, asset)
  }
  return { version, base, manifest, assets }
}

export async function downloadPublishedAsset(release, name, directory, request = globalThis.fetch) {
  assert.ok(safeName(name), 'Invalid published asset name.')
  const asset = release.assets.get(name)
  assert.ok(asset && asset.size <= (2 ** 31), `Published asset ${name} is missing or too large.`)
  const response = await responseFor(`${release.base}/${name}`, request, 10 * 60_000)
  await mkdir(directory, { recursive: true, mode: 0o700 })
  const file = path.join(directory, name)
  const output = await open(file, 'wx', 0o600)
  const digest = createHash('sha256')
  let size = 0
  try {
    for await (const chunk of response.body) {
      size += chunk.length
      assert.ok(size <= asset.size, `Published asset ${name} exceeds its declared size.`)
      digest.update(chunk)
      await output.writeFile(chunk)
    }
    assert.equal(size, asset.size, `Published asset ${name} is incomplete.`)
    assert.equal(digest.digest('hex'), asset.sha256, `Published asset ${name} checksum mismatch.`)
    await output.close()
    return file
  } catch (error) {
    await output.close().catch(() => {})
    await rm(file, { force: true })
    throw error
  }
}

export async function publishedUpdateInputs({ fromTag, version, sourceSHA, platform, architecture, directory, request = globalThis.fetch }) {
  assert.ok(typeof fromTag === 'string' && fromTag.startsWith('v'), 'Pass an existing vMAJOR.MINOR.PATCH release tag.')
  const fromVersion = validateVersion(fromTag.slice(1))
  validateTag(fromTag, fromVersion)
  validateVersion(version)
  assert.ok(compareVersions(version, fromVersion) > 0, 'The published source version must be older than the target.')
  assert.ok(['mac', 'win', 'linux'].includes(platform) && ['arm64', 'x64'].includes(architecture), 'Unsupported published update platform.')
  const [previous, target] = await Promise.all([readPublishedRelease(fromVersion, request), readPublishedRelease(version, request)])
  assert.match(sourceSHA, /^[a-f0-9]{40}$/, 'The checked-out release source SHA is required.')
  assert.equal(target.manifest.source_sha, sourceSHA, 'The published target does not identify this exact checked-out release source.')
  const targetName = desktopArtifactName(version, platform, architecture, { mac: 'zip', win: 'exe', linux: 'AppImage' }[platform])
  const feedName = desktopUpdateFeeds(version).find(feed => feed.asset === targetName)?.name
  assert.ok(feedName && target.assets.has(targetName), 'The published target package or update feed is missing.')
  const feedBytes = await smallFile(`${repository}/releases/latest/download/${feedName}`, request)
  const feed = JSON.parse(feedBytes)
  assert.equal(feed.version, version, 'Official Latest does not identify this release; do not test a different target.')
  assert.equal(hash(feedBytes), target.assets.get(feedName)?.sha256, 'Official Latest feed differs from the published target bytes.')
  assert.equal(feed.files?.length, 1, 'The published update feed has an ambiguous asset inventory.')
  assert.equal(feed.files[0].url, `${target.base}/${targetName}`, 'The published updater target is not the exact official versioned asset.')
  assert.equal(feed.files[0].size, target.assets.get(targetName).size, 'The published updater target size is inconsistent.')
  assert.match(feed.files[0].sha512, /^[A-Za-z0-9+/]{86}==$/, 'The published updater checksum is invalid.')
  const previousName = desktopArtifactName(fromVersion, platform, architecture, { mac: 'zip', win: 'exe', linux: 'AppImage' }[platform])
  const oldPackage = await downloadPublishedAsset(previous, previousName, directory, request)
  // Mac and Windows compare their installed ASAR against the verified published
  // ZIP. Linux can compare its replaced AppImage directly to the public SHA256.
  const expectedPackage = platform === 'linux' ? undefined : await downloadPublishedAsset(target, desktopArtifactName(version, platform, architecture, 'zip'), directory, request)
  return { previous, target, fromVersion, oldPackage, expectedPackage, targetName }
}
