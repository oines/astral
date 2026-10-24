import { spawnSync } from 'node:child_process'
import { randomBytes, X509Certificate } from 'node:crypto'
import { appendFile, chmod, lstat, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { isDirectInvocation } from './release-version.mjs'

export const appleSigningTeam = '6N6W6Z6739'
export const notaryProfile = 'astral-notarization'
const stateName = 'astral-signing-state.json'
const directoryPrefix = 'astral-signing-'
const keychainName = 'astral-signing.keychain-db'
const day = 86_400_000

// Never expose child_process errors or stderr: security/notarytool accept
// credentials as arguments and their default error messages can repeat them.
export function safeCommand(executable, args, { input, environment = process.env, timeout = 180_000 } = {}) {
  let result
  try {
    result = spawnSync(executable, args, {
      encoding: 'utf8', input, env: { ...environment, LC_ALL: 'C' },
      stdio: ['pipe', 'pipe', 'pipe'], timeout, maxBuffer: 8 << 20,
    })
  } catch {
    throw new Error(`${path.basename(executable)} could not start.`)
  }
  if (result.error || result.signal || result.status !== 0) {
    const status = Number.isInteger(result.status) ? result.status : 'unavailable'
    throw new Error(`${path.basename(executable)} failed (exit ${status}).`)
  }
  return { stdout: result.stdout || '', stderr: result.stderr || '' }
}

export function validatePublicCertificate(pem, { now = Date.now(), fingerprint } = {}) {
  let certificate
  let subject
  try {
    certificate = new X509Certificate(pem)
    subject = certificate.toLegacyObject().subject
  } catch {
    throw new Error('The Developer ID public certificate could not be read.')
  }
  const identity = subject.CN
  if (subject.OU !== appleSigningTeam || typeof identity !== 'string' ||
      !identity.startsWith('Developer ID Application: ') || !identity.endsWith(` (${appleSigningTeam})`) || /[\r\n\0]/.test(identity)) {
    throw new Error(`The certificate must be Developer ID Application for personal team ${appleSigningTeam}.`)
  }
  if (fingerprint && certificate.fingerprint.replaceAll(':', '').toUpperCase() !== fingerprint.toUpperCase()) {
    throw new Error('The public certificate does not match the available signing identity.')
  }
  const validFrom = Date.parse(certificate.validFrom)
  const validTo = Date.parse(certificate.validTo)
  if (!Number.isFinite(validFrom) || !Number.isFinite(validTo) || validFrom > now || validTo <= now) {
    throw new Error('The Developer ID Application certificate is expired or not yet valid.')
  }
  return { identity, expiresAt: new Date(validTo).toISOString(), expiringSoon: validTo - now <= 30 * day }
}

export function certificatesFromPEM(value) {
  return value.match(/-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----/g) || []
}

export function parseKeychainSearchList(value) {
  return value.split(/\r?\n/).map(line => line.trim()).filter(Boolean).map(line => {
    const match = /^"((?:[^"\\]|\\.)*)"$/.exec(line)
    if (!match) throw new Error('The current keychain search list could not be read safely.')
    const entry = match[1].replace(/\\(["\\])/g, '$1')
    if (!path.isAbsolute(entry) || /[\r\n\0]/.test(entry)) throw new Error('The keychain search list contains an invalid path.')
    return entry
  })
}

export function parseSigningIdentity(value) {
  const identities = [...value.matchAll(/^\s*\d+\)\s+([A-Fa-f0-9]{40})\s+"([^"\r\n]+)"\s*$/gm)]
  const expected = identities.filter(match => match[2].startsWith('Developer ID Application: ') && match[2].endsWith(` (${appleSigningTeam})`))
  if (identities.length !== 1 || expected.length !== 1) {
    throw new Error(`The temporary keychain must contain exactly one valid Developer ID Application identity for ${appleSigningTeam}.`)
  }
  return { fingerprint: expected[0][1], identity: expected[0][2] }
}

function requiredValue(environment, name) {
  const value = environment[name]
  if (typeof value !== 'string' || !value.trim() || /[\r\n\0]/.test(value)) throw new Error(`Missing or invalid ${name}.`)
  return value
}

export function appleAccountCredentials(environment) {
  if (environment.APPLE_TEAM_ID !== appleSigningTeam) throw new Error(`APPLE_TEAM_ID must be the personal team ${appleSigningTeam}.`)
  const appleID = requiredValue(environment, 'APPLE_ID')
  const appPassword = requiredValue(environment, 'APPLE_APP_SPECIFIC_PASSWORD')
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(appleID)) throw new Error('APPLE_ID must be an email address.')
  if (!/^[a-zA-Z]{4}(?:-[a-zA-Z]{4}){3}$/.test(appPassword)) throw new Error('Use an Apple app-specific password, in xxxx-xxxx-xxxx-xxxx form.')
  return { appleID, appPassword }
}

function decodeP12(value) {
  if (typeof value !== 'string') throw new Error('MAC_CERTIFICATE_P12 is required.')
  const encoded = value.replace(/\s/g, '')
  if (!encoded || encoded.length > 12 << 20 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(encoded)) {
    throw new Error('MAC_CERTIFICATE_P12 must be a base64-encoded encrypted P12 file, smaller than 8 MiB.')
  }
  const contents = Buffer.from(encoded, 'base64')
  if (contents.length === 0 || contents.length > 8 << 20 || contents.toString('base64') !== encoded) {
    contents.fill(0)
    throw new Error('MAC_CERTIFICATE_P12 is invalid or too large.')
  }
  return contents
}

async function runnerPaths(environment, platform) {
  if (platform !== 'darwin' || environment.CI !== 'true' || environment.GITHUB_ACTIONS !== 'true') {
    throw new Error('Mac credential setup and cleanup only run on a macOS GitHub Actions runner.')
  }
  const requested = requiredValue(environment, 'RUNNER_TEMP')
  if (!path.isAbsolute(requested)) throw new Error('RUNNER_TEMP must be an absolute path.')
  const runnerTemp = await realpath(requested)
  if (!(await lstat(runnerTemp)).isDirectory()) throw new Error('RUNNER_TEMP must be a directory.')
  return { runnerTemp, statePath: path.join(runnerTemp, stateName) }
}

async function loadState(statePath, runnerTemp) {
  let info
  try { info = await lstat(statePath) } catch (error) {
    if (error.code === 'ENOENT') return undefined
    throw error
  }
  if (!info.isFile() || info.isSymbolicLink() || (info.mode & 0o077) !== 0 || info.size > 64 << 10) throw new Error('The signing cleanup state is not a private regular file.')
  let state
  try { state = JSON.parse(await readFile(statePath, 'utf8')) } catch { throw new Error('The signing cleanup state could not be read.') }
  if (JSON.stringify(Object.keys(state).sort()) !== JSON.stringify(['keychain', 'previousSearchList', 'temporaryDirectory']) ||
      typeof state.temporaryDirectory !== 'string' || path.dirname(state.temporaryDirectory) !== runnerTemp ||
      !path.basename(state.temporaryDirectory).startsWith(directoryPrefix) || state.temporaryDirectory === path.join(runnerTemp, stateName) ||
      state.keychain !== path.join(state.temporaryDirectory, keychainName) || !Array.isArray(state.previousSearchList) ||
      state.previousSearchList.some(entry => typeof entry !== 'string' || !path.isAbsolute(entry) || /[\r\n\0]/.test(entry))) {
    throw new Error('The signing cleanup state contains invalid paths.')
  }
  try {
    const directory = await lstat(state.temporaryDirectory)
    if (!directory.isDirectory() || directory.isSymbolicLink() || await realpath(state.temporaryDirectory) !== state.temporaryDirectory) {
      throw new Error('The signing temporary directory is not a regular private directory.')
    }
  } catch (error) { if (error.code !== 'ENOENT') throw error }
  return state
}

export async function cleanupCredentials({ environment = process.env, platform = process.platform, command = safeCommand } = {}) {
  const { runnerTemp, statePath } = await runnerPaths(environment, platform)
  const state = await loadState(statePath, runnerTemp)
  if (!state) return
  const failures = []
  try { await command('/usr/bin/security', ['list-keychains', '-d', 'user', '-s', ...state.previousSearchList], { environment }) } catch (error) { failures.push(error) }
  try {
    if (await lstat(state.keychain).catch(error => { if (error.code !== 'ENOENT') throw error })) {
      await command('/usr/bin/security', ['delete-keychain', state.keychain], { environment })
    }
  } catch (error) { failures.push(error) }
  try { await rm(state.temporaryDirectory, { recursive: true, force: true }) } catch (error) { failures.push(error) }
  // Keep the non-secret state if restoration failed, so the always() cleanup
  // step (or a second cleanup invocation) can retry the original search list.
  if (failures.length) throw new Error('Temporary signing cleanup failed; rerun mac-credentials.mjs cleanup to restore the original keychain search list.')
  await rm(statePath, { force: true })
}

export async function setupCredentials({ environment = process.env, platform = process.platform, command = safeCommand, now = Date.now(), warn = console.warn } = {}) {
  const paths = await runnerPaths(environment, platform)
  const { appleID, appPassword } = appleAccountCredentials(environment)
  const p12Password = requiredValue(environment, 'MAC_CERTIFICATE_PASSWORD')
  const githubEnv = requiredValue(environment, 'GITHUB_ENV')
  if (!path.isAbsolute(githubEnv)) throw new Error('GITHUB_ENV must be an absolute path.')
  if (await loadState(paths.statePath, paths.runnerTemp)) throw new Error('Signing cleanup state already exists. Run mac-credentials.mjs cleanup before setup.')
  const p12 = decodeP12(environment.MAC_CERTIFICATE_P12)
  let stateCreated = false
  let temporaryDirectory
  try {
    const original = await command('/usr/bin/security', ['list-keychains', '-d', 'user'], { environment })
    const previousSearchList = parseKeychainSearchList(original.stdout)
    temporaryDirectory = await mkdtemp(path.join(paths.runnerTemp, directoryPrefix))
    await chmod(temporaryDirectory, 0o700)
    const keychain = path.join(temporaryDirectory, keychainName)
    await writeFile(paths.statePath, `${JSON.stringify({ temporaryDirectory, keychain, previousSearchList })}\n`, { mode: 0o600, flag: 'wx' })
    stateCreated = true
    const p12Path = path.join(temporaryDirectory, 'certificate.p12')
    await writeFile(p12Path, p12, { mode: 0o600, flag: 'wx' })
    p12.fill(0)
    const keychainPassword = randomBytes(32).toString('hex')
    await command('/usr/bin/security', ['create-keychain', '-p', keychainPassword, keychain], { environment })
    await command('/usr/bin/security', ['set-keychain-settings', '-lut', '21600', keychain], { environment })
    await command('/usr/bin/security', ['unlock-keychain', '-p', keychainPassword, keychain], { environment })
    await command('/usr/bin/security', ['import', p12Path, '-k', keychain, '-f', 'pkcs12', '-P', p12Password, '-x', '-T', '/usr/bin/codesign'], { environment })
    await rm(p12Path, { force: true })
    await command('/usr/bin/security', ['set-key-partition-list', '-S', 'apple-tool:,apple:,codesign:', '-s', '-t', 'private', '-k', keychainPassword, keychain], { environment })
    await command('/usr/bin/security', ['list-keychains', '-d', 'user', '-s', keychain, ...previousSearchList], { environment })
    const available = await command('/usr/bin/security', ['find-identity', '-v', '-p', 'codesigning', keychain], { environment })
    const selected = parseSigningIdentity(available.stdout)
    const exported = await command('/usr/bin/security', ['find-certificate', '-a', '-c', selected.identity, '-p', keychain], { environment })
    const matching = certificatesFromPEM(exported.stdout).filter(pem => {
      try { return new X509Certificate(pem).fingerprint.replaceAll(':', '').toUpperCase() === selected.fingerprint.toUpperCase() } catch { return false }
    })
    if (matching.length !== 1) throw new Error('Could not locate the exact public certificate for the signing identity.')
    const certificate = validatePublicCertificate(matching[0], { now, fingerprint: selected.fingerprint })
    if (certificate.expiringSoon) warn(`::warning::Developer ID Application expires on ${certificate.expiresAt}; replace the CI certificate within 30 days.`)
    await command('/usr/bin/xcrun', ['notarytool', 'store-credentials', notaryProfile, '--apple-id', appleID, '--password', appPassword, '--team-id', appleSigningTeam, '--validate', '--keychain', keychain], { environment })
    await appendFile(githubEnv, [
      `CSC_KEYCHAIN=${keychain}`, `MAC_SIGNING_IDENTITY=${certificate.identity}`,
      `ASTRAL_NOTARY_PROFILE=${notaryProfile}`, `MAC_CERTIFICATE_EXPIRES_AT=${certificate.expiresAt}`, '',
    ].join('\n'), { encoding: 'utf8' })
    return certificate
  } catch (error) {
    if (stateCreated) {
      try { await cleanupCredentials({ environment, platform, command }) } catch { throw new Error('Mac credential setup failed and cleanup needs attention. Run mac-credentials.mjs cleanup.') }
    } else if (temporaryDirectory) await rm(temporaryDirectory, { recursive: true, force: true })
    throw error
  } finally { p12.fill(0) }
}

async function main() {
  const [operation, ...extra] = process.argv.slice(2)
  if (extra.length || !['setup', 'cleanup'].includes(operation)) throw new Error('Usage: mac-credentials.mjs setup | cleanup')
  if (operation === 'cleanup') {
    await cleanupCredentials()
    console.log('Temporary Mac signing credentials removed; original keychain search list restored.')
  } else {
    const certificate = await setupCredentials()
    console.log(`Personal Developer ID Application ready for team ${appleSigningTeam}; expires ${certificate.expiresAt}.`)
  }
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
