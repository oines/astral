import { lstat, readFile } from 'node:fs/promises'
import path from 'node:path'
import { createInterface } from 'node:readline'
import { Writable } from 'node:stream'
import { appleAccountCredentials, appleSigningTeam, certificatesFromPEM, safeCommand, validatePublicCertificate } from './mac-credentials.mjs'
import { isDirectInvocation } from './release-version.mjs'

export const signingRepository = 'oines/astral'
export const signingEnvironmentName = 'mac-signing'
const permittedRefs = [{ type: 'branch', name: 'main' }, { type: 'tag', name: 'v*' }]

function parseResponse(value, description) {
  try { return JSON.parse(value) } catch { throw new Error(`Could not read ${description} from GitHub.`) }
}

export async function ensureSigningEnvironment({ command = safeCommand } = {}) {
  const endpoint = `repos/${signingRepository}/environments`
  const pages = parseResponse((await command('gh', ['api', '--paginate', '--slurp', `${endpoint}?per_page=100`])).stdout, 'deployment environments')
  if (!Array.isArray(pages) || pages.some(page => !Array.isArray(page.environments))) throw new Error('GitHub returned an invalid deployment environment inventory.')
  const existing = pages.flatMap(page => page.environments).find(entry => entry.name === signingEnvironmentName)
  if (!existing || !existing.deployment_branch_policy?.custom_branch_policies) {
    const rules = existing?.protection_rules || []
    const reviewers = rules.find(rule => rule.type === 'required_reviewers')
    // Preserve existing required reviewers and delays while restricting refs.
    // Merely omitting these PUT fields risks resetting their current values.
    const configuration = {
      wait_timer: rules.find(rule => rule.type === 'wait_timer')?.wait_timer || 0,
      prevent_self_review: reviewers?.prevent_self_review || false,
      reviewers: (reviewers?.reviewers || []).map(entry => ({ type: entry.type, id: entry.reviewer.id })),
      deployment_branch_policy: { protected_branches: false, custom_branch_policies: true },
    }
    await command('gh', ['api', '--method', 'PUT', `${endpoint}/${signingEnvironmentName}`, '--input', '-'], { input: JSON.stringify(configuration) })
  }
  const policyEndpoint = `${endpoint}/${signingEnvironmentName}/deployment-branch-policies`
  const policyPages = parseResponse((await command('gh', ['api', '--paginate', '--slurp', `${policyEndpoint}?per_page=100`])).stdout, 'deployment branch policies')
  if (!Array.isArray(policyPages) || policyPages.some(page => !Array.isArray(page.branch_policies))) throw new Error('GitHub returned an invalid deployment branch policy inventory.')
  const policies = policyPages.flatMap(page => page.branch_policies)
  if (policies.some(policy => !permittedRefs.some(expected => expected.type === policy.type && expected.name === policy.name))) {
    throw new Error('mac-signing has additional deployment rules. Restrict it to branch main and tag v* in GitHub Settings → Environments, then rerun this helper.')
  }
  for (const expected of permittedRefs) {
    if (!policies.some(policy => policy.type === expected.type && policy.name === expected.name)) {
      await command('gh', ['api', '--method', 'POST', policyEndpoint, '--input', '-'], { input: JSON.stringify(expected) })
    }
  }
}

// Readline handles paste, editing and terminal restoration, while the output
// stream suppresses all terminal echo during password questions.
export function terminalQuestion(label, { hidden = false, input = process.stdin, output = process.stdout } = {}) {
  if (!input.isTTY || !output.isTTY) throw new Error('Run this helper yourself in an interactive terminal; do not pipe credentials into it.')
  let muted = false
  const terminalOutput = new Writable({ write(chunk, encoding, done) {
    // Do not queue terminal chunks behind an asynchronous stdout callback:
    // queued password echo must never be emitted after the prompt is unmuted.
    if (!muted) output.write(chunk, encoding)
    done()
  } })
  terminalOutput.isTTY = true
  terminalOutput.columns = output.columns
  const reader = createInterface({ input, output: terminalOutput, terminal: true })
  return new Promise((resolve, reject) => {
    let settled = false
    const finish = (callback, value) => {
      if (settled) return
      settled = true
      reader.close()
      muted = false
      if (hidden) output.write('\n')
      callback(value)
    }
    reader.once('SIGINT', () => finish(reject, new Error('Credential setup cancelled.')))
    reader.once('close', () => finish(reject, new Error('Credential setup cancelled.')))
    reader.question(label, answer => finish(resolve, answer))
    muted = hidden
  })
}

export async function readEncryptedP12(filename, password, { command = safeCommand, now = Date.now() } = {}) {
  if (typeof password !== 'string' || !password || /[\r\n\0]/.test(password)) throw new Error('Use a non-empty P12 export password without line breaks.')
  const info = await lstat(filename)
  if (!info.isFile() || info.size === 0 || info.size > 8 << 20) throw new Error('Choose a regular encrypted .p12 file, smaller than 8 MiB.')
  // Only the public leaf certificate leaves OpenSSL. The private key remains
  // inside the original encrypted P12; no private PEM or decrypted file exists.
  const options = { input: `${password}\n` }
  const structure = await command('/usr/bin/openssl', ['pkcs12', '-in', filename, '-info', '-noout', '-passin', 'stdin'], options)
  if (!/Shrouded Keybag/i.test(`${structure.stdout}\n${structure.stderr}`)) throw new Error('The P12 must contain a password-encrypted private key.')
  const result = await command('/usr/bin/openssl', ['pkcs12', '-in', filename, '-clcerts', '-nokeys', '-passin', 'stdin'], options)
  const certificates = certificatesFromPEM(result.stdout)
  if (certificates.length !== 1) throw new Error('Export exactly one Developer ID Application identity and its private key to the P12.')
  const certificate = validatePublicCertificate(certificates[0], { now })
  return { contents: await readFile(filename), certificate }
}

export async function uploadSigningCredentials({ contents, p12Password, appleID, appPassword }, { command = safeCommand } = {}) {
  appleAccountCredentials({ APPLE_TEAM_ID: appleSigningTeam, APPLE_ID: appleID, APPLE_APP_SPECIFIC_PASSWORD: appPassword })
  const repositoryResult = await command('gh', ['repo', 'view', signingRepository, '--json', 'nameWithOwner'])
  let repository
  try { repository = JSON.parse(repositoryResult.stdout) } catch { throw new Error('Could not verify the GitHub repository.') }
  if (repository.nameWithOwner !== signingRepository) throw new Error(`GitHub must resolve the destination to ${signingRepository}.`)
  await ensureSigningEnvironment({ command })
  const secrets = {
    MAC_CERTIFICATE_P12: contents.toString('base64'),
    MAC_CERTIFICATE_PASSWORD: p12Password,
    APPLE_ID: appleID,
    APPLE_APP_SPECIFIC_PASSWORD: appPassword,
  }
  for (const [name, value] of Object.entries(secrets)) {
    await command('gh', ['secret', 'set', name, '--repo', signingRepository, '--env', signingEnvironmentName], { input: value })
  }
  await command('gh', ['variable', 'set', 'APPLE_TEAM_ID', '--repo', signingRepository, '--env', signingEnvironmentName], { input: appleSigningTeam })
}

async function main() {
  if (!process.stdin.isTTY || !process.stdout.isTTY || process.env.CI || process.env.GITHUB_ACTIONS) throw new Error('Run this helper yourself in a local interactive terminal, outside CI.')
  if (process.argv.length !== 2) throw new Error('Usage: node scripts/setup-apple-signing.mjs (no secret arguments)')
  console.log(`This uploads an encrypted Developer ID P12 and an Apple app-specific password to the ${signingRepository} / ${signingEnvironmentName} GitHub Environment.`)
  console.log(`The certificate must belong to your personal Apple Developer team ${appleSigningTeam}. Password entry is hidden; the original P12 stays on your Mac.`)
  console.log('Create a dedicated Apple app-specific password for Astral at https://account.apple.com (Sign-In and Security → App-Specific Passwords).')
  const filename = path.resolve((await terminalQuestion('Encrypted P12 path (plain path, without quotes): ')).trim())
  const p12Password = await terminalQuestion('P12 export password: ', { hidden: true })
  const { contents, certificate } = await readEncryptedP12(filename, p12Password)
  try {
    console.log(`Verified ${certificate.identity}; expires ${certificate.expiresAt}.`)
    if (certificate.expiringSoon) console.warn('This certificate expires within 30 days; replace it before automated signing is enabled.')
    const appleID = (await terminalQuestion('Personal Apple ID email: ')).trim()
    const appPassword = (await terminalQuestion('Astral app-specific password: ', { hidden: true })).trim()
    appleAccountCredentials({ APPLE_TEAM_ID: appleSigningTeam, APPLE_ID: appleID, APPLE_APP_SPECIFIC_PASSWORD: appPassword })
    const answer = await terminalQuestion(`Type ${signingRepository} to upload these credentials to its ${signingEnvironmentName} Environment: `)
    if (answer !== signingRepository) throw new Error('No credentials were uploaded.')
    await uploadSigningCredentials({ contents, p12Password, appleID, appPassword })
    console.log(`Uploaded four encrypted Environment secrets and APPLE_TEAM_ID=${appleSigningTeam}. Apple credentials will be validated on the macOS CI runner.`)
  } finally { contents.fill(0) }
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
