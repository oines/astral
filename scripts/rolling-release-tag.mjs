import { isDirectInvocation } from './release-version.mjs'
import { verifyReleaseBundle } from './release-draft.mjs'

function requireValue(value, message) { if (!value) throw new Error(message) }
export function releaseTagMessage(manifest, sealSHA256) {
  return `Astral ${manifest.version}\n\nSource: ${manifest.source_sha}\nRelease index SHA256: ${sealSHA256}\nBuild: ${manifest.build_provenance.build_run_url}\nPackaging: ${manifest.packaging_run_url}\n`
}

export async function createReleaseTag(directory, { environment = process.env, fetch: request = globalThis.fetch, verify = verifyReleaseBundle } = {}) {
  const token = environment.GH_TOKEN || environment.GITHUB_TOKEN
  requireValue(token && (environment.GITHUB_API_URL || 'https://api.github.com') === 'https://api.github.com', 'A job-scoped github.com publication token is required.')
  const { manifest, repository, sealSHA256 } = await verify(directory, { environment, requireSeal: true })
  requireValue(manifest.build_provenance && /^[a-f0-9]{64}$/.test(sealSHA256) && environment.ASTRAL_RELEASE_BUNDLE_SHA256 === sealSHA256, 'Automatic tags require a verified rolling release bundle and producer seal.')
  const message = releaseTagMessage(manifest, sealSHA256)
  async function api(route, { method = 'GET', body, allowMissing = false, allowConflict = false } = {}) {
    const response = await request(`https://api.github.com/repos/${repository}${route}`, {
      method, redirect: 'error', signal: AbortSignal.timeout(60_000),
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2022-11-28' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    if (allowMissing && response.status === 404 || allowConflict && response.status === 422) return undefined
    requireValue(response.ok, `Release tag API failed (HTTP ${response.status}).`)
    return response.json()
  }
  function verifyObject(value) {
    requireValue(value?.tag === manifest.tag && value.object?.type === 'commit' && value.object.sha === manifest.source_sha && value.message === message && /^[a-f0-9]{40}$/.test(value.sha || ''), 'An existing or returned tag object differs from the verified source, release index or provenance; tags will not be moved.')
    return value.sha
  }
  async function checkReference(reference) {
    requireValue(reference?.ref === `refs/tags/${manifest.tag}` && reference.object?.type === 'tag' && /^[a-f0-9]{40}$/.test(reference.object.sha || ''), 'The release tag must be an exact annotated source tag; lightweight or unrelated tags will not be moved.')
    const object = await api(`/git/tags/${reference.object.sha}`)
    requireValue(verifyObject(object) === reference.object.sha, 'The release tag reference differs from its annotated object.')
    return { tag: manifest.tag, source_sha: manifest.source_sha, tag_object_sha: object.sha, created: false }
  }
  const route = `/git/ref/tags/${encodeURIComponent(manifest.tag)}`
  const existing = await api(route, { allowMissing: true })
  if (existing) return checkReference(existing)
  const object = await api('/git/tags', { method: 'POST', body: { tag: manifest.tag, message, object: manifest.source_sha, type: 'commit', tagger: { name: 'Astral Release Automation', email: '41898282+github-actions[bot]@users.noreply.github.com' } } })
  const objectSHA = verifyObject(object)
  const reference = await api('/git/refs', { method: 'POST', body: { ref: `refs/tags/${manifest.tag}`, sha: objectSHA }, allowConflict: true })
  // A concurrent writer may have created this same tag. Verify it rather than
  // forcing or moving any reference, including when a previous reply was lost.
  if (!reference) return checkReference(await api(route))
  requireValue(reference.ref === `refs/tags/${manifest.tag}` && reference.object?.type === 'tag' && reference.object.sha === objectSHA, 'GitHub returned another tag reference; no tag movement was requested.')
  return { tag: manifest.tag, source_sha: manifest.source_sha, tag_object_sha: objectSHA, created: true }
}

if (await isDirectInvocation(import.meta.url)) {
  if (process.argv.length !== 3) { console.error('Usage: rolling-release-tag.mjs SEALED_DIR'); process.exitCode = 1 }
  else createReleaseTag(process.argv[2]).then(value => console.log(JSON.stringify(value))).catch(error => { console.error(error.message); process.exitCode = 1 })
}
