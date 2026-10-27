import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { isDirectInvocation } from './release-version.mjs'
import { digestPattern, imageRepository, lockPath, platforms, recipeHash, recipePath, validateImageConfig, validateLock } from './computer-image-contract.mjs'
export { imageRepository, lockPath, platforms, recipeHash, recipePath, validateImageConfig, validateLock } from './computer-image-contract.mjs'

const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const accept = 'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'

// This reader never pulls layers or uses Docker's cached login. Release checks
// are anonymous even on an authenticated runner. Authentication is available
// only to the publication command while bootstrapping a private GHCR package.
export async function registryReader({ authenticated = false, request = globalThis.fetch, environment = process.env } = {}) {
  const headers = {}
  if (authenticated) {
    if (!environment.GHCR_USERNAME || !environment.GHCR_TOKEN) throw new Error('Publishing requires the job-scoped GHCR username and token.')
    headers.Authorization = `Basic ${Buffer.from(`${environment.GHCR_USERNAME}:${environment.GHCR_TOKEN}`).toString('base64')}`
  }
  const tokenResponse = await request('https://ghcr.io/token?service=ghcr.io&scope=repository:oines/astral-computer:pull', { headers, redirect: 'error', signal: AbortSignal.timeout(30_000) })
  if (!tokenResponse.ok) throw new Error(`GHCR ${authenticated ? 'publication' : 'anonymous'} access failed (HTTP ${tokenResponse.status}). Make the package public before accepting its lock.`)
  const token = (await tokenResponse.json()).token
  if (typeof token !== 'string' || !token) throw new Error('GHCR did not provide a pull token.')
  const read = async (kind, reference) => {
    if (kind === 'blobs' && !digestPattern.test(reference) || kind === 'manifests' && !digestPattern.test(reference) && !/^env-[a-f0-9]{12}-[1-9][0-9]*-[1-9][0-9]*$/.test(reference)) throw new Error('Invalid environment registry reference.')
    let response = await request(`https://ghcr.io/v2/oines/astral-computer/${kind}/${reference}`, {
      headers: { Authorization: `Bearer ${token}`, Accept: accept }, redirect: 'manual', signal: AbortSignal.timeout(30_000),
    })
    // GHCR config blobs redirect to its signed GitHub CDN URL. Follow only this
    // observed HTTPS host, never forwarding the GHCR bearer token or printing
    // the signed URL. Manifests and token endpoints may not redirect.
    for (let hop = 0; [301, 302, 303, 307, 308].includes(response.status); hop++) {
      let target
      try { target = new URL(response.headers.get('location')) }
      catch { throw new Error('Invalid environment registry redirect.') }
      if (kind !== 'blobs' || hop >= 2 || target.protocol !== 'https:' || target.hostname !== 'pkg-containers.githubusercontent.com' || target.username || target.password || target.port && target.port !== '443') throw new Error('Untrusted environment registry redirect.')
      response = await request(target.href, { headers: { Accept: 'application/octet-stream' }, redirect: 'manual', signal: AbortSignal.timeout(30_000) })
    }
    if (response.status === 404) throw Object.assign(new Error('Environment image reference does not exist.'), { code: 'NOT_FOUND' })
    if (!response.ok) throw new Error(`GHCR ${authenticated ? 'publication' : 'anonymous'} ${kind} lookup failed (HTTP ${response.status}).`)
    const bytes = Buffer.from(await response.arrayBuffer())
    if (bytes.length > 4 << 20) throw new Error('Unexpectedly large environment registry metadata.')
    const digest = `sha256:${hash(bytes)}`
    if (digestPattern.test(reference) && reference !== digest || response.headers.get('docker-content-digest') && response.headers.get('docker-content-digest') !== digest) throw new Error('Registry content does not match its immutable digest.')
    return { digest, size: bytes.length, value: JSON.parse(bytes.toString()) }
  }
  return { manifest: reference => read('manifests', reference), config: reference => read('blobs', reference) }
}

export async function inspectPublishedImage(reader, reference, recipeSHA, sourceSHA) {
  const index = await reader.manifest(reference)
  const entries = index.value?.manifests
  if (index.value?.schemaVersion !== 2 || !Array.isArray(entries) || entries.length !== 2 ||
      entries.map(entry => `${entry.platform?.os}/${entry.platform?.architecture}`).sort().join(',') !== platforms.join(',')) throw new Error('Environment manifest must contain exactly native linux/amd64 and linux/arm64 images.')
  const records = []
  for (const entry of entries) {
    if (!digestPattern.test(entry.digest || '')) throw new Error('Environment platform descriptor is not pinned by digest.')
    const manifest = await reader.manifest(entry.digest)
    if (entry.size !== manifest.size || manifest.value?.schemaVersion !== 2 || !digestPattern.test(manifest.value.config?.digest || '')) throw new Error('Invalid environment platform manifest.')
    const config = await reader.config(manifest.value.config.digest)
    validateImageConfig(config.value, entry.platform.architecture, recipeSHA, sourceSHA)
    records.push({ architecture: entry.platform.architecture, digest: entry.digest })
  }
  return { digest: index.digest, records }
}

export async function verifyEnvironmentLock(lock, recipeSHA, options = {}) {
  validateLock(lock, recipeSHA)
  // Deliberately ignore GHCR credentials for this public distribution gate.
  const reader = await registryReader({ ...options, authenticated: false })
  const inspected = await inspectPublishedImage(reader, lock.digest, recipeSHA, lock.source_sha)
  if ((await reader.manifest(lock.tag)).digest !== inspected.digest) throw new Error('Immutable environment tag no longer matches its locked digest.')
  return inspected
}

export async function publishEnvironment(recordsDirectory, output) {
  const recipeSHA = recipeHash(await readFile(recipePath))
  const records = await Promise.all(['amd64', 'arm64'].map(async architecture => JSON.parse(await readFile(path.join(recordsDirectory, `platform-${architecture}.json`), 'utf8'))))
  for (const [index, record] of records.entries()) {
    if (record.image !== imageRepository || record.architecture !== ['amd64', 'arm64'][index] || !digestPattern.test(record.digest || '') ||
        record.recipe_sha256 !== recipeSHA || record.source_sha !== process.env.GITHUB_SHA || record.run_id !== process.env.GITHUB_RUN_ID ||
        !Number.isSafeInteger(record.attempt) || record.attempt < 1 || record.native_smoke !== true || record.published_smoke !== true) throw new Error('Both native architectures must pass the exact published-image smoke test before creating a manifest.')
  }
  const tag = `env-${recipeSHA.slice(0, 12)}-${process.env.GITHUB_RUN_ID}-${Math.max(...records.map(record => record.attempt))}`
  const reader = await registryReader({ authenticated: true })
  let existing
  try { existing = await reader.manifest(tag) }
  catch (error) { if (error.code !== 'NOT_FOUND') throw error }
  if (!existing) execFileSync('docker', ['buildx', 'imagetools', 'create', '--tag', `${imageRepository}:${tag}`, ...records.map(record => `${imageRepository}@${record.digest}`)], { stdio: 'inherit', timeout: 120_000 })
  const published = await inspectPublishedImage(reader, tag, recipeSHA, process.env.GITHUB_SHA)
  if (published.records.some(record => !records.some(expected => expected.architecture === record.architecture && expected.digest === record.digest))) throw new Error('Refusing to replace or accept a different image under an immutable environment tag.')
  const lock = validateLock({ schema_version: 1, image: imageRepository, tag, digest: published.digest, recipe_sha256: recipeSHA, platforms, source_sha: process.env.GITHUB_SHA, build_run_url: `https://github.com/oines/astral/actions/runs/${process.env.GITHUB_RUN_ID}`, created_at: new Date().toISOString() }, recipeSHA)
  await writeFile(output, `${JSON.stringify(lock, null, 2)}\n`, { flag: 'wx' })
  return lock
}

if (await isDirectInvocation(import.meta.url)) {
  try {
    const [operation, ...args] = process.argv.slice(2)
    const option = name => { const index = args.indexOf(name); return index < 0 ? undefined : args[index + 1] }
    const recipeSHA = recipeHash(await readFile(recipePath))
    if (operation === 'recipe' && args.length === 0) console.log(recipeSHA)
    else if (operation === 'verify') {
      const lock = JSON.parse(await readFile(option('--lock') || lockPath, 'utf8'))
      await verifyEnvironmentLock(lock, recipeSHA)
      console.log(`Public daemon-free environment verified: ${lock.image}@${lock.digest} (${lock.platforms.join(', ')}).`)
    } else if (operation === 'publish' && option('--records') && option('--output')) console.log(JSON.stringify(await publishEnvironment(option('--records'), option('--output'))))
    else throw new Error('Usage: computer-image.mjs recipe | verify [--lock FILE] | publish --records DIR --output FILE')
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
