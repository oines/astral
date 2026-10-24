import assert from 'node:assert/strict'
import { X509Certificate } from 'node:crypto'
import { once } from 'node:events'
import { lstat, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { PassThrough, Writable } from 'node:stream'
import test from 'node:test'
import { appleSigningTeam, cleanupCredentials, parseKeychainSearchList, parseSigningIdentity, safeCommand, setupCredentials, validatePublicCertificate } from './mac-credentials.mjs'
import { ensureSigningEnvironment, readEncryptedP12, terminalQuestion, uploadSigningCredentials } from './setup-apple-signing.mjs'

// Synthetic, self-signed PUBLIC test certificate. Its private key was discarded;
// it is not an Apple-issued signing identity and grants no signing authority.
const pem = `-----BEGIN CERTIFICATE-----
MIIDgDCCAmgCCQDvl4H4fBpn2zANBgkqhkiG9w0BAQsFADCBgTE3MDUGA1UEAwwu
RGV2ZWxvcGVyIElEIEFwcGxpY2F0aW9uOiBGaXh0dXJlICg2TjZXNlo2NzM5KTET
MBEGA1UECwwKNk42VzZaNjczOTEkMCIGA1UECgwbQXN0cmFsIHNpZ25pbmcgdGVz
dCBmaXh0dXJlMQswCQYDVQQGEwJVUzAeFw0yNjEwMjQwOTI5NTJaFw0yNzEwMjQw
OTI5NTJaMIGBMTcwNQYDVQQDDC5EZXZlbG9wZXIgSUQgQXBwbGljYXRpb246IEZp
eHR1cmUgKDZONlc2WjY3MzkpMRMwEQYDVQQLDAo2TjZXNlo2NzM5MSQwIgYDVQQK
DBtBc3RyYWwgc2lnbmluZyB0ZXN0IGZpeHR1cmUxCzAJBgNVBAYTAlVTMIIBIjAN
BgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAxtg02Fye1gPuGAHIVtkP7n+oqGoV
bEdXoXaASejTlwsepZjLy3tdEwY/QevnE8SAMwTlpJ0hpfYnii0TqqsG1PtyzYnJ
xeEzMJTqqU0d3237DHYrlIGMeUjCZbrAKluuJ0tVZZMiBmp9sYUouVe72251NUuv
llCZUIK1uR0NJ4hPYJOZjtyyACfoX1lKce0pbvVIVvL35kYEQS4K4GaxVhzrq5Ak
+8xQrQqq0xBPb80e1KT3y114e/mmzpiwXjFAiDIFV4lzhfhrUyRIyGIU99i73Gqd
gvHymiuBWCYJueGhv6ApQj8Qi7TtWtbJCFfMof0/W+Mm2ZUd7FMYL6w2RQIDAQAB
MA0GCSqGSIb3DQEBCwUAA4IBAQB9i4HFq4h5CxeQkUC+l495gfwMgIDny//9LuEw
hMS1F9jMLCUyJ00gv7fa71UU8n/2u4FNYT3KwnCe1FlZJtLXqVXS6Hq2sKfe2+N+
nzW9F699exkX7jVK/YngQHzV2xMC6Fa8lp5q4JnpzIQyf6vCygjkcytF67Hm+qnd
orNJS6pUtOtnND5hvP4BKkhckYhHIVnC4ylFEwK/n6W8VoAdYXlpfLTaBJngj4w2
+6Fq94Gt3DMJV/bORWAI8lc/Xk7ZICvxYwuFXiXjqxFPb03UWLvMwihfRAh5loFU
9/an//TVnS5t1FbK4zTXyjmShkGz2fTGf3WY8GbU6aW6acHA
-----END CERTIFICATE-----`
const fixture = new X509Certificate(pem)
const fingerprint = fixture.fingerprint.replaceAll(':', '')
const identity = `Developer ID Application: Fixture (${appleSigningTeam})`
const now = Date.parse(fixture.validFrom) + 86_400_000
const priorSearchList = ['/Users/runner/Library/Keychains/login.keychain-db', '/Library/Keychains/System.keychain']

async function fixtureRunner(t) {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-credentials-fixture-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const runnerTemp = await realpath(directory)
  const environment = {
    CI: 'true', GITHUB_ACTIONS: 'true', RUNNER_TEMP: runnerTemp, GITHUB_ENV: path.join(runnerTemp, 'github-env'),
    APPLE_TEAM_ID: appleSigningTeam, APPLE_ID: 'fixture@example.test', APPLE_APP_SPECIFIC_PASSWORD: 'aaaa-bbbb-cccc-dddd',
    MAC_CERTIFICATE_PASSWORD: 'fixture export password', MAC_CERTIFICATE_P12: Buffer.from('encrypted P12 fixture').toString('base64'),
  }
  const calls = []
  const command = async (executable, args) => {
    calls.push({ executable, args })
    if (args[0] === 'list-keychains' && !args.includes('-s')) return { stdout: priorSearchList.map(entry => `    "${entry}"`).join('\n'), stderr: '' }
    if (args[0] === 'create-keychain') {
      const statePath = path.join(runnerTemp, 'astral-signing-state.json')
      assert.equal((await lstat(statePath)).mode & 0o777, 0o600)
      assert.equal((await lstat(path.dirname(args.at(-1)))).mode & 0o777, 0o700)
      await writeFile(args.at(-1), 'fixture keychain')
    }
    if (args[0] === 'import') {
      assert.equal((await lstat(args[1])).mode & 0o777, 0o600)
      assert.equal(await readFile(args[1], 'utf8'), 'encrypted P12 fixture')
      assert.ok(args.includes('-x'))
      assert.equal(args[args.indexOf('-T') + 1], '/usr/bin/codesign')
      assert.ok(!args.includes('-A'))
    }
    if (args[0] === 'find-identity') return { stdout: `  1) ${fingerprint} "${identity}"\n     1 valid identities found\n`, stderr: '' }
    if (args[0] === 'find-certificate') return { stdout: pem, stderr: '' }
    return { stdout: '', stderr: '' }
  }
  return { runnerTemp, environment, calls, command }
}

test('CI setup cannot run on developer machines or a non-macOS runner', async t => {
  const { environment } = await fixtureRunner(t)
  const command = () => { throw new Error('Must not access the real keychain.') }
  await assert.rejects(setupCredentials({ environment, platform: 'linux', command }), /macOS GitHub Actions/)
  await assert.rejects(setupCredentials({ environment: { ...environment, GITHUB_ACTIONS: '' }, platform: 'darwin', command }), /macOS GitHub Actions/)
  await assert.rejects(setupCredentials({ environment: { ...environment, APPLE_TEAM_ID: 'OTHERTEAM1' }, platform: 'darwin', command }), /personal team/)
})

test('identity selection and public certificate validation reject ambiguity, expiration and the wrong fingerprint', () => {
  const line = `  1) ${fingerprint} "${identity}"\n`
  assert.deepEqual(parseSigningIdentity(line), { fingerprint, identity })
  assert.throws(() => parseSigningIdentity(line + line), /exactly one/)
  assert.throws(() => parseSigningIdentity(line.replace(appleSigningTeam, 'OTHERTEAM1')), /exactly one/)
  assert.throws(() => parseSigningIdentity(line.replace('Developer ID Application', 'Apple Development')), /exactly one/)
  assert.equal(validatePublicCertificate(pem, { now, fingerprint }).identity, identity)
  assert.throws(() => validatePublicCertificate(pem, { now: Date.parse(fixture.validFrom) - 1 }), /not yet valid/)
  assert.throws(() => validatePublicCertificate(pem, { now: Date.parse(fixture.validTo) }), /expired/)
  assert.throws(() => validatePublicCertificate(pem, { now, fingerprint: '0'.repeat(40) }), /does not match/)
  assert.equal(validatePublicCertificate(pem, { now: Date.parse(fixture.validTo) - 86_400_000 }).expiringSoon, true)
  assert.deepEqual(parseKeychainSearchList('    "/tmp/Astral \\"fixture\\".keychain"\n'), ['/tmp/Astral "fixture".keychain'])
  assert.throws(() => parseKeychainSearchList('relative.keychain'), /safely/)
})

test('setup isolates credentials, validates notarization and exports only public signing metadata; cleanup is idempotent', async t => {
  const { environment, runnerTemp, command, calls } = await fixtureRunner(t)
  const result = await setupCredentials({ environment, platform: 'darwin', command, now })
  assert.equal(result.identity, identity)
  const statePath = path.join(runnerTemp, 'astral-signing-state.json')
  const stateText = await readFile(statePath, 'utf8')
  const state = JSON.parse(stateText)
  assert.deepEqual(Object.keys(state).sort(), ['keychain', 'previousSearchList', 'temporaryDirectory'])
  assert.deepEqual(state.previousSearchList, priorSearchList)
  for (const secret of [environment.MAC_CERTIFICATE_P12, environment.MAC_CERTIFICATE_PASSWORD, environment.APPLE_APP_SPECIFIC_PASSWORD]) assert.ok(!stateText.includes(secret))
  await assert.rejects(lstat(path.join(state.temporaryDirectory, 'certificate.p12')), { code: 'ENOENT' })
  const exported = await readFile(environment.GITHUB_ENV, 'utf8')
  assert.ok(exported.includes(`CSC_KEYCHAIN=${state.keychain}\n`))
  assert.ok(exported.includes(`MAC_SIGNING_IDENTITY=${identity}\n`))
  assert.ok(exported.includes('ASTRAL_NOTARY_PROFILE=astral-notarization\n'))
  assert.ok(exported.includes(`MAC_CERTIFICATE_EXPIRES_AT=${result.expiresAt}\n`))
  for (const secret of [environment.MAC_CERTIFICATE_PASSWORD, environment.APPLE_APP_SPECIFIC_PASSWORD]) assert.ok(!exported.includes(secret))
  const notarization = calls.find(call => call.args[0] === 'notarytool')
  assert.ok(notarization.args.includes('--validate'))
  assert.equal(notarization.args[notarization.args.indexOf('--keychain') + 1], state.keychain)
  await cleanupCredentials({ environment, platform: 'darwin', command })
  assert.deepEqual(calls.at(-2).args, ['list-keychains', '-d', 'user', '-s', ...priorSearchList])
  assert.deepEqual(calls.at(-1).args, ['delete-keychain', state.keychain])
  await assert.rejects(lstat(statePath), { code: 'ENOENT' })
  await assert.rejects(lstat(state.temporaryDirectory), { code: 'ENOENT' })
  const callCount = calls.length
  await cleanupCredentials({ environment, platform: 'darwin', command })
  assert.equal(calls.length, callCount)
})

test('notarization credential failure removes private files and restores the original search list', async t => {
  const { environment, runnerTemp, command, calls } = await fixtureRunner(t)
  const failing = async (executable, args, options) => {
    if (args[0] === 'notarytool') throw new Error('xcrun failed (exit 69).')
    return command(executable, args, options)
  }
  await assert.rejects(setupCredentials({ environment, platform: 'darwin', command: failing, now }), /xcrun failed/)
  await assert.rejects(lstat(environment.GITHUB_ENV), { code: 'ENOENT' })
  await assert.rejects(lstat(path.join(runnerTemp, 'astral-signing-state.json')), { code: 'ENOENT' })
  assert.deepEqual(calls.at(-2).args, ['list-keychains', '-d', 'user', '-s', ...priorSearchList])
  assert.equal(calls.at(-1).args[0], 'delete-keychain')
})

test('cleanup retains recoverable non-secret state if restoring the search list fails', async t => {
  const { environment, runnerTemp, command } = await fixtureRunner(t)
  await setupCredentials({ environment, platform: 'darwin', command, now })
  const failing = async (executable, args, options) => {
    if (args[0] === 'list-keychains' && args.includes('-s')) throw new Error('fixture restore failure')
    return command(executable, args, options)
  }
  await assert.rejects(cleanupCredentials({ environment, platform: 'darwin', command: failing }), /rerun/)
  const statePath = path.join(runnerTemp, 'astral-signing-state.json')
  await lstat(statePath)
  await cleanupCredentials({ environment, platform: 'darwin', command })
  await assert.rejects(lstat(statePath), { code: 'ENOENT' })
})

test('unsafe cleanup state is rejected before any keychain command or file deletion', async t => {
  const { environment, runnerTemp } = await fixtureRunner(t)
  await writeFile(path.join(runnerTemp, 'astral-signing-state.json'), JSON.stringify({ temporaryDirectory: '/tmp/unrelated', keychain: '/tmp/unrelated/astral-signing.keychain-db', previousSearchList: [] }), { mode: 0o600 })
  let commands = 0
  await assert.rejects(cleanupCredentials({ environment, platform: 'darwin', command: () => { commands++ } }), /invalid paths/)
  assert.equal(commands, 0)
})

test('subprocess failure never repeats secret arguments or child stderr', () => {
  assert.throws(() => safeCommand(process.execPath, ['-e', 'process.stderr.write(process.argv[1]); process.exit(7)', 'synthetic-secret-argument']), error => {
    assert.match(error.message, /exit 7/)
    assert.ok(!error.message.includes('synthetic-secret-argument'))
    assert.ok(!error.message.includes('process.stderr'))
    return true
  })
})

test('local P12 inspection sends its password through stdin and reads only the public certificate', async t => {
  const { runnerTemp } = await fixtureRunner(t)
  const filename = path.join(runnerTemp, 'fixture.p12')
  await writeFile(filename, 'encrypted P12 fixture')
  const calls = []
  const password = 'fixture export password'
  const command = async (executable, args, options) => {
    calls.push({ executable, args, options })
    assert.ok(!args.includes(password))
    assert.equal(options.input, `${password}\n`)
    return args.includes('-info') ? { stdout: '', stderr: 'Shrouded Keybag: fixture encrypted key' } : { stdout: pem, stderr: '' }
  }
  const { contents, certificate } = await readEncryptedP12(filename, password, { command, now })
  assert.equal(contents.toString(), 'encrypted P12 fixture')
  assert.equal(certificate.identity, identity)
  assert.equal(calls.length, 2)
  assert.ok(calls[1].args.includes('-nokeys'))
  assert.equal(await readFile(filename, 'utf8'), 'encrypted P12 fixture')
})

test('local setup uploads exactly four secrets through stdin and restricts the new Environment to main and version tags', async () => {
  const calls = []
  const command = async (executable, args, options) => {
    calls.push({ executable, args, options })
    if (args[0] === 'repo') return { stdout: '{"nameWithOwner":"oines/astral"}', stderr: '' }
    if (args.at(-1).includes('/deployment-branch-policies?')) return { stdout: '[{"branch_policies":[]}]', stderr: '' }
    if (args.at(-1).includes('/environments?')) return { stdout: '[{"environments":[]}]', stderr: '' }
    return { stdout: '{}', stderr: '' }
  }
  const credentials = { contents: Buffer.from('encrypted P12 fixture'), p12Password: 'fixture export password', appleID: 'fixture@example.test', appPassword: 'aaaa-bbbb-cccc-dddd' }
  await uploadSigningCredentials(credentials, { command })
  const secretCalls = calls.filter(call => call.args[0] === 'secret')
  assert.deepEqual(secretCalls.map(call => call.args[2]), ['MAC_CERTIFICATE_P12', 'MAC_CERTIFICATE_PASSWORD', 'APPLE_ID', 'APPLE_APP_SPECIFIC_PASSWORD'])
  assert.deepEqual(secretCalls.map(call => call.options.input), [credentials.contents.toString('base64'), credentials.p12Password, credentials.appleID, credentials.appPassword])
  for (const call of calls) {
    for (const value of [credentials.p12Password, credentials.appPassword, credentials.contents.toString('base64')]) assert.ok(!call.args.includes(value))
  }
  const creation = calls.find(call => call.args.includes('PUT'))
  assert.deepEqual(JSON.parse(creation.options.input), { wait_timer: 0, prevent_self_review: false, reviewers: [], deployment_branch_policy: { protected_branches: false, custom_branch_policies: true } })
  assert.deepEqual(calls.filter(call => call.args.includes('POST')).map(call => JSON.parse(call.options.input)), [{ type: 'branch', name: 'main' }, { type: 'tag', name: 'v*' }])
  assert.equal(calls.at(-1).args[0], 'variable')
  assert.equal(calls.at(-1).options.input, appleSigningTeam)
})

test('existing protection reviewers and delays survive enabling selected refs; broad existing rules prevent secret upload', async () => {
  const calls = []
  const command = async (executable, args, options) => {
    calls.push({ executable, args, options })
    if (args.at(-1).includes('/environments?')) return { stdout: JSON.stringify([{ environments: [{ name: 'mac-signing', deployment_branch_policy: null, protection_rules: [{ type: 'wait_timer', wait_timer: 5 }, { type: 'required_reviewers', prevent_self_review: true, reviewers: [{ type: 'User', reviewer: { id: 123 } }] }] }] }]) }
    if (args.at(-1).includes('/deployment-branch-policies?')) return { stdout: '[{"branch_policies":[{"name":"*","type":"branch"}]}]' }
    return { stdout: '{}' }
  }
  await assert.rejects(ensureSigningEnvironment({ command }), /additional deployment rules/)
  const configuration = JSON.parse(calls.find(call => call.args.includes('PUT')).options.input)
  assert.equal(configuration.wait_timer, 5)
  assert.equal(configuration.prevent_self_review, true)
  assert.deepEqual(configuration.reviewers, [{ type: 'User', id: 123 }])
  assert.ok(!calls.some(call => call.args[0] === 'secret'))
})

test('hidden terminal questions never echo passwords, including when stdout writes complete asynchronously', async () => {
  const input = new PassThrough()
  input.isTTY = true
  input.setRawMode = () => input
  let shown = ''
  const output = new Writable({ write(chunk, encoding, done) { shown += chunk.toString(); setTimeout(done, 1) } })
  output.isTTY = true
  output.columns = 80
  const answer = terminalQuestion('Hidden password: ', { hidden: true, input, output })
  input.write('synthetic-pasted-password\r')
  assert.equal(await answer, 'synthetic-pasted-password')
  const finished = once(output, 'finish')
  output.end()
  await finished
  assert.ok(shown.includes('Hidden password: '))
  assert.ok(!shown.includes('synthetic-pasted-password'))
  input.destroy()
  output.destroy()
})
