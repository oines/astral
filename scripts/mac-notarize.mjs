import { execFile } from 'node:child_process'
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { promisify } from 'node:util'
import { desktopNames, digest, validateMacSigningReport } from './release-assets.mjs'
import { darwinNames, signingEnvironment, verifyApp, verifyBinaries } from './mac-signing.mjs'
import { isDirectInvocation, readReleaseVersion, repositoryRoot } from './release-version.mjs'

const execute = promisify(execFile)
const releaseDirectory = path.join(repositoryRoot, 'apps/desktop/release')
const reportDirectory = path.join(releaseDirectory, 'notarization')
const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i
const submissionStatuses = new Set(['Accepted', 'In Progress', 'Invalid', 'Rejected'])

async function command(program, args, options = {}) {
  if (process.platform !== 'darwin' || process.env.CI !== 'true') throw new Error('Notarization acceptance runs only on a disposable macOS CI runner.')
  return execute(program, args, { cwd: repositoryRoot, encoding: 'utf8', timeout: 120_000, maxBuffer: 8 << 20, ...options })
}

function context() {
  const signing = signingEnvironment()
  const sourceSHA = process.env.RELEASE_SOURCE_SHA
  const profile = process.env.ASTRAL_NOTARY_PROFILE
  const expiration = process.env.MAC_CERTIFICATE_EXPIRES_AT
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('RELEASE_SOURCE_SHA must identify the validated source commit.')
  if (!profile || /[\r\n\0]/.test(profile)) throw new Error('ASTRAL_NOTARY_PROFILE is required.')
  if (!expiration || !Number.isFinite(Date.parse(expiration)) || Date.parse(expiration) <= Date.now()) throw new Error('A current MAC_CERTIFICATE_EXPIRES_AT is required.')
  return { ...signing, sourceSHA, profile, expiration }
}

export function acceptedSubmission(value) {
  if (!uuid.test(value?.id || '') || value.status !== 'Accepted') throw new Error(`Apple notarization was not Accepted (${submissionStatuses.has(value?.status) ? value.status : 'missing or unknown status'}).`)
  return { id: value.id, status: 'Accepted' }
}

function submissionResponse(result, expectedID, requireStatus = false) {
  const response = JSON.parse(result?.stdout)
  if (typeof response?.id !== 'string' || !uuid.test(response.id)) throw new Error('Invalid Apple submission ID.')
  if (expectedID && response.id.toLowerCase() !== expectedID) throw Object.assign(new Error('Unexpected Apple submission ID.'), { code: 'ERR_NOTARY_ID' })
  if (requireStatus && !submissionStatuses.has(response.status)) throw new Error('Unknown Apple submission status.')
  return response
}

async function writeSubmissionDiagnostic(value) {
  await mkdir(reportDirectory, { recursive: true })
  await writeFile(path.join(reportDirectory, `${value.id}.json`), `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}

export async function submit(file, configuration, { run = command, record = writeSubmissionDiagnostic, log = console.log } = {}) {
  const auth = ['--keychain-profile', configuration.profile, '--keychain', configuration.keychain]
  let uploaded
  let submitSucceeded = false
  let submitExitCode = null
  try {
    uploaded = await run('xcrun', ['notarytool', 'submit', file, ...auth, '--no-wait', '--output-format', 'json'], { timeout: 600_000 })
    submitSucceeded = true
    submitExitCode = 0
  } catch (error) {
    // Apple can allocate an ID before the upload finishes. Keep it only for
    // diagnosis; a failed submit command must never proceed to wait or info.
    uploaded = { stdout: error?.stdout }
    if (Number.isSafeInteger(error?.code)) submitExitCode = error.code
  }
  let uploadResponse
  try { uploadResponse = submissionResponse(uploaded) }
  catch { throw new Error('Apple notarization upload did not return a valid submission ID; no artifacts are approved.') }
  const id = uploadResponse.id.toLowerCase()
  const diagnostic = { id, name: path.basename(file), upload_status: submitSucceeded ? 'completed' : 'unconfirmed', submit_exit_code: submitExitCode }
  const save = async response => {
    if (submitSucceeded) {
      if (submissionStatuses.has(response.status)) diagnostic.status = response.status
      if (typeof response.createdDate === 'string' && response.createdDate.length < 128 && Number.isFinite(Date.parse(response.createdDate))) {
        diagnostic.createdDate = new Date(response.createdDate).toISOString()
      }
    }
    // Never persist raw notarytool output, paths, authentication or child errors.
    try { await record({ ...diagnostic }) }
    catch { throw new Error('Could not save the Apple submission diagnostic; no artifacts are approved.') }
  }
  await save(uploadResponse)
  if (!submitSucceeded) {
    log(`Apple notarization ${id}: upload unconfirmed; submit command failed`)
    throw new Error('Apple notarization upload was not confirmed: the submit command failed; no waiting or approval was performed.')
  }
  log(`Apple notarization ${id}: upload CLI completed (exit 0)`)
  if (['Invalid', 'Rejected'].includes(uploadResponse.status)) return acceptedSubmission({ id, status: uploadResponse.status })

  let response
  try {
    const result = await run('xcrun', ['notarytool', 'wait', id, ...auth, '--timeout', '2m', '--output-format', 'json'], { timeout: 180_000 })
    response = submissionResponse(result, id, true)
  } catch (error) {
    if (error?.code === 'ERR_NOTARY_ID') throw error
    // Query this exact submission once after an interrupted or unusable wait.
  }
  if (!response || response.status === 'In Progress') {
    try {
      const result = await run('xcrun', ['notarytool', 'info', id, ...auth, '--output-format', 'json'])
      response = submissionResponse(result, id, true)
    } catch (error) {
      if (error?.code === 'ERR_NOTARY_ID') throw error
      // The saved upload ID remains available for a later read-only query.
    }
  }
  if (!response) {
    log(`Apple notarization ${id}: status unknown after confirmed upload; releasing signed artifacts with notarization pending`)
    return { id, status: 'Unknown' }
  }
  await save(response)
  log(`Apple notarization ${id}: ${response.status}`)
  if (response.status === 'In Progress') return { id, status: 'In Progress' }
  return acceptedSubmission({ id, status: response.status })
}

export async function staple(target, { run = command, pause = delay } = {}) {
  // An Accepted submission's ticket may take a moment to reach the CDN.
  for (let attempt = 0; ; attempt++) {
    try { await run('xcrun', ['stapler', 'staple', target]); break }
    catch (error) {
      if (attempt >= 4) throw error
      await pause((attempt + 1) * 20_000)
    }
  }
  await run('xcrun', ['stapler', 'validate', target])
}

export async function verifyNotarizedApp(app, arch, { stapled = true, notarized = true, run = command, inspect = verifyApp } = {}) {
  await inspect(app)
  const architecture = await run('/usr/bin/lipo', ['-archs', path.join(app, 'Contents/MacOS/Astral')])
  if (architecture.stdout.trim() !== (arch === 'x64' ? 'x86_64' : 'arm64')) throw new Error(`Unexpected packaged App architecture: ${arch}`)
  if (stapled && notarized) await run('xcrun', ['stapler', 'validate', app])
  if (notarized) await run('/usr/sbin/spctl', ['--assess', '--type', 'execute', '--verbose=2', app])
}

async function asset(file) {
  return { name: path.basename(file), size: (await stat(file)).size, sha256: await digest(file) }
}

async function writeReport(name, value) {
  await mkdir(reportDirectory, { recursive: true })
  await writeFile(path.join(reportDirectory, `${name}.json`), `${JSON.stringify(value, null, 2)}\n`)
}

async function notarizeBinaries(directory) {
  const configuration = context()
  await verifyBinaries(directory)
  const temporary = await mkdtemp(path.join(process.env.RUNNER_TEMP || tmpdir(), 'astral-notary-binaries-'))
  try {
    const source = path.join(temporary, 'Astral CLI')
    await mkdir(source)
    for (const name of darwinNames) {
      await copyFile(path.join(directory, name), path.join(source, name))
      await chmod(path.join(source, name), 0o755)
    }
    const archive = path.join(temporary, 'astral-darwin.zip')
    await command('/usr/bin/ditto', ['-c', '-k', '--keepParent', source, archive])
    const notarization = { kind: 'binaries', ...await submit(archive, configuration) }
    await writeReport('binaries', {
      version: await readReleaseVersion(), source_sha: configuration.sourceSHA,
      notarizations: [notarization], assets: await Promise.all(darwinNames.map(name => asset(path.join(directory, name)))),
    })
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

async function notarizeApp(app, arch) {
  if (!['x64', 'arm64'].includes(arch)) throw new Error('App architecture must be x64 or arm64.')
  const configuration = context()
  const version = await readReleaseVersion()
  await verifyApp(app)
  const temporary = await mkdtemp(path.join(process.env.RUNNER_TEMP || tmpdir(), 'astral-notary-app-'))
  try {
    // Apple issues tickets for both the outer container and its enclosed App.
    // Submit the signed DMG once, then staple that ticket to the DMG and the
    // original App before producing the final ZIP, as Magpie does.
    // https://developer.apple.com/documentation/security/customizing-the-notarization-workflow
    const zip = path.join(releaseDirectory, `Astral-${version}-mac-${arch}.zip`)
    const dmg = path.join(releaseDirectory, `Astral-${version}-mac-${arch}.dmg`)
    // --prepackaged preserves the signed App and its embedded CLI hashes.
    await command('pnpm', ['--filter', '@astral/desktop', 'exec', 'electron-builder', '--config', 'electron-builder.release.cjs', '--prepackaged', app, '--mac', 'dmg', `--${arch}`, '--publish', 'never'], { timeout: 600_000 })
    await command('/usr/bin/codesign', ['--force', '--sign', configuration.identity, '--keychain', configuration.keychain, '--timestamp', dmg])
    await command('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', dmg])
    const dmgSignature = await command('/usr/bin/codesign', ['--display', '--verbose=4', dmg])
    const dmgFields = `${dmgSignature.stdout}\n${dmgSignature.stderr}`.split('\n')
    if (!dmgFields.includes(`Authority=${configuration.identity}`) || !dmgFields.includes(`TeamIdentifier=${configuration.team}`) || !dmgFields.some(line => /^Timestamp=.+/.test(line))) throw new Error('The DMG does not carry the intended timestamped Developer ID signature.')
    const dmgSubmission = { kind: 'dmg', arch, ...await submit(dmg, configuration) }
    const notarized = dmgSubmission.status === 'Accepted'
    if (notarized) {
      await staple(dmg)
      await staple(app)
      await command('/usr/sbin/spctl', ['--assess', '--type', 'open', '--context', 'context:primary-signature', '--verbose=2', dmg])
    }
    await verifyNotarizedApp(app, arch, { notarized })
    await command('/usr/bin/ditto', ['-c', '-k', '--sequesterRsrc', '--keepParent', app, zip])

    // Re-open the bytes users download. This also catches packaging an old App
    // despite successful validation of the unpacked build directory.
    const extracted = path.join(temporary, 'extracted')
    await command('/usr/bin/ditto', ['-x', '-k', zip, extracted])
    const extractedApp = path.join(extracted, 'Astral.app')
    await verifyNotarizedApp(extractedApp, arch, { notarized })
    const nativeTested = arch === process.arch
    if (nativeTested) {
      await command(process.execPath, ['scripts/check-packaged-desktop.mjs'], {
        env: { ...process.env, ASTRAL_PACKAGED_APP_EXECUTABLE: path.join(extractedApp, 'Contents/MacOS/Astral') }, timeout: 180_000,
      })
    }
    const mount = path.join(temporary, 'mounted-dmg')
    await mkdir(mount)
    await command('/usr/bin/hdiutil', ['attach', '-nobrowse', '-readonly', '-mountpoint', mount, dmg])
    // The mounted copy predates App stapling. Its outer DMG has already passed
    // stapler validation; require the enclosed App's signature and Gatekeeper
    // acceptance without claiming that this copy carries its own ticket.
    try { await verifyNotarizedApp(path.join(mount, 'Astral.app'), arch, { stapled: false, notarized }) }
    finally { await command('/usr/bin/hdiutil', ['detach', mount]) }
    await writeReport(arch, {
      version, source_sha: configuration.sourceSHA, arch, native_tested: nativeTested,
      notarizations: [dmgSubmission], assets: [await asset(zip), await asset(dmg)],
    })
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

async function finalize() {
  const configuration = context()
  const version = await readReleaseVersion()
  const reports = await Promise.all(['binaries', 'x64', 'arm64'].map(async name => JSON.parse(await readFile(path.join(reportDirectory, `${name}.json`), 'utf8'))))
  if (reports.some(report => report.version !== version || report.source_sha !== configuration.sourceSHA)) throw new Error('Notarization reports do not describe the current source/version.')
  if (reports.filter(report => report.native_tested === true).length !== 1 || !reports.some(report => report.arch === process.arch && report.native_tested)) throw new Error('The final native ZIP App has not passed startup/shutdown acceptance.')
  const notarized = reports.flatMap(report => report.notarizations).every(item => item.status === 'Accepted')
  const report = {
    schema_version: 2, version, source_sha: configuration.sourceSHA, team_id: configuration.team,
    certificate_expires_at: configuration.expiration, status: 'signed', notarization_status: notarized ? 'accepted' : 'pending', native_tested_arch: process.arch,
    checks: { signatures: true, hardened_runtime: true, secure_timestamps: true, embedded_checksums: true, stapling: notarized, gatekeeper: notarized, packaged_app_startup_shutdown: true },
    notarizations: reports.flatMap(report => report.notarizations), assets: reports.flatMap(report => report.assets),
  }
  const files = new Map([
    ...darwinNames.map(name => [name, path.join(repositoryRoot, 'dist/bin', name)]),
    ...desktopNames(version).filter(name => name.includes('-mac-')).map(name => [name, path.join(releaseDirectory, name)]),
  ])
  await validateMacSigningReport(report, { version, sourceSHA: configuration.sourceSHA, files })
  await writeFile(path.join(releaseDirectory, 'mac-signing-report.json'), `${JSON.stringify(report, null, 2)}\n`)
  console.log(`Mac signatures, final DMG/ZIP checks and native App startup/shutdown passed. Apple notarization: ${report.notarization_status}.`)
}

async function main() {
  const [operation, target, arch, ...extra] = process.argv.slice(2)
  if (extra.length) throw new Error('Unexpected notarization arguments.')
  if (operation === 'binaries' && target && !arch) await notarizeBinaries(path.resolve(target))
  else if (operation === 'app' && target && arch) await notarizeApp(path.resolve(target), arch)
  else if (operation === 'finalize' && !target) await finalize()
  else throw new Error('Usage: mac-notarize.mjs binaries DIST | app APP x64|arm64 | finalize')
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
