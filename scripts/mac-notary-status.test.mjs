import assert from 'node:assert/strict'
import { lstat, mkdtemp, readFile, realpath, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { queryNotaryStatus, statusReportName } from './mac-notary-status.mjs'

const id = '12345678-1234-4234-8234-123456789abc'
const otherID = '87654321-4321-4321-8321-cba987654321'
const record = { id, name: 'astral-darwin.zip', status: 'In Progress', createdDate: '2026-10-24T14:03:53Z' }

async function fixture(t, response = { history: [record] }) {
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-notary-status-fixture-'))
  t.after(() => rm(temporary, { recursive: true, force: true }))
  const directory = await realpath(temporary)
  const environment = {
    CI: 'true', GITHUB_ACTIONS: 'true', RUNNER_TEMP: directory,
    APPLE_TEAM_ID: '6N6W6Z6739', MAC_SIGNING_IDENTITY: 'Developer ID Application: Fixture (6N6W6Z6739)',
    CSC_KEYCHAIN: path.join(directory, 'fixture.keychain-db'), ASTRAL_NOTARY_PROFILE: 'fixture-notary', NOTARY_SUBMISSION_ID: '',
  }
  const calls = []
  const output = []
  const options = {
    environment, platform: 'darwin', log: value => output.push(value),
    command: async (executable, args, options) => {
      calls.push({ executable, args, options })
      return { stdout: JSON.stringify(response), stderr: 'unprinted tool diagnostics' }
    },
  }
  return { options, calls, output, file: path.join(directory, statusReportName) }
}

test('history only exposes Astral archive metadata and uses the stored keychain profile', async t => {
  const secret = 'DO-NOT-PRINT-ACCOUNT-DATA'
  const { options, calls, output, file } = await fixture(t, {
    message: secret, appleID: secret,
    history: [
      { ...record, secret, team: secret },
      { ...record, id: otherID, name: 'Astral-2026.10.1-mac-arm64.dmg', status: 'Accepted', password: secret },
      { ...record, name: 'OtherApp.zip', secret },
    ],
  })
  const records = await queryNotaryStatus(options)
  assert.equal(records.length, 2)
  for (const entry of records) assert.deepEqual(Object.keys(entry).sort(), ['createdDate', 'id', 'name', 'status'])
  assert.equal(records[0].createdDate, '2026-10-24T14:03:53.000Z')
  assert.deepEqual(calls[0].args, ['notarytool', 'history', '--keychain-profile', 'fixture-notary', '--keychain', options.environment.CSC_KEYCHAIN, '--output-format', 'json'])
  assert.equal(calls[0].executable, '/usr/bin/xcrun')
  assert.equal(calls[0].options.timeout, 120_000)
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')), records)
  assert.deepEqual(JSON.parse(output.join('')), records)
  assert.ok(!output.join('').includes(secret))
  assert.ok(!output.join('').includes('unprinted tool diagnostics'))
  assert.equal((await lstat(file)).mode & 0o777, 0o600)
})

test('an empty history remains empty and makes no claim about the timed-out upload', async t => {
  const { options, file } = await fixture(t, { history: [] })
  assert.deepEqual(await queryNotaryStatus(options), [])
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')), [])
})

test('a supplied UUID selects info and checks the returned identity', async t => {
  const { options, calls } = await fixture(t, { ...record, message: 'not copied' })
  options.environment.NOTARY_SUBMISSION_ID = id
  assert.equal((await queryNotaryStatus(options))[0].id, id)
  assert.deepEqual(calls[0].args.slice(0, 3), ['notarytool', 'info', id])

  const mismatch = await fixture(t, { ...record, id: otherID })
  mismatch.options.environment.NOTARY_SUBMISSION_ID = id
  await assert.rejects(queryNotaryStatus(mismatch.options), /different notarization submission ID/)
  assert.equal(mismatch.output.length, 0)
  await assert.rejects(lstat(mismatch.file), { code: 'ENOENT' })
})

test('invalid submission IDs never reach notarytool', async t => {
  for (const invalid of ['../../submission', '--apple-id=private@example.test', `${id}\n`, 'not-a-uuid', 42]) {
    const { options, calls, output } = await fixture(t)
    options.environment.NOTARY_SUBMISSION_ID = invalid
    await assert.rejects(queryNotaryStatus(options), /must be empty or a notarization submission UUID/)
    assert.equal(calls.length, 0)
    assert.equal(output.length, 0)
  }
})

test('non-CI machines, other platforms and foreign teams are rejected before command execution', async t => {
  for (const mutation of [
    value => { value.platform = 'linux' },
    value => { value.environment.CI = 'false' },
    value => { value.environment.GITHUB_ACTIONS = 'false' },
    value => { value.environment.APPLE_TEAM_ID = 'OLDTEAM123' },
  ]) {
    const { options, calls } = await fixture(t)
    mutation(options)
    await assert.rejects(queryNotaryStatus(options), /macOS GitHub Actions runner|must be 6N6W6Z6739/)
    assert.equal(calls.length, 0)
  }
})

test('command failures do not expose raw errors, stderr or authentication arguments', async t => {
  const { options, output, file } = await fixture(t)
  const secret = 'PRIVATE-AUTH-ARGUMENT'
  options.command = async () => {
    const error = new Error(`xcrun --password ${secret}`)
    error.stdout = JSON.stringify({ password: secret })
    error.stderr = `authentication failed for ${secret}`
    throw error
  }
  await assert.rejects(queryNotaryStatus(options), error => {
    assert.equal(error.message, 'Apple notarization status query failed.')
    assert.ok(!String(error.stack).includes(secret))
    return true
  })
  assert.equal(output.length, 0)
  await assert.rejects(lstat(file), { code: 'ENOENT' })
})

test('malformed responses fail without printing the original response', async t => {
  for (const value of ['PRIVATE-INVALID-JSON', JSON.stringify({ history: [{ ...record, status: 'PRIVATE-UNKNOWN-STATUS' }] })]) {
    const { options, output, file } = await fixture(t)
    options.command = async () => ({ stdout: value, stderr: 'PRIVATE-STDERR' })
    await assert.rejects(queryNotaryStatus(options), /not valid JSON|incomplete notarization status metadata/)
    assert.equal(output.length, 0)
    await assert.rejects(lstat(file), { code: 'ENOENT' })
  }
})
