import { createHash } from 'node:crypto'
import { createReadStream, readFileSync } from 'node:fs'
import { copyFile, mkdir, readdir, readFile, stat, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { desktopArtifactName, desktopUpdateFeeds, writeDesktopUpdateFeeds, validateDesktopUpdateFeeds } from './desktop-update-feeds.mjs'
import { validateStorageMigrations, isDirectInvocation, readReleaseVersion, validateTag } from './release-version.mjs'
import { recipeHash, recipePath, validateLock } from './computer-image-contract.mjs'
import { validateBuildMetadata } from './rolling-release-version.mjs'

export const distributionNames = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))

export function desktopNames(version) {
  return [['mac', ['dmg', 'zip']], ['win', ['exe', 'zip']], ['linux', ['AppImage', 'deb']]]
    .flatMap(([system, extensions]) => ['x64', 'arm64'].flatMap(arch => extensions.map(extension => desktopArtifactName(version, system, arch, extension))))
}

export const macSigningReportName = 'mac-signing-report.json'
const signingChecks = ['signatures', 'hardened_runtime', 'secure_timestamps', 'embedded_checksums', 'packaged_app_startup_shutdown']

export function validateDataFormats(formats, { version, sourceSHA }) {
  validateStorageMigrations(formats)
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
  const nativeEvidence = report?.schema_version === 2
    ? ['arm64', 'x64'].includes(report.native_tested_arch)
    : report?.schema_version === 3 && Array.isArray(report.native_tested_arches) && report.native_tested_arches.length === 2 &&
      new Set(report.native_tested_arches).size === 2 && ['arm64', 'x64'].every(arch => report.native_tested_arches.includes(arch)) && !Object.hasOwn(report, 'native_tested_arch')
  if (!nativeEvidence || report.version !== version || report.source_sha !== sourceSHA ||
      report.team_id !== '6N6W6Z6739' || report.status !== 'signed' || !validExpiry ||
      ![2, 3].includes(report.schema_version)) throw new Error('Mac signing report metadata does not match the candidate source/version, personal team or valid certificate contract.')
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

export async function collectCandidate({ input, output, sourceSHA, repository, runURL, ciRunURL, tag, buildMetadata, macSigned = false, publicRelease = false, environment = process.env }) {
  if (publicRelease) macSigned = true
  const version = await readReleaseVersion(undefined, environment)
  validateTag(tag, version)
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('A full source commit SHA is required.')
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '')) throw new Error('A GitHub owner/repository is required.')
  for (const url of [runURL, ciRunURL]) {
    if (!url?.startsWith(`https://github.com/${repository}/actions/runs/`) || !/^\d+$/.test(url.split('/').at(-1))) throw new Error('Candidate and successful CI run URLs must identify this repository.')
  }
  if (buildMetadata !== undefined) {
    validateBuildMetadata(buildMetadata, { version, sourceSHA, repository })
    if (!publicRelease || ciRunURL !== runURL) throw new Error('Rolling release collection requires final public metadata and the protected source-check and packaging run.')
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
  if (buildMetadata !== undefined && (signingReport?.schema_version !== 3 || signingReport.notarization_status !== 'accepted')) throw new Error('Rolling releases require both native Mac architectures and every Apple notarization accepted before sealing.')
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
    ...(buildMetadata === undefined ? {} : { build_provenance: buildMetadata, packaging_run_url: runURL }),
    data_formats: formats.data_formats,
    host_schema: formats.host_schema,
    core_format: formats.core_format,
    core_schema: formats.core_schema,
    storage_migrations: formats.storage_migrations,
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
  const hostMigration = formats.host_schema >= 4 ? 'The Host 3 → 4 migration restores visibility of Sessions archived while still bound to IM. It appends an unarchived current revision while preserving every earlier revision, IM event and Core history entry; ordinary archived Sessions stay archived. Desktop keeps its existing schema-3-core-11/ data directory.' : 'The supported public data baseline is Host 3 / Core 11.'
  const capabilities = `Archiving now seals a conversation and its subagents: running work stops, driving inputs are cleared, and fixed-target schedules and event endpoints are disabled. History, export and independent forks remain available. Restore unlocks the conversation without continuing old work or re-enabling automation. Archived targets reject new schedules, event endpoints and delivery; chat binding requires an explicit restore first.

The sidebar shows clock and lightning markers together. Open a conversation's three-dot menu to find its automatic tasks at the top; selecting a name opens the matching Bot and task editor. A conversation remains in Now with a blue dot while any of its subagents are working, even when the original agent is idle.

Permanently deleting a Computer also removes its event endpoints and events not yet accepted into conversation context. Mixed batches retain other Computers' events, and accepted conversation context remains intact. Deletion and queue cleanup commit together; old callbacks and late UI responses cannot restore removed work. Computer work files, containers and the Daemon service are retained.

Files and read-only subagent conversations now share a tabbed right-hand workspace. Chat and the workspace sit side by side with a draggable divider; the workspace overlays chat only when there is not enough room for both. Files opens directory browsing directly on the current session's computer and working directory; selecting a file follows its source computer. The preview and directory divider is adjustable too. Text and code previews support copying contents; partial previews clearly copy only the loaded excerpt, while downloads provide the complete file. Desktop can reveal verified local files in the system file manager; remote and container files stay within Astral's browsing and download controls.

Probe can switch from a fixed model back to Follow the Session’s current model, including the session's current reasoning settings.

Bots now support an ordered model list with separate reasoning choices. Sessions try their preferred model first, then the Bot's alternatives; main sessions and subagents use the same recovery rules. Retryable failures allow up to five total attempts per model, including the first; quota and authorization failures move directly to the next candidate. Optional automatic restoration is off by default and checks the preferred model through real Models SDK requests before returning at a safe request boundary. Checks may incur charges. If all candidates fail with restoration enabled, the task waits for recovery.

Reorder model priorities with drag handles or keyboard controls, using the same model-and-reasoning picker as conversations. Apply to unarchived sessions makes this Bot's unarchived conversations and their subagents prefer the first model without restarting stopped work. Archived trees keep their saved model choices, including after restoration.

The sidebar's Bot menu now includes Manage Bot, opening the selected Bot's settings. The conversation's Computer menu includes Manage Computers, opening that conversation's Bot computer permissions.

Model-request recovery avoids repeatedly serializing and copying complete image-bearing requests through nested model wrappers. Compact request freezing also avoids building an all-history JSON digest index for a small unmatched request tail, using the existing bounded literal representation. Actual recovery, retry limits and request isolation are preserved; no cache or storage format is introduced.

Core prepares new inline images once using the native Go VPX encoder. Ordinary read-tool images use quality 75 and a 150,000-base64-byte compression threshold; IM previews use quality 30 and a 30,000-byte threshold. Fitting small originals keep their exact bytes. Original dimensions are preserved unless the caller explicitly requests a size limit, and there is no outer loop to retry quality or resize to a fixed byte count. Final sizes vary with image content. Probe, ordinary requests, compaction and replay reuse the frozen result. Original downloads and existing recorded history are preserved.

Read-tool images enter the model context and persistent tool history directly as native image data. The Host no longer uploads another copy to the Computer or creates a file reference before returning a read result. Historical image previews continue to use the recorded tool result, including after the original file or Computer is unavailable. User-uploaded attachments keep their existing file-reference and input-image handling.

IM attachment and resource content endpoints support a single byte range with HTTP 206 and Content-Range when the source size is known; unsatisfiable or unsupported multi-range requests return 416. Unknown-size sources continue to return the complete content with HTTP 200. Server and the calling Computer's Daemon both need Range support. A range may be served by rereading and discarding the source prefix rather than a persistent cache or native upstream resume.

New local containers created from the Server use a verified Docker host route instead of configured public access URLs. The Server checks the route from the container and verifies its pinned identity; it reports failure if no local route works. Explicit CLI connections to a remote Server keep their selected remote address. Existing containers retain their saved address until it is explicitly changed.

Bots can delegate long-running or parallel work to persistent subagents, including handing off work after it has already started. Each subagent has a stable name chosen by its parent or a generated code, independent of its first task. New delegation supports one level below the original conversation: root → subagent. Only the original user-facing root can create subagents; subagents cannot delegate further. Deeper subagents created by earlier versions remain available in their original positions. Subagents report progress to their direct parent with the original event time in Astral's configured timezone. Both a normally ending round and a terminal run failure notify and trigger that parent, which can review the result or error, choose another model, retry or assign more work. Model fallback and waiting for preferred-model recovery do not send terminal notifications. These runtime events do not classify business-task success.

Root instructions call for responsive coordination and independently actionable delegation; subagents are instructed to report meaningful progress and blockers. When no useful independent work remains, they are instructed to end the current round rather than poll or keep it alive waiting for outside action; normal waits for executing commands and bounded checks remain allowed.

Select a subagent bubble in the conversation to inspect its full conversation and tool activity in the same right-hand workspace. For deeper subagents created by earlier versions, follow their nested bubbles and switch between conversations through tabs without losing your original draft or reading position. Subagents remain reusable after a round ends and do not appear as separate conversations in the main sidebar.

Expanded tool results show image thumbnails that open in the shared viewer. Subagent-management details show the actual operation, target, input, model and reasoning selection, and returned status or error. Message-send details show the historical destination, body, attachments, reply and confirmed, failed or unconfirmed result. These views work in both the original conversation and read-only subagent panels; inspecting a historical call does not execute it again.

Changing the model or reasoning settings in the original conversation updates all existing subagents, including subtasks that have not started. A parent can also change one direct child's model and reasoning for later work without changing that child's descendants. A read-only model directory lists enabled, locally available profiles and their supported reasoning choices. Requests already sent keep their original settings; later requests use the new choice. Subagents default to their parent's current computer and working directory. Inherited conversation history stays unchanged, and only the original user-facing conversation receives new messaging-platform context or can send messages to its bound chat.

Permanently deleting a conversation or delegated subtask also deletes its complete subagent subtree, including independent histories. Computer working files and external platform messages remain. Subagent privacy follows the original conversation, and their storage usage is included with that conversation rather than shown as separately managed rows.

Linux Server installation prepares the current user's systemd service and linger when needed, collects Web administrator credentials, and registers the astral command. The installer explains any required sudo step while continuing to run the Server as the original user. Use install-server.sh --uninstall to remove only that installation's service and program; data, identity, backups and recovery files are retained.

The CLI discovers registered installations and provides administrator setup, authentication status, credential reset and hostname recovery commands without requiring users to reconstruct internal data paths.

Standalone Servers allow remote Web management by default. Hostname restrictions and their allowlist are edited and saved together, with the current access hostname retained when restrictions are enabled. Desktop keeps its separate remote-management control. Account and hostname changes use explicit local actions, with failures retaining the current draft.

Computer settings distinguish a missing Docker command, an unreachable engine and insufficient permissions, show the actual Server account, and provide a retry action. Feishu setup guidance links to the external permissions and event configuration required to complete a connection.

Narrow chat panels use a two-layer Composer with one frame, directly accessible Computer and model selection, and a stable send/stop position. The FollowUp queue uses compact continuous rows. Message images and files appear above the separate text bubble; multiple images use two columns and file names wrap within the message. Clicking an image opens a full-page viewer with zoom, pan, multi-image navigation and download, including trackpad and phone gestures. Unavailable originals retain their historical preview and preview download. Ordinary files continue to open in the file sidebar.

Slow network sends show a centered spinner in place of the button icon only after 300ms. Repeated Enter does not submit again while the request is pending. A failed send retains the draft, and an unchanged retry reuses the original input identity.

Deleting a search service turns off web search for all Bots that reference it, with the affected Bots shown before confirmation. Old unavailable search references no longer block unrelated Bot settings, while newly selected search services must exist.

Official Server and Computer downloads and updates consistently use the configured HTTPS GitHub release proxy. This does not redirect unrelated API traffic or the Desktop updater.

The bilingual documentation is rewritten around user tasks, external preparation, platform setup and troubleshooting. It removes button-by-button instructions already expressed by the UI and separates advanced deployment and API reference material.

- macOS: install the DMG matching your Mac architecture. Developer ID signatures are verified. ${notarized ? 'Apple notarization is accepted.' : 'Apple notarization is pending; Gatekeeper may require manually allowing the app in System Settings → Privacy & Security.'} See mac-signing-report.json for exact final-byte evidence.
- Windows: use the EXE installer for a current-user installation. Windows packages are not code-signed and may show SmartScreen warnings; ZIP builds are for manual installation.
- Linux Desktop: AppImage supports in-app updating; install newer DEB packages manually. Linux packages are not code-signed.
- Linux VPS: run install-server.sh for a current-user headless Server and systemd user service.

Desktop checks for new versions without downloading packages. Use Settings → About & diagnostics → Update and restart to check Latest again, download and verify the package, then install and restart. You can keep using Astral during download; installation interrupts running tasks. Ordinary exit does not install updates. Remote Daemons can be updated from Computer settings or with the displayed update command; container updates retain the existing environment.

Versioned migrations are bundled with the latest binary. Upgrades apply missing migrations in order without downloading intermediate releases. Before a migration, the Server snapshots its data; migration failure stops startup and restores that snapshot. The current target is Host ${formats.host_schema} / Core ${formats.core_format} / SQLite schema ${formats.core_schema}, starting from the supported Host 3 / Core 11 baseline. ${hostMigration} Older private data formats and downgrades are not supported. Desktop installation errors can be retried; a release which cannot start may require manually reinstalling a previous version after restoring its matching data backup.

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
  if (!['candidate', 'public'].includes(command) || !option('--input') || !option('--output')) throw new Error('Usage: release-assets.mjs <candidate|public> --input DIR --output EMPTY_DIR --sha COMMIT --repository OWNER/REPO --tag vMAJOR.MINOR.PATCH --run-url URL --ci-run-url URL [--build-metadata FILE] [--mac-signed (candidate only; public always requires Mac signing)]')
  const buildMetadata = option('--build-metadata') === undefined ? undefined : JSON.parse(await readFile(option('--build-metadata'), 'utf8'))
  const manifest = await collectCandidate({ input: option('--input'), output: option('--output'), sourceSHA: option('--sha'), repository: option('--repository'), tag: option('--tag'), runURL: option('--run-url'), ciRunURL: option('--ci-run-url'), buildMetadata, macSigned: args.includes('--mac-signed'), publicRelease: command === 'public' })
  console.log(`Collected ${manifest.assets.length} assets for ${manifest.tag}; public_release_ready=${manifest.public_release_ready}.`)
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
