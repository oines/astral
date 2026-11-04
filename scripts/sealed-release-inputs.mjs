import { copyFile, lstat, mkdir, mkdtemp, readFile, readdir, rm, stat, appendFile } from 'node:fs/promises'
import { constants } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { desktopNames, digest, distributionNames } from './release-assets.mjs'
import { verifyReleaseBundle } from './release-draft.mjs'
import { gatherPackageInputs, validatePackageReferences } from './rolling-release-package-inputs.mjs'
import { isDirectInvocation, repositoryRoot } from './release-version.mjs'

const descriptorName = 'package-inputs.json'
function requireValue(value, message) { if (!value) throw new Error(message) }
const payloadNames = version => [...distributionNames, ...desktopNames(version)]

async function emptyOutput(directory, input) {
  requireValue(path.resolve(directory) !== path.resolve(input) && !path.resolve(directory).startsWith(`${path.resolve(input)}${path.sep}`), 'Restored output must be outside its sealed metadata input.')
  try {
    const info = await lstat(directory)
    requireValue(info.isDirectory() && !info.isSymbolicLink() && (await readdir(directory)).length === 0, 'Sealed release output must be a new empty directory.')
    return false
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
    await mkdir(directory, { recursive: true, mode: 0o700 })
    return true
  }
}

async function regularFiles(directory) {
  const files = await readdir(directory)
  for (const name of files) {
    requireValue(/^[A-Za-z0-9._-]+$/.test(name), 'Sealed metadata contains an unsafe filename.')
    const info = await lstat(path.join(directory, name))
    requireValue(info.isFile() && !info.isSymbolicLink(), 'Sealed metadata contains a non-regular file.')
  }
  return files.sort()
}

function referencesForManifest(references, manifest, environment) {
  const runID = Number(environment.GITHUB_RUN_ID)
  requireValue(Number.isSafeInteger(runID) && runID > 0 && manifest.repository === environment.GITHUB_REPOSITORY &&
    manifest.schema_version === 1 && manifest.purpose === 'release' && manifest.public_release_ready === true &&
    manifest.packaging_run_url === `https://github.com/${environment.GITHUB_REPOSITORY}/actions/runs/${runID}` &&
    manifest.ci_run_url === manifest.packaging_run_url && manifest.candidate_run_url === manifest.packaging_run_url &&
    manifest.build_provenance?.source_sha === manifest.source_sha && manifest.build_provenance?.version === manifest.version, 'Sealed metadata does not belong to this exact protected release source and packaging run.')
  return validatePackageReferences(references, { repository: manifest.repository, runID, metadata: manifest.build_provenance })
}

function checksumIndex(bytes) {
  const result = new Map()
  for (const line of bytes.toString('utf8').trim().split('\n')) {
    const match = /^([a-f0-9]{64}) {2}([A-Za-z0-9._-]+)$/.exec(line)
    requireValue(match && !result.has(match[2]) && match[2] !== 'SHA256SUMS' && match[2] !== descriptorName, 'The sealed checksum inventory is invalid or ambiguous.')
    result.set(match[2], match[1])
  }
  return result
}

async function copy(source, target) {
  // These are the already accepted bytes. Never call the collector, compiler,
  // signer, feed generator, report aggregator or checksum writer here.
  await copyFile(source, target, constants.COPYFILE_EXCL | constants.COPYFILE_FICLONE)
}

export async function sealReleaseInputs({ directory, output, root = repositoryRoot, environment = process.env, verify = verifyReleaseBundle }) {
  const verified = await verify(directory, { environment })
  const { manifest, files, sealSHA256 } = verified
  const referenceFile = path.join(root, '.release-metadata', descriptorName)
  const referenceInfo = await lstat(referenceFile)
  requireValue(referenceInfo.isFile() && !referenceInfo.isSymbolicLink(), 'Frozen package references must be a regular file.')
  const references = referencesForManifest(JSON.parse(await readFile(referenceFile, 'utf8')), manifest, environment)
  const metadataFiles = files.filter(name => !payloadNames(manifest.version).includes(name)).sort()
  requireValue(!files.includes(descriptorName) && metadataFiles.includes('SHA256SUMS') && metadataFiles.includes('release-manifest.json'), 'The final public inventory lacks its sealed metadata.')
  const created = await emptyOutput(output, directory)
  try {
    for (const name of metadataFiles) await copy(path.join(directory, name), path.join(output, name))
    await copy(referenceFile, path.join(output, descriptorName))
    // Copying metadata must not silently change its independently attested
    // index. The original descriptor is attested as a second subject.
    requireValue(await digest(path.join(output, 'SHA256SUMS')) === sealSHA256, 'The sealed metadata index changed while being copied.')
    const result = { version: manifest.version, source_sha: manifest.source_sha, bundle_sha256: sealSHA256, inputs_sha256: await digest(path.join(output, descriptorName)),
      artifact_ids: references.artifacts.map(item => item.id), public_assets: files.length, metadata_files: metadataFiles.length + 1,
      metadata_bytes: (await Promise.all((await regularFiles(output)).map(async name => (await stat(path.join(output, name))).size))).reduce((sum, value) => sum + value, 0) }
    return result
  } catch (error) { if (created) await rm(output, { recursive: true, force: true }); throw error }
}

export async function restoreReleaseInputs({ input, output, root = repositoryRoot, environment = process.env, gather = gatherPackageInputs, verify = verifyReleaseBundle, ...transport }) {
  const files = await regularFiles(input)
  requireValue(files.includes(descriptorName) && files.includes('SHA256SUMS') && files.includes('release-manifest.json'), 'The sealed metadata artifact is incomplete.')
  const expectedIndex = environment.ASTRAL_RELEASE_BUNDLE_SHA256, expectedInputs = environment.ASTRAL_RELEASE_INPUTS_SHA256
  requireValue(/^[a-f0-9]{64}$/.test(expectedIndex || '') && /^[a-f0-9]{64}$/.test(expectedInputs || ''), 'Restoration requires both independently verified sealed index and package-reference digests.')
  requireValue(await digest(path.join(input, 'SHA256SUMS')) === expectedIndex && await digest(path.join(input, descriptorName)) === expectedInputs, 'The sealed index or immutable package references differ from their verified digests.')
  const manifest = JSON.parse(await readFile(path.join(input, 'release-manifest.json'), 'utf8'))
  const references = referencesForManifest(JSON.parse(await readFile(path.join(input, descriptorName), 'utf8')), manifest, environment)
  const sums = checksumIndex(await readFile(path.join(input, 'SHA256SUMS')))
  const payload = payloadNames(manifest.version)
  requireValue(payload.every(name => sums.has(name)), 'The sealed index omits a final package payload.')
  const metadataFiles = [...sums.keys()].filter(name => !payload.includes(name)).concat('SHA256SUMS').sort()
  requireValue(isDeepStrictEqual(files, [...metadataFiles, descriptorName].sort()), 'Unexpected or missing files in sealed release metadata.')
  for (const name of metadataFiles) if (name !== 'SHA256SUMS') requireValue(await digest(path.join(input, name)) === sums.get(name), 'Sealed metadata bytes differ from the accepted checksum index.')
  const created = await emptyOutput(output, input)
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-release-restore-'))
  try {
    const packageOutput = path.join(temporary, 'payloads')
    await gather({ ...transport, root, environment, references, output: packageOutput, copyNotarization: false })
    for (const name of payload) {
      const artifact = distributionNames.includes(name) ? 'signed-distribution' : name.includes('-mac-') ? `desktop-mac-${name.includes('-arm64.') ? 'arm64' : 'x64'}` : name.includes('-win-') ? 'desktop-win' : 'desktop-linux'
      const source = path.join(packageOutput, artifact, name)
      requireValue(await digest(source) === sums.get(name), `Original package bytes differ from the sealed release index: ${name}.`)
      await copy(source, path.join(output, name))
    }
    for (const name of metadataFiles) await copy(path.join(input, name), path.join(output, name))
    const verified = await verify(output, { environment, requireSeal: true })
    requireValue(verified.sealSHA256 === expectedIndex && isDeepStrictEqual(verified.manifest, manifest), 'The restored bundle differs from its sealed source and inventory.')
    return { version: manifest.version, source_sha: manifest.source_sha, bundle_sha256: verified.sealSHA256, inputs_sha256: expectedInputs,
      artifact_ids: references.artifacts.map(item => item.id), public_assets: verified.files.length }
  } catch (error) { if (created) await rm(output, { recursive: true, force: true }); throw error }
  finally { await rm(temporary, { recursive: true, force: true }) }
}

async function main() {
  const [command, input, output, ...extra] = process.argv.slice(2)
  requireValue(['seal', 'restore'].includes(command) && input && output && !extra.length, 'Usage: sealed-release-inputs.mjs seal FULL_DIR METADATA_DIR | restore METADATA_DIR FULL_DIR')
  const result = command === 'seal' ? await sealReleaseInputs({ directory: input, output }) : await restoreReleaseInputs({ input, output })
  if (process.env.GITHUB_OUTPUT) await appendFile(process.env.GITHUB_OUTPUT, `bundle_sha256=${result.bundle_sha256}\ninputs_sha256=${result.inputs_sha256}\n`)
  console.log(JSON.stringify(result))
}

if (await isDirectInvocation(import.meta.url)) main().catch(error => { console.error(error.message); process.exitCode = 1 })
