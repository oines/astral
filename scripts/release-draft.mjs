import { createReadStream } from 'node:fs'
import { readdir, readFile, stat } from 'node:fs/promises'
import path from 'node:path'
import { digest, desktopNames, distributionNames, macSigningReportName, validateMacSigningReport } from './release-assets.mjs'
import { isDirectInvocation, readReleaseVersion, validateTag, validateVersion } from './release-version.mjs'

export function compareVersions(left, right) {
  const a = validateVersion(left).split('.').map(Number)
  const b = validateVersion(right).split('.').map(Number)
  for (let index = 0; index < a.length; index++) {
    if (a[index] !== b[index]) return a[index] > b[index] ? 1 : -1
  }
  return 0
}

// Candidate mode always leaves a draft. Public mode is an explicit CI-only
// operation; publishing is its final mutation, after the complete asset check.
export async function createDraft(directory, { publicRelease = false, environment = process.env, fetch: request = globalThis.fetch, log = console.log } = {}) {
  if (typeof publicRelease !== 'boolean') throw new Error('Public release mode must be selected explicitly.')
  const token = environment.GH_TOKEN || environment.GITHUB_TOKEN
  if (!token) throw new Error('A job-scoped GITHUB_TOKEN with contents:write is required.')
  const repository = environment.GITHUB_REPOSITORY
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '')) throw new Error('GITHUB_REPOSITORY is required.')
  const manifest = JSON.parse(await readFile(path.join(directory, 'release-manifest.json'), 'utf8'))
  const version = await readReleaseVersion()
  validateTag(manifest.tag, version)
  const macSigned = publicRelease || manifest.purpose === 'mac-signed-candidate'
  const validPurpose = publicRelease ? manifest.purpose === 'release' && manifest.public_release_ready === true : ['unsigned-candidate', 'mac-signed-candidate'].includes(manifest.purpose) && manifest.public_release_ready === false
  if (manifest.schema_version !== 1 || manifest.version !== version || manifest.repository !== repository || !validPurpose || !/^[a-f0-9]{40}$/.test(manifest.source_sha) ||
      manifest.signing?.macos !== (macSigned ? 'signed-and-notarized' : 'not-signed-or-notarized') || manifest.signing?.windows !== 'not-signed' || manifest.signing?.linux !== 'not-signed' ||
      manifest.migration_status !== 'not-implemented' || manifest.desktop_updater !== 'not-implemented') throw new Error(publicRelease ? 'Public releases require a matching release manifest and complete Mac signing claims.' : 'Only matching review-only candidate manifests can create a draft; public releases and incomplete signing claims are forbidden.')
  if (publicRelease) {
    const runPrefix = `https://github.com/${repository}/actions/runs/`
    if (environment.CI !== 'true' || environment.GITHUB_ACTIONS !== 'true' || !/^\d+$/.test(environment.GITHUB_RUN_ID || '') ||
        (environment.GITHUB_SERVER_URL || 'https://github.com') !== 'https://github.com' ||
        manifest.candidate_run_url !== `${runPrefix}${environment.GITHUB_RUN_ID}` ||
        typeof manifest.ci_run_url !== 'string' || !manifest.ci_run_url.startsWith(runPrefix) || !/^\d+$/.test(manifest.ci_run_url.slice(runPrefix.length))) throw new Error('Public publishing requires this GitHub Actions run and its matching release/CI metadata.')
    if (manifest.host_schema !== 3 || manifest.core_format !== 10 || manifest.data_formats?.host !== 3 || manifest.data_formats?.core !== 10 || Object.keys(manifest.data_formats).length !== 2) throw new Error('Public releases currently support only the Host 3 / Core 10 data baseline.')
  }
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
  const manifestNames = requiredNames.filter(name => !['release-manifest.json', 'RELEASE_NOTES.md'].includes(name))
  if (!Array.isArray(manifest.assets) || manifest.assets.length !== manifestNames.length || new Set(manifest.assets.map(asset => asset?.name)).size !== manifestNames.length) throw new Error('Manifest asset inventory is incomplete or ambiguous.')
  for (const asset of manifest.assets) {
    if (!asset || !manifestNames.includes(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0 || sums.get(asset.name) !== asset.sha256 || (await stat(path.join(directory, asset.name))).size !== asset.size) throw new Error('Manifest asset inventory does not match the final size and SHA256 inventory.')
  }
  if (publicRelease && (!Array.isArray(manifest.distribution_names) || manifest.distribution_names.length !== distributionNames.length || new Set(manifest.distribution_names).size !== distributionNames.length || distributionNames.some(name => !manifest.distribution_names.includes(name)))) throw new Error('Public release executable inventory is incomplete.')
  if (macSigned) await validateMacSigningReport(JSON.parse(await readFile(path.join(directory, macSigningReportName), 'utf8')), { version, sourceSHA: manifest.source_sha, files: new Map(requiredNames.map(name => [name, path.join(directory, name)])) })
  const expectedAssets = new Map()
  for (const name of files) expectedAssets.set(name, { size: (await stat(path.join(directory, name))).size, digest: `sha256:${sums.get(name) || await digest(path.join(directory, name))}` })
  const apiBase = environment.GITHUB_API_URL || 'https://api.github.com'
  if (apiBase !== 'https://api.github.com') throw new Error('This candidate publisher is configured only for github.com.')
  const headers = { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'Astral-release-candidate' }
  async function api(route, options = {}) {
    let response
    try { response = await request(`${apiBase}/repos/${repository}${route}`, { ...options, redirect: 'error', headers: { ...headers, 'Content-Type': 'application/json', ...options.headers } }) }
    catch { throw new Error(`GitHub API ${options.method || 'GET'} ${route} could not complete.`) }
    if (response.status === 404 && options.allowMissing) return undefined
    if (!response.ok) throw new Error(`GitHub API ${options.method || 'GET'} ${route} failed (HTTP ${response.status}).`)
    return response.json()
  }
  async function* releasePages() {
    for (let page = 1; page <= 100; page++) {
      const releases = await api(`/releases?per_page=100&page=${page}`)
      if (!Array.isArray(releases) || releases.length > 100) throw new Error('Release inventory could not be verified.')
      yield releases
      if (releases.length < 100) return
    }
    throw new Error('Release inventory exceeds the verified pagination limit.')
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
  const marker = `<!-- astral-${manifest.purpose}:${manifest.source_sha} -->`
  const notes = await readFile(path.join(directory, 'RELEASE_NOTES.md'), 'utf8')
  if (!notes.includes(marker)) throw new Error('Candidate release notes do not match the source.')
  let tagObject = (await api(`/git/ref/tags/${encodeURIComponent(manifest.tag)}`)).object
  for (let depth = 0; tagObject.type === 'tag' && depth < 8; depth++) tagObject = (await api(`/git/tags/${tagObject.sha}`)).object
  if (tagObject.type !== 'commit' || tagObject.sha !== manifest.source_sha) throw new Error('The existing release tag no longer identifies the validated source commit.')
  function verifyRelease(value) {
    if (!value || !Number.isSafeInteger(value.id) || value.id <= 0 || value.tag_name !== manifest.tag || value.prerelease !== false || !value.body?.includes(marker) || value.target_commitish !== manifest.source_sha || (publicRelease && value.body !== notes)) throw new Error('Refusing to replace a release with unrelated source, tag or notes.')
  }
  function completeInventory(assets) {
    if (assets.length !== files.length || files.some(name => !assets.some(asset => asset.name === name))) throw new Error('Final draft inventory is incomplete; no publish operation was requested.')
  }
  let release = await api(`/releases/tags/${encodeURIComponent(manifest.tag)}`, { allowMissing: true })
  if (!release) {
    // GitHub's tag endpoint may expose only the published release. An upload
    // retry must also find an authenticated draft before creating anything.
    const matches = []
    for await (const releases of releasePages()) matches.push(...releases.filter(value => value?.tag_name === manifest.tag))
    if (matches.length > 1) throw new Error('Multiple releases use this tag; refusing an ambiguous draft retry.')
    if (matches.length === 1) {
      const id = matches[0].id
      if (!Number.isSafeInteger(id) || id <= 0) throw new Error('The matching draft has an invalid release ID.')
      release = await api(`/releases/${id}`)
      if (release?.id !== id) throw new Error('GitHub returned a different release for the matching draft ID.')
    }
  }
  if (release) {
    verifyRelease(release)
    if (release.draft !== true) {
      if (!publicRelease || release.draft !== false) throw new Error('Refusing to replace a published release or an unrelated draft.')
      completeInventory(await listAssets(release.id))
      log(`Matching release is already published; no changes were made: ${release.html_url}`)
      return release
    }
  } else release = await api('/releases', {
    method: 'POST',
    body: JSON.stringify({ tag_name: manifest.tag, target_commitish: manifest.source_sha, name: publicRelease ? `Astral ${version}` : `Astral ${version} — ${macSigned ? 'Mac signed candidate' : 'unsigned candidate'}`, body: notes, draft: true, prerelease: false, make_latest: 'false' }),
  })
  verifyRelease(release)
  if (release.draft !== true) throw new Error('The upload target is not the expected draft.')
  let existingAssets = await listAssets(release.id)
  for (const name of files) {
    release = await api(`/releases/${release.id}`)
    verifyRelease(release)
    if (release.draft !== true) throw new Error('Release is no longer the expected draft. Upload stopped.')
    const file = path.join(directory, name)
    const existing = existingAssets.find(asset => asset.name === name)
    if (existing) continue
    const uploadURL = new URL(release.upload_url.replace(/\{.*$/, ''))
    if (uploadURL.protocol !== 'https:' || uploadURL.hostname !== 'uploads.github.com') throw new Error('Unexpected GitHub upload origin.')
    uploadURL.searchParams.set('name', name)
    let response
    try { response = await request(uploadURL, { method: 'POST', redirect: 'error', headers: { ...headers, 'Content-Type': 'application/octet-stream', 'Content-Length': String((await stat(file)).size) }, body: createReadStream(file), duplex: 'half' }) }
    catch { throw new Error(`Candidate upload ${name} could not complete; the partial release remains a draft.`) }
    if (!response.ok) throw new Error(`Candidate upload ${name} failed (HTTP ${response.status}); the partial release remains a draft.`)
    const uploaded = await response.json()
    if (uploaded.name !== name) throw new Error(`GitHub returned an unexpected uploaded asset name for ${name}.`)
    verifyAsset(uploaded)
    existingAssets = [...existingAssets, uploaded]
  }
  release = await api(`/releases/${release.id}`)
  verifyRelease(release)
  if (release.draft !== true) throw new Error('Release changed state while uploading. No publish operation was requested by this helper.')
  const finalAssets = await listAssets(release.id)
  completeInventory(finalAssets)
  if (publicRelease) {
    let latest = true
    for await (const published of releasePages()) {
      for (const previous of published) {
        if (previous.draft !== false || previous.prerelease !== false) continue
        let previousVersion
        try { previousVersion = validateVersion(previous.tag_name?.replace(/^v/, '')) } catch { throw new Error('A published release has an unrecognized version; Latest was not changed.') }
        if (compareVersions(version, previousVersion) <= 0) latest = false
      }
    }
    // The workflow serializes public jobs across tags. Check the tag and draft
    // once more immediately before the sole publication mutation.
    let currentTag = (await api(`/git/ref/tags/${encodeURIComponent(manifest.tag)}`)).object
    for (let depth = 0; currentTag.type === 'tag' && depth < 8; depth++) currentTag = (await api(`/git/tags/${currentTag.sha}`)).object
    if (currentTag.type !== 'commit' || currentTag.sha !== manifest.source_sha) throw new Error('The existing release tag no longer identifies the validated source commit.')
    release = await api(`/releases/${release.id}`)
    verifyRelease(release)
    if (release.draft !== true) throw new Error('Release is no longer the expected draft; publication stopped.')
    completeInventory(await listAssets(release.id))
    release = await api(`/releases/${release.id}`, { method: 'PATCH', body: JSON.stringify({ draft: false, make_latest: latest ? 'true' : 'false' }) })
    verifyRelease(release)
    if (release.draft !== false) throw new Error('GitHub did not confirm publication; inspect the release before retrying.')
    log(`Published Astral ${version}; Latest=${latest}: ${release.html_url}`)
  } else log(`Review-only ${macSigned ? 'Mac signed' : 'unsigned'} candidate remains a Draft Release: ${release.html_url}`)
  return release
}

if (await isDirectInvocation(import.meta.url)) {
  const args = process.argv.slice(2)
  const publicRelease = args[0] === '--public'
  const directory = publicRelease ? args[1] : args[0]
  if (!directory || directory.startsWith('--') || args.length !== (publicRelease ? 2 : 1)) { console.error('Usage: release-draft.mjs [--public] RELEASE_DIRECTORY'); process.exitCode = 1 }
  else createDraft(directory, { publicRelease }).catch(error => { console.error(error.message); process.exitCode = 1 })
}
