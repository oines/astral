import { execFileSync } from 'node:child_process'
import { appendFile, mkdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { isDirectInvocation, readReleaseVersion, repositoryRoot, setReleaseVersion, validateVersion } from './release-version.mjs'

export const releaseWorkflowPath = '.github/workflows/prepare-release.yml'
const metadataKeys = ['schema_version', 'repository', 'source_sha', 'version', 'build_run_id', 'build_run_attempt', 'build_run_number', 'build_run_url', 'workflow_path']

function positiveInteger(value, name) {
  if (!(typeof value === 'number' || typeof value === 'string' && /^[1-9]\d*$/.test(value)) || !Number.isSafeInteger(Number(value)) || Number(value) < 1) throw new Error(`${name} must be a positive safe integer.`)
  return Number(value)
}

export function versionForRun(runNumber) {
  const number = positiveInteger(runNumber, 'Build run number')
  if (!Number.isSafeInteger(13 + number)) throw new Error('Build run number exceeds the rolling version range.')
  return validateVersion(`0.1.${13 + number}`)
}

export function validateBuildMetadata(value, { version, sourceSHA, repository } = {}) {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length !== metadataKeys.length || metadataKeys.some(key => !Object.hasOwn(value, key)) || value.schema_version !== 1) throw new Error('Release build metadata has an unsupported schema.')
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(value.repository || '') || value.repository.split('/').some(part => part === '.' || part === '..')) throw new Error('Release build metadata must identify a GitHub repository.')
  if (!/^[a-f0-9]{40}$/.test(value.source_sha || '')) throw new Error('Release build metadata requires a full source SHA.')
  for (const key of ['build_run_id', 'build_run_attempt', 'build_run_number']) {
    if (typeof value[key] !== 'number' || positiveInteger(value[key], key) !== value[key]) throw new Error(`Release build metadata ${key} must be a positive safe integer.`)
  }
  if (value.version !== versionForRun(value.build_run_number)) throw new Error('Release build version does not match its frozen producer run number.')
  if (value.build_run_url !== `https://github.com/${value.repository}/actions/runs/${value.build_run_id}` || value.workflow_path !== releaseWorkflowPath) throw new Error('Release build metadata does not identify the expected producer workflow and run.')
  if (version !== undefined && value.version !== version || sourceSHA !== undefined && value.source_sha !== sourceSHA || repository !== undefined && value.repository !== repository) throw new Error('Release build metadata does not match the expected source, version and repository.')
  return value
}

export function createBuildMetadata(environment = process.env, sourceSHA = environment.GITHUB_SHA) {
  const repository = environment.GITHUB_REPOSITORY
  if (environment.GITHUB_WORKFLOW_REF !== `${repository}/${releaseWorkflowPath}@refs/heads/main`) throw new Error('Rolling release metadata must be frozen by prepare-release.yml on main.')
  const number = positiveInteger(environment.GITHUB_RUN_NUMBER, 'GITHUB_RUN_NUMBER')
  return validateBuildMetadata({
    schema_version: 1,
    repository,
    source_sha: sourceSHA,
    version: versionForRun(number),
    build_run_id: positiveInteger(environment.GITHUB_RUN_ID, 'GITHUB_RUN_ID'),
    build_run_attempt: positiveInteger(environment.GITHUB_RUN_ATTEMPT, 'GITHUB_RUN_ATTEMPT'),
    build_run_number: number,
    build_run_url: `https://github.com/${repository}/actions/runs/${environment.GITHUB_RUN_ID}`,
    workflow_path: releaseWorkflowPath,
  })
}

function checkedOutSource(root) {
  return execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', timeout: 15_000, stdio: ['ignore', 'pipe', 'pipe'] }).trim()
}

export async function applyBuildVersion(metadata, { root = repositoryRoot, environment = process.env } = {}) {
  validateBuildMetadata(metadata)
  if (checkedOutSource(root) !== metadata.source_sha) throw new Error('Checked-out source does not match the frozen release build SHA.')
  if (environment.GITHUB_REPOSITORY !== undefined && environment.GITHUB_REPOSITORY !== metadata.repository) throw new Error('Checked-out repository does not match the frozen release build repository.')
  if (environment.ASTRAL_RELEASE_VERSION !== undefined && environment.ASTRAL_RELEASE_VERSION !== metadata.version) throw new Error('Release version override differs from the frozen build metadata.')
  await readReleaseVersion(root, {})
  // This is a disposable runner overlay, not a version-bump commit. Electron
  // reads this metadata for app.getVersion(), native bundles and artifact names.
  await setReleaseVersion(metadata.version, root)
  if (environment.GITHUB_ENV) await appendFile(environment.GITHUB_ENV, `ASTRAL_RELEASE_VERSION=${metadata.version}\n`)
  return metadata
}

async function outputs(metadata, environment = process.env) {
  if (environment.GITHUB_OUTPUT) await appendFile(environment.GITHUB_OUTPUT, `version=${metadata.version}\ntag=v${metadata.version}\nsource_sha=${metadata.source_sha}\n`)
}

function options(args, allowed) {
  const result = {}
  for (let index = 0; index < args.length; index += 2) {
    if (!allowed.includes(args[index]) || !args[index + 1] || args[index + 1].startsWith('--') || Object.hasOwn(result, args[index])) throw new Error('Invalid rolling release version arguments.')
    result[args[index]] = args[index + 1]
  }
  return result
}

async function main() {
  const [command, ...args] = process.argv.slice(2)
  if (command === 'freeze') {
    const option = options(args, ['--output', '--sha'])
    if (!option['--output']) throw new Error('freeze requires --output FILE.')
    const metadata = createBuildMetadata(process.env, option['--sha'] || process.env.GITHUB_SHA)
    if (checkedOutSource(repositoryRoot) !== metadata.source_sha) throw new Error('Checked-out source does not match the release source being frozen.')
    const output = path.resolve(option['--output'])
    await mkdir(path.dirname(output), { recursive: true })
    await writeFile(output, `${JSON.stringify(metadata, null, 2)}\n`, { flag: 'wx' })
    await outputs(metadata)
    console.log(JSON.stringify(metadata))
  } else if (command === 'apply') {
    const option = options(args, ['--metadata'])
    if (!option['--metadata']) throw new Error('apply requires --metadata FILE.')
    const metadata = JSON.parse(await readFile(path.resolve(option['--metadata']), 'utf8'))
    await applyBuildVersion(metadata)
    await outputs(metadata)
    console.log(JSON.stringify({ version: metadata.version, tag: `v${metadata.version}`, source_sha: metadata.source_sha }))
  } else throw new Error('Usage: rolling-release-version.mjs freeze --output FILE [--sha FULL_SHA] | apply --metadata FILE')
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
