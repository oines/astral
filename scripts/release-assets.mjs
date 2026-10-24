import { createHash } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { copyFile, mkdir, readdir, readFile, stat, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { isDirectInvocation, readReleaseVersion, validateTag } from './release-version.mjs'

export const distributionNames = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))

export function desktopNames(version) {
  return [['mac', ['dmg', 'zip']], ['win', ['exe', 'zip']], ['linux', ['AppImage', 'deb']]]
    .flatMap(([system, extensions]) => ['x64', 'arm64'].flatMap(arch => extensions.map(extension => `Astral-${version}-${system}-${arch}.${extension}`)))
}

export const macSigningReportName = 'mac-signing-report.json'
const signingChecks = ['signatures', 'hardened_runtime', 'secure_timestamps', 'embedded_checksums', 'stapling', 'gatekeeper', 'packaged_app_startup_shutdown']

export async function validateMacSigningReport(report, { version, sourceSHA, files }) {
  const expires = report?.certificate_expires_at
  const expiryDate = typeof expires === 'string' ? new Date(expires) : new Date(NaN)
  const validExpiry = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(expires || '') &&
    Number.isFinite(expiryDate.getTime()) && expiryDate.toISOString() === (expires.includes('.') ? expires : expires.replace('Z', '.000Z')) && expiryDate.getTime() > Date.now()
  if (report?.schema_version !== 1 || report.version !== version || report.source_sha !== sourceSHA ||
      report.team_id !== '6N6W6Z6739' || report.status !== 'accepted' || !validExpiry ||
      !['arm64', 'x64'].includes(report.native_tested_arch)) throw new Error('Mac signing report metadata does not match the candidate source/version, personal team or valid certificate contract.')
  if (!report.checks || Object.keys(report.checks).length !== signingChecks.length || signingChecks.some(name => report.checks[name] !== true)) throw new Error('Mac signing report must pass every required signature, notarization and native acceptance check.')
  const notarizations = report.notarizations
  const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i
  if (!Array.isArray(notarizations) || notarizations.length !== 5 ||
      notarizations.some(item => !item || !uuid.test(item.id || '') || item.status !== 'Accepted') ||
      new Set(notarizations.map(item => item.id.toLowerCase())).size !== 5 ||
      notarizations.filter(item => item.kind === 'binaries' && item.arch === undefined).length !== 1 ||
      ['app', 'dmg'].some(kind => ['arm64', 'x64'].some(arch => notarizations.filter(item => item.kind === kind && item.arch === arch).length !== 1))) throw new Error('Mac signing report must contain exactly five distinct accepted binary, app and DMG notarizations.')
  const names = [...distributionNames.filter(name => name.startsWith('astral-darwin-')), ...desktopNames(version).filter(name => name.includes('-mac-'))]
  if (!Array.isArray(report.assets) || report.assets.length !== names.length ||
      new Set(report.assets.map(asset => asset?.name)).size !== names.length ||
      report.assets.some(asset => !asset || !names.includes(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0 || !/^[a-f0-9]{64}$/.test(asset.sha256 || ''))) throw new Error('Mac signing report must describe exactly the two Darwin binaries and four Mac packages.')
  for (const asset of report.assets) {
    const file = files.get(asset.name)
    if (!file || (await stat(file)).size !== asset.size || await digest(file) !== asset.sha256) throw new Error(`Mac signing report does not match final artifact bytes: ${asset.name}`)
  }
}

export async function digest(file) {
  const hash = createHash('sha256')
  for await (const chunk of createReadStream(file)) hash.update(chunk)
  return hash.digest('hex')
}

async function walk(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const name = path.join(directory, entry.name)
    if (entry.isSymbolicLink()) throw new Error(`Release inputs must not contain symbolic links: ${name}`)
    if (entry.isDirectory()) files.push(...await walk(name))
    else if (entry.isFile()) files.push(name)
    else throw new Error(`Unsupported release input: ${name}`)
  }
  return files
}

export async function collectCandidate({ input, output, sourceSHA, repository, runURL, ciRunURL, tag, macSigned = false }) {
  const version = await readReleaseVersion()
  validateTag(tag, version)
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('A full source commit SHA is required.')
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '')) throw new Error('A GitHub owner/repository is required.')
  for (const url of [runURL, ciRunURL]) {
    if (!url?.startsWith(`https://github.com/${repository}/actions/runs/`) || !/^\d+$/.test(url.split('/').at(-1))) throw new Error('Candidate and successful CI run URLs must identify this repository.')
  }
  if (path.resolve(input) === path.resolve(output) || path.resolve(output).startsWith(`${path.resolve(input)}${path.sep}`)) throw new Error('Output must be outside the input artifact directory.')
  await mkdir(output, { recursive: true })
  if ((await readdir(output)).length) throw new Error('Candidate output directory must be empty; refusing to mix earlier release files.')
  const required = [...distributionNames, ...desktopNames(version), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', ...(macSigned ? [macSigningReportName] : [])]
  const allowed = new Set([...required, 'checksums.txt', 'release-formats.json'])
  const inputs = new Map()
  for (const file of await walk(input)) {
    const name = path.basename(file)
    if (!allowed.has(name)) throw new Error(`Unexpected candidate input ${name}; this review-only candidate pipeline accepts only the expected artifacts.`)
    // Recalculate the build's pre-sign checksum manifest after all artifact processing.
    if (name === 'checksums.txt') continue
    if (inputs.has(name) && await digest(inputs.get(name)) !== await digest(file)) throw new Error(`Different artifact inputs share the name ${name}.`)
    inputs.set(name, file)
  }
  for (const name of [...required, 'release-formats.json']) {
    if (!inputs.has(name)) throw new Error(`Missing required release artifact: ${name}`)
    if ((await stat(inputs.get(name))).size === 0) throw new Error(`Empty release artifact: ${name}`)
  }
  const formats = JSON.parse(await readFile(inputs.get('release-formats.json'), 'utf8'))
  if (formats.schema_version !== 1 || formats.version !== version || formats.source_sha !== sourceSHA ||
      !Number.isSafeInteger(formats.host_schema) || formats.host_schema < 1 || !Number.isSafeInteger(formats.core_format) || formats.core_format < 1 ||
      formats.data_formats?.host !== formats.host_schema || formats.data_formats?.core !== formats.core_format ||
      formats.migration_status !== 'not-implemented' || typeof formats.core_module_version !== 'string') throw new Error('Release format metadata does not match the candidate source/version contract.')
  if (macSigned) await validateMacSigningReport(JSON.parse(await readFile(inputs.get(macSigningReportName), 'utf8')), { version, sourceSHA, files: inputs })
  for (const name of required) await copyFile(inputs.get(name), path.join(output, name))
  const records = []
  for (const name of required.sort()) records.push({ name, size: (await stat(path.join(output, name))).size, sha256: await digest(path.join(output, name)) })
  await writeFile(path.join(output, 'checksums.txt'), `${distributionNames.map(name => `${records.find(record => record.name === name).sha256}  ${name}`).join('\n')}\n`)
  records.push({ name: 'checksums.txt', size: (await stat(path.join(output, 'checksums.txt'))).size, sha256: await digest(path.join(output, 'checksums.txt')) })
  const manifest = {
    schema_version: 1,
    purpose: macSigned ? 'mac-signed-candidate' : 'unsigned-candidate',
    public_release_ready: false,
    version,
    tag,
    source_sha: sourceSHA,
    repository,
    candidate_run_url: runURL,
    ci_run_url: ciRunURL,
    data_formats: formats.data_formats,
    host_schema: formats.host_schema,
    core_format: formats.core_format,
    core_module_version: formats.core_module_version,
    migration_status: formats.migration_status,
    signing: { macos: macSigned ? 'signed-and-notarized' : 'not-signed-or-notarized', windows: 'not-signed', linux: 'not-signed' },
    desktop_updater: 'not-implemented',
    distribution_names: distributionNames,
    assets: records,
  }
  await writeFile(path.join(output, 'release-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
  const label = macSigned ? 'Mac signed candidate' : 'unsigned candidate'
  const macStatus = macSigned ? 'verified signed and notarized; final-byte evidence is in mac-signing-report.json' : 'not performed'
  const body = `# Astral ${version} — ${label}\n\nThis is a review-only Draft Release. It is not approved for public distribution.\n\n- Source: ${sourceSHA}\n- Required CI: ${ciRunURL}\n- Candidate packaging: ${runURL}\n- macOS signing and notarization: ${macStatus}. Windows and Linux signing: not performed.\n- Desktop one-click updating and production update metadata: not implemented; public-release gate remains pending.\n- Versioned data migrations and their rollback/backup acceptance: not implemented; public-release gate remains pending.\n- Only the runner's native architecture is exercised by ordinary CI; producing an architecture's installer does not establish native execution.\n\nThe six unified Server/Web UI/CLI/Daemon binaries and three-platform Desktop packages are collected here for review. Final checksums describe these exact candidate bytes. They do not establish publisher authenticity.\n\nDo not publish this draft or point the landing page, VPS installer, or updater at it. A public release requires the separate release gates described in docs/releasing.md.\n\n<!-- astral-${manifest.purpose}:${sourceSHA} -->\n`
  await writeFile(path.join(output, 'RELEASE_NOTES.md'), body)
  const finalFiles = (await readdir(output)).sort()
  const sums = []
  for (const name of finalFiles) sums.push(`${await digest(path.join(output, name))}  ${name}`)
  await writeFile(path.join(output, 'SHA256SUMS'), `${sums.join('\n')}\n`)
  return manifest
}

async function main() {
  const [command, ...args] = process.argv.slice(2)
  const option = name => { const index = args.indexOf(name); return index < 0 ? undefined : args[index + 1] }
  if (command !== 'candidate' || !option('--input') || !option('--output')) throw new Error('Usage: release-assets.mjs candidate --input DIR --output EMPTY_DIR --sha COMMIT --repository OWNER/REPO --tag vYYYY.M.N --run-url URL --ci-run-url URL [--mac-signed]')
  const manifest = await collectCandidate({ input: option('--input'), output: option('--output'), sourceSHA: option('--sha'), repository: option('--repository'), tag: option('--tag'), runURL: option('--run-url'), ciRunURL: option('--ci-run-url'), macSigned: args.includes('--mac-signed') })
  console.log(`Collected ${manifest.assets.length} assets for ${manifest.tag}; public_release_ready=false.`)
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
