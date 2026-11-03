import { copyFile, lstat, mkdir, mkdtemp, readFile, readdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { desktopNames, digest, distributionNames } from './release-assets.mjs'
import { downloadArtifact } from './rolling-release-inputs.mjs'
import { isDirectInvocation, readReleaseVersion, repositoryRoot } from './release-version.mjs'
import { validateBuildMetadata } from './rolling-release-version.mjs'

const artifactNames = ['signed-distribution', 'mac-notary-binaries', 'desktop-mac-x64', 'desktop-mac-arm64', 'desktop-win', 'desktop-linux']
function requireValue(value, message) { if (!value) throw new Error(message) }
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

export async function gatherPackageInputs({ root = repositoryRoot, environment = process.env, fetch: request = globalThis.fetch, download = downloadArtifact } = {}) {
  const repository = environment.GITHUB_REPOSITORY, token = environment.GH_TOKEN || environment.GITHUB_TOKEN
  const runID = Number(environment.GITHUB_RUN_ID)
  requireValue(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '') && !repository.split('/').some(part => part === '.' || part === '..') && token && /^[1-9]\d*$/.test(environment.GITHUB_RUN_ID || '') && Number.isSafeInteger(runID) && runID > 0 && (environment.GITHUB_API_URL || 'https://api.github.com') === 'https://api.github.com', 'A job-scoped repository and current Release run are required.')
  const version = await readReleaseVersion(root, environment)
  const metadata = validateBuildMetadata(JSON.parse(await readFile(path.join(root, '.release-metadata/release-build.json'), 'utf8')), { repository, version })
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
  const selected = artifactNames.map(name => {
    const matching = artifacts.filter(item => item.name === name)
    requireValue(matching.length === 1 && matching[0].expired === false && matching[0].workflow_run?.id === runID && Number.isSafeInteger(matching[0].id) && matching[0].id > 0 && /^sha256:[a-f0-9]{64}$/.test(matching[0].digest || ''), `Missing, ambiguous or foreign package artifact: ${name}.`)
    return matching[0]
  })
  const output = path.join(root, 'release-inputs')
  try {
    const info = await lstat(output)
    requireValue(info.isDirectory() && !info.isSymbolicLink() && (await readdir(output)).length === 0, 'release-inputs must be a new empty directory.')
  } catch (error) { if (error.code !== 'ENOENT') throw error }
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-package-inputs-'))
  try {
    await Promise.all(selected.map(async item => {
      const actual = await download({ artifactID: item.id, runID, output: path.join(temporary, item.name), environment, fetch: request })
      requireValue(actual.id === item.id && actual.name === item.name && actual.digest === item.digest && actual.size_in_bytes === item.size_in_bytes, 'The package artifact changed between selection and download.')
    }))
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
        if (name === 'mac-notary-binaries') await copyExact(source, path.join(root, 'apps/desktop/release/notarization/binaries.json'))
        else if (file.startsWith('notarization/')) await copyExact(source, path.join(root, 'apps/desktop/release', file))
        else {
          await copyExact(source, path.join(output, name, file))
          if (name.startsWith('desktop-mac-')) await copyExact(source, path.join(root, 'apps/desktop/release', file))
        }
      }
    }
    return { source_sha: metadata.source_sha, version, artifact_ids: selected.map(item => item.id), package_inputs: signedNames.length + desktopNames(version).length, notarization_partials: 3 }
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

if (await isDirectInvocation(import.meta.url)) {
  if (process.argv.length !== 2) { console.error('Usage: rolling-release-package-inputs.mjs'); process.exitCode = 1 }
  else gatherPackageInputs().then(value => console.log(JSON.stringify(value))).catch(error => { console.error(error.message); process.exitCode = 1 })
}
