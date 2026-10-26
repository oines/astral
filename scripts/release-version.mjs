import { readFile, realpath, rename, writeFile } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const repositoryRoot = fileURLToPath(new URL('../', import.meta.url))
const manifests = ['package.json', 'apps/desktop/package.json']

export async function isDirectInvocation(url) {
  if (!process.argv[1]) return false
  return await realpath(process.argv[1]).catch(() => undefined) === fileURLToPath(url)
}

export function validateVersion(version) {
  const match = typeof version === 'string' && /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(version)
  if (!match || match.slice(1).some(part => !Number.isSafeInteger(Number(part)))) throw new Error('Release version must be MAJOR.MINOR.PATCH, with nonnegative safe integers and no leading zeroes, prerelease or build suffix.')
  return version
}

export function validateTag(tag, version) {
  if (tag !== `v${validateVersion(version)}`) throw new Error(`Expected tag v${version}, received ${tag || '(empty)'}.`)
  return tag
}

export async function readReleaseVersion(root = repositoryRoot) {
  const entries = await Promise.all(manifests.map(async name => JSON.parse(await readFile(path.join(root, name), 'utf8'))))
  const version = validateVersion(entries[0].version)
  if (entries.some(entry => entry.version !== version)) throw new Error('Root and Desktop package versions differ. Use release-version.mjs set MAJOR.MINOR.PATCH.')
  return version
}

export async function setReleaseVersion(version, root = repositoryRoot) {
  validateVersion(version)
  // Read and validate both files before making either change. No commit or tag is created.
  const entries = await Promise.all(manifests.map(async name => ({ name, value: JSON.parse(await readFile(path.join(root, name), 'utf8')) })))
  for (const { name, value } of entries) {
    if (typeof value.version !== 'string') throw new Error(`Missing version in ${name}.`)
  }
  for (const { name, value } of entries) {
    const target = path.join(root, name)
    const temporary = `${target}.release-version-${process.pid}`
    await writeFile(temporary, `${JSON.stringify({ ...value, version }, null, 2)}\n`, { flag: 'wx' })
    await rename(temporary, target)
  }
  return readReleaseVersion(root)
}

export async function readDataFormats(binary, sourceSHA, root = repositoryRoot) {
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('A full source commit SHA is required.')
  if (!binary) throw new Error('Pass --binary with the just-built runner-native Astral executable.')
  const version = await readReleaseVersion(root)
  const metadata = JSON.parse(execFileSync(path.resolve(binary), ['version', '--json'], { encoding: 'utf8', timeout: 15_000, maxBuffer: 64 << 10 }))
  const goModule = await readFile(path.join(root, 'go.mod'), 'utf8')
  const dependency = /^\s*github\.com\/oines\/astral-core\s+(\S+)\s*$/m.exec(goModule)
  if (metadata.version !== version || !Number.isSafeInteger(metadata.data_formats?.host) || metadata.data_formats.host < 1 ||
      !Number.isSafeInteger(metadata.data_formats?.core) || metadata.data_formats.core < 1 || !dependency ||
      metadata.migration_status !== 'goose' || metadata.migration_baselines?.host !== 3 || metadata.migration_baselines?.core !== 10 ||
      metadata.migration_targets?.host !== metadata.data_formats.host || metadata.migration_targets?.core !== metadata.data_formats.core) {
    throw new Error('Built executable does not report the expected release version and Host/Core data formats.')
  }
  return {
    schema_version: 1,
    version,
    source_sha: sourceSHA,
    data_formats: metadata.data_formats,
    host_schema: metadata.data_formats.host,
    core_format: metadata.data_formats.core,
    core_module_version: dependency[1],
    migration_status: metadata.migration_status,
    migration_baselines: metadata.migration_baselines,
    migration_targets: metadata.migration_targets,
  }
}

function option(args, name) {
  const position = args.indexOf(name)
  if (position < 0) return undefined
  if (!args[position + 1] || args[position + 1].startsWith('--')) throw new Error(`${name} requires a value.`)
  return args[position + 1]
}

async function main() {
  const [command, ...args] = process.argv.slice(2)
  if (command === 'validate' && args.length === 1) console.log(validateVersion(args[0]))
  else if (command === 'set' && args.length === 1) console.log(`Root and Desktop versions are now ${await setReleaseVersion(args[0])}. Review and commit both manifests.`)
  else if (command === 'check') {
    if (args.length && (args.length !== 2 || args[0] !== '--tag')) throw new Error('Usage: release-version.mjs check [--tag vMAJOR.MINOR.PATCH]')
    const version = await readReleaseVersion()
    const tag = option(args, '--tag')
    if (tag !== undefined) validateTag(tag, version)
    console.log(version)
  } else if (command === 'formats') console.log(JSON.stringify(await readDataFormats(option(args, '--binary'), option(args, '--sha')), null, 2))
  else throw new Error('Usage: release-version.mjs validate MAJOR.MINOR.PATCH | set MAJOR.MINOR.PATCH | check [--tag vMAJOR.MINOR.PATCH] | formats --binary NATIVE_EXECUTABLE --sha COMMIT')
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
