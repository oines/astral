import { constants } from 'node:fs'
import { lstat, open, realpath } from 'node:fs/promises'
import path from 'node:path'
import { safeCommand } from './mac-credentials.mjs'
import { signingEnvironment } from './mac-signing.mjs'
import { isDirectInvocation } from './release-version.mjs'

export const statusReportName = 'astral-notary-status.json'
const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i
const statuses = new Set(['Accepted', 'Invalid', 'In Progress', 'Rejected'])

function submissionID(value) {
  if (value === undefined || value === '') return ''
  if (typeof value !== 'string' || !uuid.test(value)) throw new Error('NOTARY_SUBMISSION_ID must be empty or a notarization submission UUID.')
  return value
}

function publicSubmission(value) {
  if (!value || typeof value.id !== 'string' || !uuid.test(value.id) || typeof value.name !== 'string' || !value.name ||
      !statuses.has(value.status) || typeof value.createdDate !== 'string' || !Number.isFinite(Date.parse(value.createdDate))) {
    throw new Error('Apple returned incomplete notarization status metadata.')
  }
  // Do not copy the response object: notarytool may return other account data.
  return { id: value.id, name: value.name, status: value.status, createdDate: new Date(value.createdDate).toISOString() }
}

async function writeStatusReport(file, records) {
  let handle
  try {
    handle = await open(file, constants.O_CREAT | constants.O_WRONLY | constants.O_TRUNC | constants.O_NOFOLLOW, 0o600)
    await handle.chmod(0o600)
    await handle.writeFile(`${JSON.stringify(records, null, 2)}\n`, 'utf8')
  } catch {
    throw new Error('Could not write the sanitized notarization status report.')
  } finally { await handle?.close() }
}

export async function queryNotaryStatus({ environment = process.env, platform = process.platform, command = safeCommand, log = console.log } = {}) {
  if (platform !== 'darwin' || environment.CI !== 'true' || environment.GITHUB_ACTIONS !== 'true') {
    throw new Error('Notarization status queries run only on a macOS GitHub Actions runner.')
  }
  const { keychain } = signingEnvironment(environment)
  const id = submissionID(environment.NOTARY_SUBMISSION_ID)
  const profile = environment.ASTRAL_NOTARY_PROFILE
  if (typeof profile !== 'string' || !profile.trim() || /[\r\n\0]/.test(profile)) throw new Error('ASTRAL_NOTARY_PROFILE is required.')
  const temporary = environment.RUNNER_TEMP
  if (typeof temporary !== 'string' || !path.isAbsolute(temporary) || /[\r\n\0]/.test(temporary)) throw new Error('RUNNER_TEMP must be an absolute directory path.')
  let directory
  try {
    directory = await realpath(temporary)
    if (!(await lstat(directory)).isDirectory()) throw new Error()
  } catch { throw new Error('RUNNER_TEMP must be an existing directory.') }

  const operation = id ? ['info', id] : ['history']
  let result
  try {
    result = await command('/usr/bin/xcrun', ['notarytool', ...operation, '--keychain-profile', profile, '--keychain', keychain, '--output-format', 'json'], { environment, timeout: 120_000 })
  } catch {
    // Never let child_process errors repeat credential arguments or stderr.
    throw new Error('Apple notarization status query failed.')
  }
  let response
  try { response = JSON.parse(result.stdout) } catch { throw new Error('Apple notarization status response was not valid JSON.') }
  let records
  if (id) {
    records = [publicSubmission(response)]
    if (records[0].id.toLowerCase() !== id.toLowerCase()) throw new Error('Apple returned a different notarization submission ID.')
  } else {
    if (!Array.isArray(response?.history)) throw new Error('Apple notarization history response was incomplete.')
    // The first CLI archive is named astral-darwin.zip, so include either case.
    records = response.history.filter(value => typeof value?.name === 'string' && /^astral/i.test(value.name)).map(publicSubmission)
  }
  await writeStatusReport(path.join(directory, statusReportName), records)
  log(JSON.stringify(records, null, 2))
  return records
}

async function main() {
  if (process.argv.length > 2) throw new Error('Set NOTARY_SUBMISSION_ID to a UUID, or leave it empty to query Astral history.')
  await queryNotaryStatus()
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
