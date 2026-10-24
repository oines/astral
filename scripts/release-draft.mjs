import { createReadStream } from 'node:fs'
import { readdir, readFile, stat } from 'node:fs/promises'
import path from 'node:path'
import { digest, desktopNames, distributionNames, macSigningReportName, validateMacSigningReport } from './release-assets.mjs'
import { isDirectInvocation, readReleaseVersion, validateTag } from './release-version.mjs'

// This helper can only create or resume review-only Draft Releases. It has no publish path.
async function createDraft(directory) {
  const token = process.env.GH_TOKEN || process.env.GITHUB_TOKEN
  if (!token) throw new Error('A job-scoped GITHUB_TOKEN with contents:write is required.')
  const repository = process.env.GITHUB_REPOSITORY
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '')) throw new Error('GITHUB_REPOSITORY is required.')
  const manifest = JSON.parse(await readFile(path.join(directory, 'release-manifest.json'), 'utf8'))
  const version = await readReleaseVersion()
  validateTag(manifest.tag, version)
  const macSigned = manifest.purpose === 'mac-signed-candidate'
  if (manifest.schema_version !== 1 || manifest.version !== version || manifest.repository !== repository || !['unsigned-candidate', 'mac-signed-candidate'].includes(manifest.purpose) || manifest.public_release_ready !== false || !/^[a-f0-9]{40}$/.test(manifest.source_sha) ||
      manifest.signing?.macos !== (macSigned ? 'signed-and-notarized' : 'not-signed-or-notarized') || manifest.signing?.windows !== 'not-signed' || manifest.signing?.linux !== 'not-signed' ||
      manifest.migration_status !== 'not-implemented' || manifest.desktop_updater !== 'not-implemented') throw new Error('Only matching review-only candidate manifests can create a draft; public releases and incomplete signing claims are forbidden.')
  const requiredNames = [...distributionNames, ...desktopNames(version), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'checksums.txt', 'release-manifest.json', 'RELEASE_NOTES.md', ...(macSigned ? [macSigningReportName] : [])]
  const sumLines = (await readFile(path.join(directory, 'SHA256SUMS'), 'utf8')).trim().split('\n')
  const sums = new Map()
  for (const line of sumLines) {
    const match = /^([a-f0-9]{64}) {2}([A-Za-z0-9._-]+)$/.exec(line)
    if (!match || sums.has(match[2])) throw new Error('Invalid or duplicate SHA256SUMS entry.')
    sums.set(match[2], match[1])
  }
  if (sums.size !== requiredNames.length || requiredNames.some(name => !sums.has(name))) throw new Error('Checksum inventory does not describe the complete candidate.')
  const files = (await readdir(directory)).sort()
  if (files.length !== requiredNames.length + 1 || files.some(name => name !== 'SHA256SUMS' && !sums.has(name))) throw new Error('Unexpected or missing files in candidate upload directory.')
  for (const [name, expected] of sums) {
    if (await digest(path.join(directory, name)) !== expected) throw new Error(`Checksum mismatch before uploading ${name}.`)
  }
  if (macSigned) await validateMacSigningReport(JSON.parse(await readFile(path.join(directory, macSigningReportName), 'utf8')), { version, sourceSHA: manifest.source_sha, files: new Map(requiredNames.map(name => [name, path.join(directory, name)])) })
  const expectedAssets = new Map()
  for (const name of files) expectedAssets.set(name, { size: (await stat(path.join(directory, name))).size, digest: `sha256:${sums.get(name) || await digest(path.join(directory, name))}` })
  const apiBase = process.env.GITHUB_API_URL || 'https://api.github.com'
  if (apiBase !== 'https://api.github.com') throw new Error('This candidate publisher is configured only for github.com.')
  const headers = { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'Astral-release-candidate' }
  async function api(route, options = {}) {
    const response = await fetch(`${apiBase}/repos/${repository}${route}`, { ...options, headers: { ...headers, 'Content-Type': 'application/json', ...options.headers } })
    if (response.status === 404 && options.allowMissing) return undefined
    if (!response.ok) throw new Error(`GitHub API ${options.method || 'GET'} ${route} failed (HTTP ${response.status}).`)
    return response.json()
  }
  function verifyAsset(asset) {
    const expected = expectedAssets.get(asset.name)
    if (!expected || asset.size !== expected.size || asset.digest !== expected.digest || asset.state !== 'uploaded') {
      throw new Error(`Draft asset ${asset.name || '(unnamed)'} is unexpected or its name/size/digest/state does not match local bytes; explicitly remove the bad draft asset before retrying.`)
    }
  }
  async function listAssets(id) {
    const assets = await api(`/releases/${id}/assets?per_page=100`)
    if (!Array.isArray(assets) || assets.length >= 100 || assets.length > files.length || new Set(assets.map(asset => asset.name)).size !== assets.length) {
      throw new Error('Draft contains an unexpected or ambiguous asset inventory; refusing to upload.')
    }
    for (const asset of assets) verifyAsset(asset)
    return assets
  }
  let tagObject = (await api(`/git/ref/tags/${encodeURIComponent(manifest.tag)}`)).object
  for (let depth = 0; tagObject.type === 'tag' && depth < 8; depth++) tagObject = (await api(`/git/tags/${tagObject.sha}`)).object
  if (tagObject.type !== 'commit' || tagObject.sha !== manifest.source_sha) throw new Error('The existing release tag no longer identifies the validated source commit.')
  const marker = `<!-- astral-${manifest.purpose}:${manifest.source_sha} -->`
  const notes = await readFile(path.join(directory, 'RELEASE_NOTES.md'), 'utf8')
  if (!notes.includes(marker)) throw new Error('Candidate release notes do not match the source.')
  let release = await api(`/releases/tags/${encodeURIComponent(manifest.tag)}`, { allowMissing: true })
  if (release) {
    if (!release.draft || !release.body?.includes(marker) || release.target_commitish !== manifest.source_sha) throw new Error('Refusing to replace a published release or an unrelated draft.')
  } else release = await api('/releases', {
    method: 'POST',
    body: JSON.stringify({ tag_name: manifest.tag, target_commitish: manifest.source_sha, name: `Astral ${version} — ${macSigned ? 'Mac signed candidate' : 'unsigned candidate'}`, body: notes, draft: true, prerelease: false, make_latest: 'false' }),
  })
  let existingAssets = await listAssets(release.id)
  for (const name of files) {
    release = await api(`/releases/${release.id}`)
    if (!release.draft || !release.body?.includes(marker)) throw new Error('Release is no longer the expected draft. Upload stopped.')
    const file = path.join(directory, name)
    const existing = existingAssets.find(asset => asset.name === name)
    if (existing) continue
    const uploadURL = new URL(release.upload_url.replace(/\{.*$/, ''))
    if (uploadURL.protocol !== 'https:' || uploadURL.hostname !== 'uploads.github.com') throw new Error('Unexpected GitHub upload origin.')
    uploadURL.searchParams.set('name', name)
    const response = await fetch(uploadURL, { method: 'POST', headers: { ...headers, 'Content-Type': 'application/octet-stream', 'Content-Length': String((await stat(file)).size) }, body: createReadStream(file), duplex: 'half' })
    if (!response.ok) throw new Error(`Candidate upload ${name} failed (HTTP ${response.status}); the partial release remains a draft.`)
    const uploaded = await response.json()
    if (uploaded.name !== name) throw new Error(`GitHub returned an unexpected uploaded asset name for ${name}.`)
    verifyAsset(uploaded)
    existingAssets = [...existingAssets, uploaded]
  }
  release = await api(`/releases/${release.id}`)
  if (!release.draft || !release.body?.includes(marker)) throw new Error('Release changed state while uploading. No publish operation was requested by this helper.')
  const finalAssets = await listAssets(release.id)
  if (finalAssets.length !== files.length || files.some(name => !finalAssets.some(asset => asset.name === name))) throw new Error('Final draft inventory is incomplete; no publish operation was requested.')
  console.log(`Review-only ${macSigned ? 'Mac signed' : 'unsigned'} candidate remains a Draft Release: ${release.html_url}`)
}

if (await isDirectInvocation(import.meta.url)) {
  const args = process.argv.slice(2)
  if (args.length !== 1) { console.error('Usage: release-draft.mjs CANDIDATE_DIRECTORY'); process.exitCode = 1 }
  else createDraft(args[0]).catch(error => { console.error(error.message); process.exitCode = 1 })
}
