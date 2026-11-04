import { copyFile, lstat, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { desktopNames, digest, distributionNames } from './release-assets.mjs'
import { downloadArtifact } from './rolling-release-inputs.mjs'
import { isDirectInvocation, readReleaseVersion, repositoryRoot } from './release-version.mjs'
import { validateBuildMetadata } from './rolling-release-version.mjs'

export const packageArtifactNames = ['signed-distribution', 'mac-notary-binaries', 'desktop-mac-x64', 'desktop-mac-arm64', 'desktop-win', 'desktop-linux']
function requireValue(value, message) { if (!value) throw new Error(message) }
const positive = value => Number.isSafeInteger(value) && value > 0
const exactKeys = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) && isDeepStrictEqual(Object.keys(value).sort(), [...keys].sort())

export function validatePackageReferences(value, { repository, runID, metadata }) {
  requireValue(exactKeys(value, ['schema_version', 'repository', 'version', 'source_sha', 'packaging_run_id', 'packaging_run_url', 'build_provenance', 'artifacts']) &&
    value.schema_version === 1 && value.repository === repository && value.packaging_run_id === runID &&
    value.packaging_run_url === `https://github.com/${repository}/actions/runs/${runID}` &&
    value.version === metadata.version && value.source_sha === metadata.source_sha && isDeepStrictEqual(value.build_provenance, metadata), 'Sealed package references differ from their exact repository, source, producer or packaging run.')
  validateBuildMetadata(value.build_provenance, { repository, version: value.version, sourceSHA: value.source_sha })
  requireValue(Array.isArray(value.artifacts) && value.artifacts.length === packageArtifactNames.length && new Set(value.artifacts.map(item => item?.name)).size === packageArtifactNames.length &&
    new Set(value.artifacts.map(item => item?.id)).size === packageArtifactNames.length, 'Sealed package artifact inventory is incomplete or ambiguous.')
  for (const item of value.artifacts) requireValue(exactKeys(item, ['name', 'id', 'digest', 'size_in_bytes', 'workflow_run']) && packageArtifactNames.includes(item.name) && positive(item.id) && positive(item.size_in_bytes) &&
    /^sha256:[a-f0-9]{64}$/.test(item.digest || '') && exactKeys(item.workflow_run, ['id', 'head_sha']) && item.workflow_run.id === runID && /^[a-f0-9]{40}$/.test(item.workflow_run.head_sha || ''), 'Sealed package artifact identity or digest is invalid.')
  return value
}

function freezeReferences(selected, repository, runID, metadata) {
  return validatePackageReferences({
    schema_version: 1, repository, version: metadata.version, source_sha: metadata.source_sha,
    packaging_run_id: runID, packaging_run_url: `https://github.com/${repository}/actions/runs/${runID}`, build_provenance: metadata,
    artifacts: selected.map(item => ({ name: item.name, id: item.id, digest: item.digest, size_in_bytes: item.size_in_bytes, workflow_run: { id: item.workflow_run.id, head_sha: item.workflow_run.head_sha } })),
  }, { repository, runID, metadata })
}
async function filesIn(directory, prefix = '') {
  const files = []
  for (const item of await readdir(directory, { withFileTypes: true })) {
    const name = prefix + item.name
    requireValue(!item.isSymbolicLink(), 'Package artifacts cannot contain symbolic links.')
    if (item.isDirectory()) files.push(...await filesIn(path.join(directory, item.name), name + '/'))
    else { requireValue(item.isFile(), 'Package artifacts must contain regular files.'); files.push(name) }
  }
  return files.sort()
}
function exactFiles(actual, expected, label) {
  requireValue(JSON.stringify(actual) === JSON.stringify([...expected].sort()), `Unexpected or missing payload in ${label}.`)
}
async function copyExact(source, target) {
  await mkdir(path.dirname(target), { recursive: true })
  try {
    const info = await lstat(target)
    requireValue(info.isFile() && !info.isSymbolicLink() && await digest(source) === await digest(target), 'A different file already occupies a final package input path.')
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
    await copyFile(source, target)
  }
}

export async function gatherPackageInputs({ root = repositoryRoot, environment = process.env, fetch: request = globalThis.fetch, download = downloadArtifact, references, output = path.join(root, 'release-inputs'), copyNotarization = true } = {}) {
  const repository = environment.GITHUB_REPOSITORY, token = environment.GH_TOKEN || environment.GITHUB_TOKEN
  const runID = Number(environment.GITHUB_RUN_ID)
  requireValue(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '') && !repository.split('/').some(part => part === '.' || part === '..') && token && /^[1-9]\d*$/.test(environment.GITHUB_RUN_ID || '') && Number.isSafeInteger(runID) && runID > 0 && (environment.GITHUB_API_URL || 'https://api.github.com') === 'https://api.github.com', 'A job-scoped repository and current Release run are required.')
  const version = await readReleaseVersion(root, environment)
  const metadata = validateBuildMetadata(JSON.parse(await readFile(path.join(root, '.release-metadata/release-build.json'), 'utf8')), { repository, version })
  let selected
  if (references !== undefined) selected = validatePackageReferences(references, { repository, runID, metadata }).artifacts
  else {
    const artifacts = []
    for (let page = 1; page <= 100; page++) {
      const response = await request(`https://api.github.com/repos/${repository}/actions/runs/${runID}/artifacts?per_page=100&page=${page}`, {
        redirect: 'error', signal: AbortSignal.timeout(60_000), headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
      })
      requireValue(response.ok, `Package input inventory failed (HTTP ${response.status}).`)
      const result = await response.json()
      requireValue(Array.isArray(result.artifacts) && result.artifacts.length <= 100, 'Package input inventory is invalid.')
      artifacts.push(...result.artifacts)
      if (result.artifacts.length < 100) break
      requireValue(page < 100, 'Package artifact inventory exceeds the verification limit.')
    }
    selected = packageArtifactNames.map(name => {
      const matching = artifacts.filter(item => item.name === name)
      requireValue(matching.length === 1 && matching[0].expired === false && matching[0].workflow_run?.id === runID && /^[a-f0-9]{40}$/.test(matching[0].workflow_run?.head_sha || '') && positive(matching[0].id) && positive(matching[0].size_in_bytes) && /^sha256:[a-f0-9]{64}$/.test(matching[0].digest || ''), `Missing, ambiguous or foreign package artifact: ${name}.`)
      return matching[0]
    })
  }
  const frozen = freezeReferences(selected, repository, runID, metadata)
  try {
    const info = await lstat(output)
    requireValue(info.isDirectory() && !info.isSymbolicLink() && (await readdir(output)).length === 0, 'release-inputs must be a new empty directory.')
  } catch (error) { if (error.code !== 'ENOENT') throw error }
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-package-inputs-'))
  try {
    const downloads = await Promise.allSettled(selected.map(async item => {
      const actual = await download({ artifactID: item.id, runID, output: path.join(temporary, item.name), environment, fetch: request })
      requireValue(actual.id === item.id && actual.name === item.name && actual.digest === item.digest && actual.size_in_bytes === item.size_in_bytes && actual.expired === false && actual.workflow_run?.id === item.workflow_run.id && actual.workflow_run?.head_sha === item.workflow_run.head_sha, 'The package artifact changed between selection and download.')
    }))
    const failed = downloads.find(value => value.status === 'rejected')
    if (failed) throw failed.reason
    const signedNames = [...distributionNames, 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1', 'release-formats.json', 'checksums.txt']
    const plans = new Map([
      ['signed-distribution', signedNames], ['mac-notary-binaries', ['binaries.json']],
      ...['x64', 'arm64'].map(arch => [`desktop-mac-${arch}`, [...desktopNames(version).filter(name => name.includes(`-mac-${arch}.`)), `notarization/${arch}.json`]]),
      ...['win', 'linux'].map(platform => [`desktop-${platform}`, desktopNames(version).filter(name => name.includes(`-${platform}-`))]),
    ])
    for (const [name, expected] of plans) exactFiles(await filesIn(path.join(temporary, name)), expected, name)
    const formats = JSON.parse(await readFile(path.join(temporary, 'signed-distribution/release-formats.json'), 'utf8'))
    requireValue(formats.schema_version === 1 && formats.version === version && formats.source_sha === metadata.source_sha, 'Signed distribution metadata differs from the frozen release source/version.')
    const checksums = new Map()
    for (const line of (await readFile(path.join(temporary, 'signed-distribution/checksums.txt'), 'utf8')).trim().split('\n')) {
      const match = /^([a-f0-9]{64}) {2}([A-Za-z0-9._-]+)$/.exec(line)
      requireValue(match && distributionNames.includes(match[2]) && !checksums.has(match[2]), 'Signed program checksums are invalid or ambiguous.')
      checksums.set(match[2], match[1])
    }
    requireValue(checksums.size === distributionNames.length, 'Signed program checksum inventory is incomplete.')
    for (const name of distributionNames) requireValue(await digest(path.join(temporary, 'signed-distribution', name)) === checksums.get(name), 'Signed program bytes differ from their frozen checksums.')
    const partials = [
      ['mac-notary-binaries', 'binaries.json', undefined],
      ...['x64', 'arm64'].map(arch => [`desktop-mac-${arch}`, `notarization/${arch}.json`, arch]),
    ]
    for (const [name, file, arch] of partials) {
      const report = JSON.parse(await readFile(path.join(temporary, name, file), 'utf8'))
      requireValue(report.version === version && report.source_sha === metadata.source_sha && report.arch === arch, 'A notarization partial differs from the frozen release source/version/architecture.')
    }
    for (const [name, files] of plans) {
      for (const file of files) {
        const source = path.join(temporary, name, file)
        if (name === 'mac-notary-binaries' || file.startsWith('notarization/')) {
          if (copyNotarization) await copyExact(source, path.join(root, 'apps/desktop/release', name === 'mac-notary-binaries' ? 'notarization/binaries.json' : file))
        } else {
          await copyExact(source, path.join(output, name, file))
          if (copyNotarization && name.startsWith('desktop-mac-')) await copyExact(source, path.join(root, 'apps/desktop/release', file))
        }
      }
    }
    if (references === undefined) {
      const file = path.join(root, '.release-metadata/package-inputs.json'), bytes = `${JSON.stringify(frozen, null, 2)}\n`
      try { await writeFile(file, bytes, { flag: 'wx', mode: 0o600 }) }
      catch (error) {
        if (error.code !== 'EEXIST') throw error
        requireValue(await readFile(file, 'utf8') === bytes, 'The frozen package input references already exist with different identities.')
      }
    }
    return { source_sha: metadata.source_sha, version, artifact_ids: selected.map(item => item.id), package_inputs: signedNames.length + desktopNames(version).length, notarization_partials: 3, references: frozen }
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

if (await isDirectInvocation(import.meta.url)) {
  if (process.argv.length !== 2) { console.error('Usage: rolling-release-package-inputs.mjs'); process.exitCode = 1 }
  else gatherPackageInputs().then(value => console.log(JSON.stringify(value))).catch(error => { console.error(error.message); process.exitCode = 1 })
}
