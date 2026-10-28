import { createHash } from 'node:crypto'
import { createReadStream, readFileSync } from 'node:fs'
import { copyFile, mkdir, readdir, readFile, stat, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { desktopArtifactName, desktopUpdateFeeds, writeDesktopUpdateFeeds, validateDesktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { isDirectInvocation, readReleaseVersion, validateTag } from './release-version.mjs'
import { recipeHash, recipePath, validateLock } from './computer-image-contract.mjs'

export const distributionNames = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))

export function desktopNames(version) {
  return [['mac', ['dmg', 'zip']], ['win', ['exe', 'zip']], ['linux', ['AppImage', 'deb']]]
    .flatMap(([system, extensions]) => ['x64', 'arm64'].flatMap(arch => extensions.map(extension => desktopArtifactName(version, system, arch, extension))))
}

export const macSigningReportName = 'mac-signing-report.json'
const signingChecks = ['signatures', 'hardened_runtime', 'secure_timestamps', 'embedded_checksums', 'packaged_app_startup_shutdown']

export function validateDataFormats(formats, { version, sourceSHA }) {
  if (formats?.schema_version !== 1 || formats.version !== version || formats.source_sha !== sourceSHA ||
      !Number.isSafeInteger(formats.host_schema) || !Number.isSafeInteger(formats.core_format) ||
      formats.host_schema < 3 || formats.core_format < 11 ||
      formats.data_formats?.host !== formats.host_schema || formats.data_formats?.core !== formats.core_format || Object.keys(formats.data_formats).length !== 2 ||
      formats.migration_status !== 'goose' || typeof formats.core_module_version !== 'string' || !formats.core_module_version ||
      formats.migration_baselines?.host !== 3 || formats.migration_baselines?.core !== 11 || Object.keys(formats.migration_baselines).length !== 2 ||
      formats.migration_targets?.host !== formats.host_schema || formats.migration_targets?.core !== formats.core_format || Object.keys(formats.migration_targets).length !== 2) throw new Error('Release format metadata does not match the candidate source/version and Goose migration contract.')
  try {
    validateLock(formats.computer_environment, recipeHash(readFileSync(recipePath)))
  } catch (cause) {
    throw new Error('Release format metadata does not match the current Computer environment recipe and immutable image lock contract.', { cause })
  }
}

export async function validateMacSigningReport(report, { version, sourceSHA, files }) {
  const expires = report?.certificate_expires_at
  const expiryDate = typeof expires === 'string' ? new Date(expires) : new Date(NaN)
  const validExpiry = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(expires || '') &&
    Number.isFinite(expiryDate.getTime()) && expiryDate.toISOString() === (expires.includes('.') ? expires : expires.replace('Z', '.000Z')) && expiryDate.getTime() > Date.now()
  if (report?.schema_version !== 2 || report.version !== version || report.source_sha !== sourceSHA ||
      report.team_id !== '6N6W6Z6739' || report.status !== 'signed' || !validExpiry ||
      !['arm64', 'x64'].includes(report.native_tested_arch)) throw new Error('Mac signing report metadata does not match the candidate source/version, personal team or valid certificate contract.')
  if (!report.checks || Object.keys(report.checks).length !== signingChecks.length + 2 || signingChecks.some(name => report.checks[name] !== true)) throw new Error('Mac signing report must pass every required signature and native acceptance check.')
  const notarizations = report.notarizations
  const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i
  if (!Array.isArray(notarizations) || notarizations.length !== 3 ||
      notarizations.some(item => !item || !uuid.test(item.id || '') || !['Accepted', 'In Progress', 'Unknown'].includes(item.status)) ||
      new Set(notarizations.map(item => item.id.toLowerCase())).size !== 3 ||
      notarizations.filter(item => item.kind === 'binaries' && item.arch === undefined).length !== 1 ||
      ['arm64', 'x64'].some(arch => notarizations.filter(item => item.kind === 'dmg' && item.arch === arch).length !== 1)) throw new Error('Mac signing report must contain exactly three distinct non-rejected binary and DMG notarizations.')
  const accepted = notarizations.every(item => item.status === 'Accepted')
  if (report.notarization_status !== (accepted ? 'accepted' : 'pending') || report.checks.stapling !== accepted || report.checks.gatekeeper !== accepted) throw new Error('Mac notarization status and acceptance checks must accurately match every submission.')
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

export async function collectCandidate({ input, output, sourceSHA, repository, runURL, ciRunURL, tag, macSigned = false, publicRelease = false }) {
  if (publicRelease) macSigned = true
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
  const required = [...distributionNames, ...desktopNames(version), 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'install-server.sh', 'install-computer.sh', 'install-computer.ps1', 'release-formats.json', ...(macSigned ? [macSigningReportName] : [])]
  const allowed = new Set([...required, 'checksums.txt', 'release-formats.json'])
  const inputs = new Map()
  for (const file of await walk(input)) {
    const name = path.basename(file)
    if (!allowed.has(name)) throw new Error(`Unexpected ${publicRelease ? 'release' : 'candidate'} input ${name}; only the expected artifacts are accepted.`)
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
  validateDataFormats(formats, { version, sourceSHA })
  const signingReport = macSigned ? JSON.parse(await readFile(inputs.get(macSigningReportName), 'utf8')) : undefined
  if (macSigned) await validateMacSigningReport(signingReport, { version, sourceSHA, files: inputs })
  const notarized = signingReport?.notarization_status === 'accepted'
  for (const name of required) await copyFile(inputs.get(name), path.join(output, name))
  await writeDesktopUpdateFeeds(output, version)
  await validateDesktopUpdateFeeds(output, version)
  required.push(...desktopUpdateFeeds(version).map(feed => feed.name))
  const records = []
  for (const name of required.sort()) records.push({ name, size: (await stat(path.join(output, name))).size, sha256: await digest(path.join(output, name)) })
  await writeFile(path.join(output, 'checksums.txt'), `${distributionNames.map(name => `${records.find(record => record.name === name).sha256}  ${name}`).join('\n')}\n`)
  records.push({ name: 'checksums.txt', size: (await stat(path.join(output, 'checksums.txt'))).size, sha256: await digest(path.join(output, 'checksums.txt')) })
  const manifest = {
    schema_version: 1,
    purpose: publicRelease ? 'release' : macSigned ? 'mac-signed-candidate' : 'unsigned-candidate',
    public_release_ready: publicRelease,
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
    computer_environment: formats.computer_environment,
    migration_status: formats.migration_status,
    migration_baselines: formats.migration_baselines,
    migration_targets: formats.migration_targets,
    signing: { macos: macSigned ? (notarized ? 'signed-and-notarized' : 'signed-notarization-pending') : 'not-signed-or-notarized', windows: 'not-signed', linux: 'not-signed' },
    desktop_updater: 'electron-updater',
    distribution_names: distributionNames,
    assets: records,
  }
  await writeFile(path.join(output, 'release-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
  const label = macSigned ? 'Mac signed candidate' : 'unsigned candidate'
  const macStatus = macSigned ? (notarized ? 'verified signed and notarized' : 'verified signed; Apple notarization is pending') + '; final-byte evidence is in mac-signing-report.json' : 'not performed'
  const capabilities = `- macOS: install the DMG matching your Mac architecture. Developer ID signatures are verified. ${notarized ? 'Apple notarization is accepted.' : 'Apple notarization is pending; Gatekeeper may require manually allowing the app in System Settings → Privacy & Security.'} See mac-signing-report.json for exact final-byte evidence.
- Windows: use the EXE installer for a current-user installation. Windows packages are not code-signed and may show SmartScreen warnings; ZIP builds are for manual installation.
- Linux Desktop: AppImage supports in-app updating; install newer DEB packages manually. Linux packages are not code-signed.
- Linux VPS: run install-server.sh for a current-user headless Server and systemd user service.

Desktop checks for new versions without downloading packages. Use Settings → About & diagnostics → Update and restart to check Latest again, download and verify the package, then install and restart. You can keep using Astral during download; installation interrupts running tasks. Ordinary exit does not install updates. Remote Daemons can be updated from Computer settings or with the displayed update command; container updates retain the existing environment.

Versioned migrations are bundled with the latest binary. Upgrades apply missing migrations in order without downloading intermediate releases. Before a migration, the Server snapshots its data; migration failure stops startup and restores that snapshot. The current target is Host ${formats.host_schema} / Core ${formats.core_format}, starting from the supported Host 3 / Core 11 baseline. Older private data formats and downgrades are not supported. Desktop installation errors can be retried; a release which cannot start may require manually reinstalling a previous version after restoring its matching data backup.

- Source: ${sourceSHA}
- Required CI: ${ciRunURL}
- Release packaging: ${runURL}
- Native acceptance covers the runner architecture; cross-building an installer does not establish native execution on every architecture.

The six unified Server/Web UI/CLI/Daemon binaries and three-platform Desktop packages are included. checksums.txt covers the six binaries; SHA256SUMS covers all attached files except itself. Checksums describe exact release bytes and do not independently establish publisher authenticity.
`
  const body = publicRelease ? `# Astral ${version}

${capabilities}
<!-- astral-release:${sourceSHA} -->
`
    : `# Astral ${version} — ${label}

This is a review-only Draft Release. It is not approved for public distribution.

${capabilities}
- macOS signing and notarization: ${macStatus}.

<!-- astral-${manifest.purpose}:${sourceSHA} -->
`
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
  if (!['candidate', 'public'].includes(command) || !option('--input') || !option('--output')) throw new Error('Usage: release-assets.mjs <candidate|public> --input DIR --output EMPTY_DIR --sha COMMIT --repository OWNER/REPO --tag vMAJOR.MINOR.PATCH --run-url URL --ci-run-url URL [--mac-signed (candidate only; public always requires Mac signing)]')
  const manifest = await collectCandidate({ input: option('--input'), output: option('--output'), sourceSHA: option('--sha'), repository: option('--repository'), tag: option('--tag'), runURL: option('--run-url'), ciRunURL: option('--ci-run-url'), macSigned: args.includes('--mac-signed'), publicRelease: command === 'public' })
  console.log(`Collected ${manifest.assets.length} assets for ${manifest.tag}; public_release_ready=${manifest.public_release_ready}.`)
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
