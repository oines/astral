import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { appendFile, lstat, mkdir, mkdtemp, open, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { isDirectInvocation, repositoryRoot } from './release-version.mjs'
import { releaseWorkflowPath, validateBuildMetadata } from './rolling-release-version.mjs'

function requireValue(value, message) { if (!value) throw new Error(message) }
function positive(value, name) {
  requireValue(/^[1-9]\d*$/.test(String(value)) && Number.isSafeInteger(Number(value)), `${name} must be a positive safe integer.`)
  return Number(value)
}
function context(environment, request) {
  const repository = environment.GITHUB_REPOSITORY
  requireValue(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '') && !repository.split('/').some(part => part === '.' || part === '..'), 'GITHUB_REPOSITORY is required.')
  const token = environment.GH_TOKEN || environment.GITHUB_TOKEN
  requireValue(token && (environment.GITHUB_API_URL || 'https://api.github.com') === 'https://api.github.com', 'A job-scoped github.com token is required.')
  const api = async (route, raw = false) => {
    const response = await request(`https://api.github.com/repos/${repository}${route}`, {
      redirect: 'manual', signal: AbortSignal.timeout(60_000),
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
    })
    if (raw) return response
    requireValue(response.ok, `Release input API failed (HTTP ${response.status}).`)
    return response.json()
  }
  return { repository, api, request }
}
function sourceSHA(value) { requireValue(/^[a-f0-9]{40}$/.test(value || ''), 'A full release source SHA is required.'); return value }
function trustedRun(run, repository) {
  requireValue(run?.repository?.full_name === repository && run.head_repository?.full_name === repository &&
    run.path?.split('@')[0] === releaseWorkflowPath && run.head_branch === 'main' &&
    ['push', 'workflow_dispatch'].includes(run.event), 'The producer is not this repository\'s trusted main Prepare workflow.')
  sourceSHA(run.head_sha)
  for (const field of ['id', 'run_number', 'run_attempt']) positive(run[field], `Producer ${field}`)
}
function artifactIdentity(artifact, id, runID, expectedSHA) {
  requireValue(artifact?.id === id && artifact.expired === false && Number.isSafeInteger(artifact.size_in_bytes) && artifact.size_in_bytes > 0 &&
    /^sha256:[a-f0-9]{64}$/.test(artifact.digest || '') && artifact.workflow_run?.id === runID &&
    /^[a-f0-9]{40}$/.test(artifact.workflow_run?.head_sha || '') &&
    (expectedSHA === undefined || artifact.workflow_run.head_sha === expectedSHA), 'Artifact identity, source, expiry or immutable digest differs from the expected producer.')
  return artifact
}
function safeEntries(entries) {
  requireValue(Array.isArray(entries) && entries.length > 0, 'Release artifact ZIP is empty.')
  const names = new Set()
  for (const entry of entries) {
    const name = entry.name
    requireValue(typeof name === 'string' && !name.startsWith('/') && !name.includes('\\') && !name.includes(':') && ![...name].some(character => character.codePointAt(0) <= 31 || character.codePointAt(0) === 127), 'Unsafe release artifact ZIP path.')
    const parts = name.replace(/\/$/, '').split('/')
    requireValue(parts.every(part => /^[A-Za-z0-9_][A-Za-z0-9_. -]*$/.test(part) && !/[. ]$/.test(part) && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(part)), 'Unsafe release artifact ZIP path.')
    const normalized = parts.join('/').toLowerCase()
    requireValue(!names.has(normalized) && ['file', 'directory'].includes(entry.type), 'Release artifact ZIP contains a duplicate path, link or special file.')
    names.add(normalized)
  }
}
function inspectArchive(archive, platform, execute) {
  if (platform === 'win32') {
    const script = `$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.IO.Compression.FileSystem; $zip=[IO.Compression.ZipFile]::OpenRead($env:ASTRAL_ARCHIVE_ZIP); try { $items=@($zip.Entries | ForEach-Object { $kind=($_.ExternalAttributes -shr 16) -band 61440; @{name=$_.FullName; type=$(if ($kind -eq 40960 -or ($kind -ne 0 -and $kind -ne 32768 -and $kind -ne 16384)) {'special'} elseif ($_.FullName.EndsWith('/')) {'directory'} else {'file'})} }); ConvertTo-Json -InputObject $items -Compress } finally { $zip.Dispose() }`
    return JSON.parse(execute('pwsh', ['-NoProfile', '-NonInteractive', '-Command', script], { encoding: 'utf8', timeout: 60_000, maxBuffer: 16 << 20, env: { ...process.env, ASTRAL_ARCHIVE_ZIP: archive } }).replace(/^\uFEFF/, ''))
  }
  const listingNames = execute('unzip', ['-Z1', archive], { encoding: 'utf8', timeout: 60_000, maxBuffer: 16 << 20 })
  const names = (listingNames.endsWith('\n') ? listingNames.slice(0, -1) : listingNames).split('\n')
  const listing = execute('unzip', ['-Z', '-l', archive], { encoding: 'utf8', timeout: 60_000, maxBuffer: 16 << 20 })
  const types = listing.split('\n').flatMap(line => {
    const permission = /^([-dlbcps][rwxstST-]{9})\s/.exec(line)?.[1]
    return permission ? [permission[0] === '-' ? 'file' : permission[0] === 'd' ? 'directory' : 'special'] : []
  })
  requireValue(names.length === types.length, 'Release artifact ZIP entry types could not be verified.')
  return names.map((name, index) => ({ name, type: types[index] }))
}
async function emptyDestination(output) {
  try {
    const info = await lstat(output)
    requireValue(info.isDirectory() && !info.isSymbolicLink() && (await readdir(output)).length === 0, 'Artifact output must be a new empty directory.')
    return false
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
    await mkdir(output, { recursive: true, mode: 0o700 })
    return true
  }
}

export async function downloadArtifact({ artifactID, runID, output, expectedSHA, environment = process.env, fetch: request = globalThis.fetch, execute = execFileSync, platform = process.platform }) {
  const id = positive(artifactID, 'Artifact ID'), producer = positive(runID, 'Producer run ID')
  if (expectedSHA !== undefined) sourceSHA(expectedSHA)
  const ctx = context(environment, request)
  const artifact = artifactIdentity(await ctx.api(`/actions/artifacts/${id}`), id, producer, expectedSHA)
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-artifact-'))
  const archive = path.join(temporary, 'artifact.zip')
  let createdOutput = false
  try {
    let response = await ctx.api(`/actions/artifacts/${id}/zip`, true)
    for (let count = 0; response.status >= 300 && response.status < 400; count++) {
      requireValue(count < 5, 'Too many artifact download redirects.')
      const location = new URL(response.headers.get('location'))
      requireValue(location.protocol === 'https:' && !location.username && !location.password && !location.hash, 'Unsafe artifact download redirect.')
      // Credentials are deliberately sent only to the fixed GitHub API origin.
      response = await request(location.href, { redirect: 'manual', signal: AbortSignal.timeout(15 * 60_000) })
    }
    requireValue(response.ok && response.body, `Artifact archive download failed (HTTP ${response.status}).`)
    const handle = await open(archive, 'wx', 0o600)
    let size = 0
    const hash = createHash('sha256')
    try {
      for await (const chunk of response.body) {
        size += chunk.length
        requireValue(size <= artifact.size_in_bytes, 'Artifact archive exceeds its immutable declared size.')
        hash.update(chunk)
        await handle.writeFile(chunk)
      }
    } finally { await handle.close() }
    requireValue(size === artifact.size_in_bytes && `sha256:${hash.digest('hex')}` === artifact.digest, 'Artifact archive does not match its immutable GitHub SHA256 digest and size.')
    safeEntries(inspectArchive(archive, platform, execute))
    createdOutput = await emptyDestination(output)
    if (platform === 'win32') execute('pwsh', ['-NoProfile', '-NonInteractive', '-Command', "$ErrorActionPreference='Stop'; Expand-Archive -LiteralPath $env:ASTRAL_ARCHIVE_ZIP -DestinationPath $env:ASTRAL_ARCHIVE_OUTPUT"], { timeout: 15 * 60_000, stdio: 'pipe', env: { ...process.env, ASTRAL_ARCHIVE_ZIP: archive, ASTRAL_ARCHIVE_OUTPUT: path.resolve(output) } })
    else execute('unzip', ['-q', archive, '-d', output], { timeout: 15 * 60_000, stdio: 'pipe' })
    return artifact
  } catch (error) {
    if (createdOutput) await rm(output, { recursive: true, force: true })
    throw error
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

export async function resolveInputs({ runID, outputDirectory, environment = process.env, fetch: request = globalThis.fetch, verifySource = sha => execFileSync('git', ['merge-base', '--is-ancestor', sha, 'origin/main'], { cwd: repositoryRoot, stdio: 'pipe', timeout: 15_000 }) }) {
  const id = positive(runID, 'Producer run ID')
  const ctx = context(environment, request)
  const run = await ctx.api(`/actions/runs/${id}`)
  trustedRun(run, ctx.repository)
  requireValue(run.id === id && run.status === 'completed' && run.conclusion === 'success', 'Prepare must complete successfully before entering the protected release stage.')
  await verifySource(run.head_sha)
  const recent = await ctx.api('/actions/workflows/prepare-release.yml/runs?branch=main&per_page=100')
  requireValue(Array.isArray(recent.workflow_runs), 'Prepare request inventory is missing.')
  const newer = recent.workflow_runs.some(item => item.head_branch === 'main' && ['push', 'workflow_dispatch'].includes(item.event) && Number.isSafeInteger(item.run_number) && item.run_number > run.run_number)
  let resolution = { eligible: false, source_sha: run.head_sha }
  if (!newer) {
    const artifacts = []
    for (let page = 1; page <= 100; page++) {
      const result = await ctx.api(`/actions/runs/${id}/artifacts?per_page=100&page=${page}`)
      requireValue(Array.isArray(result.artifacts) && result.artifacts.length <= 100, 'Prepare artifact inventory is invalid.')
      artifacts.push(...result.artifacts)
      if (result.artifacts.length < 100) break
      requireValue(page < 100, 'Prepare artifact inventory exceeds the verification limit.')
    }
    let pair
    for (let attempt = run.run_attempt; attempt >= 1; attempt--) {
      const metadata = artifacts.filter(item => !item.expired && item.name === `build-metadata-${attempt}`)
      const unsigned = artifacts.filter(item => !item.expired && item.name === `unsigned-distribution-${attempt}`)
      requireValue(metadata.length <= 1 && unsigned.length <= 1, 'Ambiguous Prepare artifact pair.')
      if (metadata.length === 1 && unsigned.length === 1) { pair = { metadata: metadata[0], unsigned: unsigned[0], attempt }; break }
    }
    requireValue(pair, 'No complete frozen metadata and unsigned distribution pair is available.')
    for (const item of [pair.metadata, pair.unsigned]) artifactIdentity(item, positive(item.id, 'Artifact ID'), id, run.head_sha)
    const temporary = await mkdtemp(path.join(tmpdir(), 'astral-producer-metadata-'))
    let metadata
    try {
      const output = path.join(temporary, 'metadata')
      await downloadArtifact({ artifactID: pair.metadata.id, runID: id, output, expectedSHA: run.head_sha, environment, fetch: request })
      requireValue(JSON.stringify(await readdir(output)) === JSON.stringify(['release-build.json']), 'Producer metadata artifact has an unexpected file inventory.')
      metadata = validateBuildMetadata(JSON.parse(await readFile(path.join(output, 'release-build.json'), 'utf8')), { sourceSHA: run.head_sha, repository: ctx.repository })
      requireValue(metadata.build_run_id === id && metadata.build_run_number === run.run_number && metadata.build_run_attempt === pair.attempt, 'Frozen metadata does not match the selected Prepare run and artifact attempt.')
    } finally { await rm(temporary, { recursive: true, force: true }) }
    resolution = { eligible: true, source_sha: run.head_sha, version: metadata.version, tag: `v${metadata.version}`,
      build_metadata_artifact_id: pair.metadata.id, build_metadata_artifact_digest: pair.metadata.digest,
      unsigned_artifact_id: pair.unsigned.id, unsigned_artifact_digest: pair.unsigned.digest, build_metadata: metadata }
  }
  await emptyDestination(outputDirectory)
  await writeFile(path.join(outputDirectory, 'resolution.json'), `${JSON.stringify(resolution, null, 2)}\n`, { flag: 'wx', mode: 0o600 })
  if (environment.GITHUB_OUTPUT) for (const [key, value] of Object.entries(resolution)) if (key !== 'build_metadata') await appendFile(environment.GITHUB_OUTPUT, `${key}=${value}\n`)
  return resolution
}

function options(args) {
  const values = {}
  for (let index = 0; index < args.length; index += 2) {
    requireValue(['--artifact-id', '--run-id', '--output', '--source-sha', '--output-dir'].includes(args[index]) && args[index + 1] && !Object.hasOwn(values, args[index]), 'Invalid release input arguments.')
    values[args[index]] = args[index + 1]
  }
  return values
}
async function main() {
  const [command, ...args] = process.argv.slice(2), values = options(args)
  if (command === 'resolve') {
    requireValue(values['--output-dir'], 'resolve requires --output-dir DIR.')
    console.log(JSON.stringify(await resolveInputs({ runID: environmentRunID(), outputDirectory: path.resolve(values['--output-dir']) })))
  } else if (command === 'download') {
    requireValue(values['--output'], 'download requires --output DIR.')
    console.log(JSON.stringify(await downloadArtifact({ artifactID: values['--artifact-id'], runID: values['--run-id'], output: path.resolve(values['--output']), expectedSHA: values['--source-sha'] })))
  } else throw new Error('Usage: rolling-release-inputs.mjs resolve --output-dir DIR | download --artifact-id ID --run-id RUN [--source-sha SHA] --output DIR')
}
function environmentRunID() { return process.env.ASTRAL_BUILD_RUN_ID }
if (await isDirectInvocation(import.meta.url)) main().catch(error => { console.error(error.message); process.exitCode = 1 })
