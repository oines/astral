import { execFile } from 'node:child_process'
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { promisify } from 'node:util'
import { desktopNames, digest, validateMacSigningReport } from './release-assets.mjs'
import { darwinNames, personalTeam, signingEnvironment, verifyApp, verifyBinaries } from './mac-signing.mjs'
import { isDirectInvocation, readReleaseVersion, repositoryRoot, validateVersion } from './release-version.mjs'

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
  if (process.platform !== 'darwin' || process.env.CI !== 'true') throw new Error('Notarization runs only on a disposable macOS CI runner.')
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

export async function waitForSubmission(id, configuration, { run = command, record = async () => {}, log = console.log, pause = delay, now = Date.now, attempts = 15, timeout = 30 * 60_000 } = {}) {
  if (!uuid.test(id || '') || !Number.isSafeInteger(attempts) || attempts < 1 || attempts > 15 || !Number.isSafeInteger(timeout) || timeout < 1 || timeout > 30 * 60_000) throw new Error('A valid submission ID and a bounded notarization wait are required.')
  id = id.toLowerCase()
  const auth = ['--keychain-profile', configuration.profile, '--keychain', configuration.keychain]
  const deadline = now() + timeout
  for (let attempt = 0; attempt < attempts && now() < deadline; attempt++) {
    let response
    try {
      const remaining = deadline - now()
      if (remaining <= 0) break
      const result = await run('xcrun', ['notarytool', 'wait', id, ...auth, '--timeout', '2m', '--output-format', 'json'], { timeout: Math.min(180_000, remaining) })
      response = submissionResponse(result, id, true)
    } catch (error) {
      if (error?.code === 'ERR_NOTARY_ID') throw error
      // An expired wait or an interrupted status query does not authorize a
      // second upload. Every subsequent query stays bound to this exact ID.
    }
    if ((!response || response.status === 'In Progress') && now() < deadline) {
      try {
        const result = await run('xcrun', ['notarytool', 'info', id, ...auth, '--output-format', 'json'], { timeout: Math.min(120_000, deadline - now()) })
        response = submissionResponse(result, id, true)
      } catch (error) {
        if (error?.code === 'ERR_NOTARY_ID') throw error
      }
    }
    if (response) {
      await record(response)
      log(`Apple notarization ${id}: ${response.status}`)
      if (response.status !== 'In Progress') return acceptedSubmission({ id, status: response.status })
    }
    if (attempt + 1 < attempts && now() < deadline) await pause(Math.min((attempt + 1) * 1_000, 15_000, deadline - now()))
  }
  throw new Error(`Apple notarization ${id} is not Accepted after the bounded wait; the saved submission and input remain available for resume. No release is approved.`)
}

export async function submit(file, configuration, { run = command, record = writeSubmissionDiagnostic, log = console.log, fingerprint = digest, ...waitOptions } = {}) {
  const auth = ['--keychain-profile', configuration.profile, '--keychain', configuration.keychain]
  let artifactSHA256
  try { artifactSHA256 = await fingerprint(file) } catch { throw new Error('The notarization input could not be fingerprinted.') }
  if (!/^[a-f0-9]{64}$/.test(artifactSHA256 || '')) throw new Error('The notarization input must have a valid SHA256.')
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
  const diagnostic = { id, name: path.basename(file), artifact_sha256: artifactSHA256, upload_status: submitSucceeded ? 'completed' : 'unconfirmed', submit_exit_code: submitExitCode }
  if (/^[a-f0-9]{40}$/.test(configuration.sourceSHA || '')) diagnostic.source_sha = configuration.sourceSHA
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

  return waitForSubmission(id, configuration, { ...waitOptions, run, record: save, log })
}

export async function resumeSubmission(file, receipt, configuration, { fingerprint = digest, record = writeSubmissionDiagnostic, ...options } = {}) {
  if (!uuid.test(receipt?.id || '') || receipt.upload_status !== 'completed' || receipt.submit_exit_code !== 0 ||
      receipt.name !== path.basename(file) || !/^[a-f0-9]{64}$/.test(receipt.artifact_sha256 || '') || receipt.source_sha !== configuration.sourceSHA) {
    throw new Error('Resume requires the confirmed upload receipt for this source and input file.')
  }
  let actual
  try { actual = await fingerprint(file) } catch { throw new Error('The saved notarization input could not be fingerprinted.') }
  if (actual !== receipt.artifact_sha256) throw new Error('The notarization input changed; refusing to reuse its Apple submission ID.')
  const diagnostic = {
    id: receipt.id.toLowerCase(), name: receipt.name, artifact_sha256: actual,
    upload_status: 'completed', submit_exit_code: 0,
    ...(receipt.source_sha === undefined ? {} : { source_sha: receipt.source_sha }),
  }
  return waitForSubmission(diagnostic.id, configuration, {
    ...options,
    record: async response => {
      diagnostic.status = response.status
      if (typeof response.createdDate === 'string' && response.createdDate.length < 128 && Number.isFinite(Date.parse(response.createdDate))) diagnostic.createdDate = new Date(response.createdDate).toISOString()
      try { await record({ ...diagnostic }) } catch { throw new Error('Could not save the Apple submission diagnostic; no artifacts are approved.') }
    },
  })
}

async function runStapler(target, phases, { run = command, pause = delay, log = console.log } = {}) {
  const output = value => typeof value === 'string' ? value.slice(0, 16_384) + (value.length > 16_384 ? '\n[truncated]' : '') : ''
  const record = (attempt, phase, result, failed = false) => {
    const code = failed ? result?.code : 0
    // Only stapler is logged here: its arguments contain a file, never notary
    // credentials. Do not serialize child errors, messages or environment data.
    log(`Stapler diagnostic: ${JSON.stringify({
      target, attempt, phase,
      exit_code: Number.isSafeInteger(code) || typeof code === 'string' && /^[A-Z_0-9]{1,64}$/.test(code) ? code : null,
      stdout: output(result?.stdout), stderr: output(result?.stderr),
    })}`)
  }
  // Validation also contacts Apple's ticketing service. Bound retries without
  // changing which checks must pass or modifying final extracted ZIP bytes.
  for (let attempt = 0; ; attempt++) {
    try {
      for (const phase of phases) {
        let result
        try { result = await run('xcrun', ['stapler', phase, target]) }
        catch (error) { record(attempt + 1, phase, error, true); throw error }
        record(attempt + 1, phase, result)
      }
      return
    }
    catch (error) {
      if (attempt >= 4) throw error
      await pause((attempt + 1) * 20_000)
    }
  }
}

export async function staple(target, options) {
  // A successful staple alone never approves the artifact: each attempt must
  // both attach and validate its ticket.
  await runStapler(target, ['staple', 'validate'], options)
}

export async function verifyNotarizedApp(app, arch, { stapled = true, notarized = true, run = command, inspect = verifyApp, pause = delay, log = console.log } = {}) {
  await inspect(app)
  const architecture = await run('/usr/bin/lipo', ['-archs', path.join(app, 'Contents/MacOS/Astral')])
  if (architecture.stdout.trim() !== (arch === 'x64' ? 'x86_64' : 'arm64')) throw new Error(`Unexpected packaged App architecture: ${arch}`)
  if (stapled && notarized) await runStapler(app, ['validate'], { run, pause, log })
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
    // Keep the exact submitted archive beside its receipt when Apple remains
    // unavailable. A later explicit resume must use these bytes, not a rebuilt ZIP.
    await mkdir(reportDirectory, { recursive: true })
    const archive = path.join(reportDirectory, 'astral-darwin.zip')
    await command('/usr/bin/ditto', ['-c', '-k', '--keepParent', source, archive])
    const notarization = { kind: 'binaries', ...await submit(archive, configuration) }
    await writeReport('binaries', {
      version: await readReleaseVersion(), source_sha: configuration.sourceSHA,
      team_id: configuration.team, certificate_expires_at: configuration.expiration,
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
      team_id: configuration.team, certificate_expires_at: configuration.expiration,
      notarizations: [dmgSubmission], assets: [await asset(zip), await asset(dmg)],
    })
  } finally { await rm(temporary, { recursive: true, force: true }) }
}

export function combineNotarizationReports(reports, { version, sourceSHA, now = Date.now() }) {
  validateVersion(version)
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '') || !Array.isArray(reports) || reports.length !== 3 || reports.some(report => !report || report.version !== version || report.source_sha !== sourceSHA)) throw new Error('Notarization reports do not describe the current source/version.')
  const binaries = reports.filter(report => report.arch === undefined)
  const appReports = ['x64', 'arm64'].map(arch => {
    const matching = reports.filter(report => report.arch === arch)
    if (matching.length !== 1 || matching[0].native_tested !== true) throw new Error(`The final ${arch} ZIP App has not passed native startup/shutdown acceptance.`)
    return matching[0]
  })
  if (binaries.length !== 1 || binaries[0].native_tested !== undefined) throw new Error('Exactly one independent binary notarization report is required.')
  const ordered = [...binaries, ...appReports]
  const expiration = binaries[0].certificate_expires_at
  if (!Number.isFinite(Date.parse(expiration)) || Date.parse(expiration) <= now || ordered.some(report => report.team_id !== personalTeam || report.certificate_expires_at !== expiration)) throw new Error('Notarization reports must use the same current Developer ID certificate and personal team.')
  const submissions = ordered.flatMap(report => report.notarizations || [])
  if (submissions.length !== 3 || new Set(submissions.map(item => item.id?.toLowerCase())).size !== 3 ||
      submissions[0].kind !== 'binaries' || submissions[0].arch !== undefined || appReports.some((report, index) => report.notarizations?.length !== 1 || submissions[index + 1].kind !== 'dmg' || submissions[index + 1].arch !== report.arch)) throw new Error('Exactly one binary and two architecture-specific DMG notarizations are required.')
  for (const submission of submissions) acceptedSubmission(submission)
  const expectedNames = [...darwinNames, ...desktopNames(version).filter(name => name.includes('-mac-'))]
  const assets = ordered.flatMap(report => report.assets || [])
  if (assets.length !== expectedNames.length || new Set(assets.map(item => item.name)).size !== expectedNames.length || assets.some(item => !expectedNames.includes(item.name) || !Number.isSafeInteger(item.size) || item.size <= 0 || !/^[a-f0-9]{64}$/.test(item.sha256 || ''))) throw new Error('Mac notarization asset inventory is incomplete or ambiguous.')
  for (const [report, expected] of [[binaries[0], darwinNames], ...appReports.map(report => [report, expectedNames.filter(name => name.includes(`-mac-${report.arch}.`))])]) {
    if (report.assets?.length !== expected.length || report.assets.some(item => !expected.includes(item.name))) throw new Error('A notarization report contains another architecture’s assets.')
  }
  return {
    schema_version: 3, version, source_sha: sourceSHA, team_id: personalTeam,
    certificate_expires_at: expiration, status: 'signed', notarization_status: 'accepted', native_tested_arches: ['x64', 'arm64'],
    checks: { signatures: true, hardened_runtime: true, secure_timestamps: true, embedded_checksums: true, stapling: true, gatekeeper: true, packaged_app_startup_shutdown: true },
    notarizations: submissions, assets,
  }
}

export async function finalize({ directory = releaseDirectory, programs = path.join(repositoryRoot, 'dist/bin'), sourceSHA = process.env.RELEASE_SOURCE_SHA, version } = {}) {
  version ??= await readReleaseVersion()
  const reports = await Promise.all(['binaries', 'x64', 'arm64'].map(async name => JSON.parse(await readFile(path.join(directory, 'notarization', `${name}.json`), 'utf8'))))
  const report = combineNotarizationReports(reports, { version, sourceSHA })
  const files = new Map([
    ...darwinNames.map(name => [name, path.join(programs, name)]),
    ...desktopNames(version).filter(name => name.includes('-mac-')).map(name => [name, path.join(directory, name)]),
  ])
  await validateMacSigningReport(report, { version, sourceSHA, files })
  await writeFile(path.join(directory, 'mac-signing-report.json'), `${JSON.stringify(report, null, 2)}\n`)
  console.log(`Mac signatures, final DMG/ZIP checks and native App startup/shutdown passed. Apple notarization: ${report.notarization_status}.`)
  return report
}

async function main() {
  const [operation, target, arch, ...extra] = process.argv.slice(2)
  if (extra.length) throw new Error('Unexpected notarization arguments.')
  if (operation === 'binaries' && target && !arch) await notarizeBinaries(path.resolve(target))
  else if (operation === 'app' && target && arch) await notarizeApp(path.resolve(target), arch)
  else if (operation === 'resume-submission' && target && uuid.test(arch || '')) {
    const receipt = JSON.parse(await readFile(path.join(reportDirectory, `${arch.toLowerCase()}.json`), 'utf8'))
    await resumeSubmission(path.resolve(target), receipt, context())
  }
  else if (operation === 'finalize' && !arch) await finalize(target ? { programs: path.resolve(target) } : {})
  else throw new Error('Usage: mac-notarize.mjs binaries DIST | app APP x64|arm64 | resume-submission INPUT SUBMISSION_ID | finalize [PROGRAMS]')
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
