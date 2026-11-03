import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { acceptedSubmission, combineNotarizationReports, finalize, resumeSubmission, staple, submit, verifyNotarizedApp, waitForSubmission } from './mac-notarize.mjs'

const submissionID = '12345678-1234-4234-8234-123456789abc'
const otherSubmissionID = '87654321-4321-4321-8321-cba987654321'
const configuration = { profile: 'private-profile', keychain: '/private/signing.keychain-db' }
const artifactSHA = 'a'.repeat(64)
const completedUpload = { artifact_sha256: artifactSHA, upload_status: 'completed', submit_exit_code: 0 }

function submissionHarness(responses) {
  const calls = []
  const records = []
  const logs = []
  const events = []
  return {
    calls, records, logs, events,
    dependencies: {
      fingerprint: async () => artifactSHA,
      attempts: 1,
      pause: async () => {},
      run: async (program, args, options) => {
        calls.push({ program, args, options })
        events.push(args[1])
        assert.ok(responses.length, 'unexpected notarytool command')
        const response = responses.shift()
        if (response instanceof Error) throw response
        return { stdout: typeof response === 'string' ? response : JSON.stringify(response) }
      },
      record: async value => { records.push(value); events.push('record') },
      log: value => { logs.push(value); events.push('log') },
    },
  }
}

test('only a terminal Apple Accepted response approves a notarization', () => {
  const id = '12345678-1234-4234-8234-123456789abc'
  assert.deepEqual(acceptedSubmission({ id, status: 'Accepted' }), { id, status: 'Accepted' })
  for (const status of ['In Progress', 'Invalid', 'Rejected', undefined]) {
    assert.throws(() => acceptedSubmission({ id, status }), /not Accepted/)
  }
  for (const invalid of ['', '../../submission', 'accepted-without-a-submission-id']) {
    assert.throws(() => acceptedSubmission({ id: invalid, status: 'Accepted' }), /not Accepted/)
  }
})

test('upload saves its ID before a separate bounded native wait and only records safe fields', async () => {
  const harness = submissionHarness([
    { id: submissionID, path: '/private/archive.zip', message: 'private-upload-output', profile: configuration.profile },
    { id: submissionID.toUpperCase(), status: 'Accepted', createdDate: '2026-10-24T08:00:00Z', name: 'private-response-name', keychain: configuration.keychain },
  ])
  assert.deepEqual(await submit('/private/archive/Astral.dmg', configuration, harness.dependencies), { id: submissionID, status: 'Accepted' })
  assert.deepEqual(harness.events, ['submit', 'record', 'log', 'wait', 'record', 'log'])
  assert.deepEqual(harness.records, [
    { id: submissionID, name: 'Astral.dmg', ...completedUpload },
    { id: submissionID, name: 'Astral.dmg', ...completedUpload, status: 'Accepted', createdDate: '2026-10-24T08:00:00.000Z' },
  ])
  const [upload, wait] = harness.calls
  assert.equal(upload.program, 'xcrun')
  assert.ok(upload.args.includes('--no-wait'))
  assert.ok(!upload.args.includes('--wait'))
  assert.deepEqual(wait.args, ['notarytool', 'wait', submissionID, '--keychain-profile', configuration.profile, '--keychain', configuration.keychain, '--timeout', '2m', '--output-format', 'json'])
  assert.equal(wait.options.timeout, 180_000)
  assert.deepEqual(harness.logs, [`Apple notarization ${submissionID}: upload CLI completed (exit 0)`, `Apple notarization ${submissionID}: Accepted`])
  assert.doesNotMatch(JSON.stringify([harness.records, harness.logs]), /private|profile|keychain|message|path/)
})

test('a wait timeout records In Progress for the original ID and blocks release without calling it rejection', async () => {
  const harness = submissionHarness([
    { id: submissionID },
    new Error('private wait error with auth arguments'),
    { id: submissionID, name: 'Astral.dmg', status: 'In Progress', createdDate: '2026-10-24T08:00:00Z' },
  ])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /not Accepted after the bounded wait/)
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info'])
  assert.equal(harness.calls[2].args[2], submissionID)
  assert.equal(harness.records.at(-1).status, 'In Progress')
  assert.equal(harness.records.at(-1).upload_status, 'completed')
  assert.equal(harness.records.at(-1).submit_exit_code, 0)
})

test('the same submission confirmed Accepted by info can complete after an interrupted wait', async () => {
  const harness = submissionHarness([
    { id: submissionID }, new Error('transport interrupted'), { id: submissionID, status: 'Accepted' },
  ])
  assert.deepEqual(await submit('/private/Astral.dmg', configuration, harness.dependencies), { id: submissionID, status: 'Accepted' })
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info'])
  assert.deepEqual(harness.records.at(-1), { id: submissionID, name: 'Astral.dmg', ...completedUpload, status: 'Accepted' })
})

test('a failed status query retains the upload ID but blocks approval without leaking credentials', async () => {
  const harness = submissionHarness([
    { id: submissionID, createdDate: 'not a date', status: 'private-unknown-status' },
    Object.assign(new Error('private wait arguments'), { stdout: 'private malformed output', stderr: configuration.profile }),
    Object.assign(new Error(`private info arguments ${configuration.keychain}`), { stdout: 'private output' }),
  ])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /not Accepted after the bounded wait/)
  assert.deepEqual(harness.records, [{ id: submissionID, name: 'Astral.dmg', ...completedUpload }])
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info'])
  assert.doesNotMatch(JSON.stringify([harness.records, harness.logs]), /private|profile|keychain/)
})

test('an unrelated Accepted ID never approves the original submission or starts another upload', async () => {
  const harness = submissionHarness([
    { id: submissionID },
    { id: otherSubmissionID, status: 'Accepted' },
    { id: otherSubmissionID, status: 'Accepted' },
  ])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /Unexpected Apple submission ID/)
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait'])
  assert.deepEqual(harness.records, [{ id: submissionID, name: 'Astral.dmg', ...completedUpload }])
})

test('malformed wait JSON falls back to info for the original submission only', async () => {
  const harness = submissionHarness([{ id: submissionID }, 'private malformed JSON', { id: submissionID, status: 'Invalid' }])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /not Accepted \(Invalid\)/)
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info'])
  assert.equal(harness.records.at(-1).status, 'Invalid')
})

test('invalid upload IDs fail safely before waiting, querying or writing diagnostics', async () => {
  for (const response of [{ id: '../../private-secret' }, { status: 'Accepted' }, 'private malformed JSON', new Error('private upload error')]) {
    const harness = submissionHarness([response])
    await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), error => {
      assert.match(error.message, /upload did not return a valid submission ID/)
      assert.doesNotMatch(error.message, /private/)
      return true
    })
    assert.equal(harness.calls.length, 1)
    assert.deepEqual(harness.records, [])
    assert.deepEqual(harness.logs, [])
  }
})

test('a failed upload with an ID records only unconfirmed evidence and never waits, queries or retries', async () => {
  for (const failure of [
    { code: 75, expectedCode: 75 },
    { code: 'ETIMEDOUT', signal: 'SIGTERM', killed: true, expectedCode: null },
    { code: null, signal: 'SIGTERM', killed: true, expectedCode: null },
    { code: Number.MAX_SAFE_INTEGER + 1, expectedCode: null },
    { code: 'private untrusted exit code', expectedCode: null },
  ]) {
    const uploadError = Object.assign(new Error(`private upload arguments ${configuration.profile}`), {
      code: failure.code, signal: failure.signal, killed: failure.killed,
      stdout: JSON.stringify({ id: submissionID, status: 'In Progress', createdDate: '2026-10-24T08:00:00Z', path: '/private/archive.zip' }),
      stderr: `private authentication ${configuration.keychain}`,
    })
    const harness = submissionHarness([uploadError])
    await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), error => {
      assert.match(error.message, /upload was not confirmed/)
      assert.doesNotMatch(error.message, /private|ETIMEDOUT|SIGTERM/)
      return true
    })
    assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit'])
    assert.deepEqual(harness.events, ['submit', 'record', 'log'])
    assert.deepEqual(harness.records, [{ id: submissionID, name: 'Astral.dmg', artifact_sha256: artifactSHA, upload_status: 'unconfirmed', submit_exit_code: failure.expectedCode }])
    assert.deepEqual(harness.logs, [`Apple notarization ${submissionID}: upload unconfirmed; submit command failed`])
    assert.doesNotMatch(JSON.stringify([harness.records, harness.logs]), /private|In Progress|createdDate|signal|killed/)
  }
})

test('even an Accepted response and a numeric zero on a rejected upload cannot approve or start waiting', async () => {
  const uploadError = Object.assign(new Error('private failed upload'), {
    code: 0,
    stdout: JSON.stringify({ id: submissionID, status: 'Accepted', path: '/private/archive.zip' }),
  })
  const harness = submissionHarness([uploadError])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /upload was not confirmed/)
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit'])
  assert.deepEqual(harness.records, [{ id: submissionID, name: 'Astral.dmg', artifact_sha256: artifactSHA, upload_status: 'unconfirmed', submit_exit_code: 0 }])
  assert.doesNotMatch(JSON.stringify([harness.records, harness.logs]), /Accepted|private/)
})

test('a resolved upload command with malformed JSON cannot claim completion or start waiting', async () => {
  const harness = submissionHarness(['private malformed JSON'])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), error => {
    assert.match(error.message, /upload did not return a valid submission ID/)
    assert.doesNotMatch(error.message, /private/)
    return true
  })
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit'])
  assert.deepEqual(harness.records, [])
  assert.deepEqual(harness.logs, [])
})

test('a diagnostic write failure stops before waiting and hides the underlying filesystem error', async () => {
  const harness = submissionHarness([{ id: submissionID }])
  harness.dependencies.record = async () => { throw new Error('private diagnostic path') }
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), error => {
    assert.match(error.message, /Could not save the Apple submission diagnostic/)
    assert.doesNotMatch(error.message, /private/)
    return true
  })
  assert.equal(harness.calls.length, 1)
  assert.deepEqual(harness.logs, [])
})

test('stapling retries five times with increasing waits and validates the fetched ticket', async () => {
  const calls = []
  const pauses = []
  const logs = []
  let attempts = 0
  await staple('/release/Astral.app', {
    run: async (program, args) => {
      calls.push({ program, args })
      if (args[1] === 'staple' && ++attempts < 5) throw new Error('CloudKit temporarily unavailable')
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: value => { logs.push(value) },
  })
  assert.equal(attempts, 5)
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
  assert.deepEqual(calls.at(-1), { program: 'xcrun', args: ['stapler', 'validate', '/release/Astral.app'] })
  assert.equal(logs.length, 6)
})

test('a fifth staple failure aborts without a final extra wait or validation', async () => {
  let attempts = 0
  const pauses = []
  const unavailable = new Error('CloudKit unavailable')
  await assert.rejects(staple('/release/Astral.dmg', {
    run: async (_program, args) => {
      assert.equal(args[1], 'staple')
      attempts++
      throw unavailable
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: () => {},
  }), error => error === unavailable)
  assert.equal(attempts, 5)
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
})

test('a first validation failure repeats staple and validate before succeeding', async () => {
  const calls = []
  const pauses = []
  const logs = []
  let validations = 0
  await staple('/release/Astral.dmg', {
    run: async (program, args) => {
      assert.equal(program, 'xcrun')
      assert.equal(args[0], 'stapler')
      assert.equal(args[2], '/release/Astral.dmg')
      calls.push(args[1])
      if (args[1] === 'validate' && ++validations === 1) {
        throw Object.assign(new Error('do not serialize this child error'), { code: 65, stdout: 'Ticket not yet usable', stderr: 'retry diagnostic' })
      }
      return { stdout: `${args[1]} succeeded`, stderr: '' }
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: value => { logs.push(JSON.parse(value.slice('Stapler diagnostic: '.length))) },
  })
  assert.deepEqual(calls, ['staple', 'validate', 'staple', 'validate'])
  assert.deepEqual(pauses, [20_000])
  assert.deepEqual(logs, [
    { target: '/release/Astral.dmg', attempt: 1, phase: 'staple', exit_code: 0, stdout: 'staple succeeded', stderr: '' },
    { target: '/release/Astral.dmg', attempt: 1, phase: 'validate', exit_code: 65, stdout: 'Ticket not yet usable', stderr: 'retry diagnostic' },
    { target: '/release/Astral.dmg', attempt: 2, phase: 'staple', exit_code: 0, stdout: 'staple succeeded', stderr: '' },
    { target: '/release/Astral.dmg', attempt: 2, phase: 'validate', exit_code: 0, stdout: 'validate succeeded', stderr: '' },
  ])
})

test('validation exhaustion still fails after five complete attempts without an extra wait', async () => {
  const calls = []
  const pauses = []
  const invalid = Object.assign(new Error('ticket remains invalid'), { code: 65 })
  await assert.rejects(staple('/release/Astral.app', {
    run: async (_program, args) => {
      calls.push(args[1])
      if (args[1] === 'validate') throw invalid
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: () => {},
  }), error => error === invalid)
  assert.deepEqual(calls, Array.from({ length: 5 }, () => ['staple', 'validate']).flat())
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
})

test('stapler diagnostics whitelist bounded command output and omit child error details', async () => {
  const logs = []
  const secret = 'DO-NOT-PRINT-CHILD-ERROR-OR-ENVIRONMENT'
  const unavailable = Object.assign(new Error(secret), {
    code: 'ENOENT', stdout: 's'.repeat(20_000), stderr: 'safe stapler error',
    environment: { APPLE_PASSWORD: secret }, command: secret,
  })
  await assert.rejects(staple('/release/Astral.dmg', {
    run: async () => { throw unavailable },
    pause: async () => {},
    log: value => { logs.push(value) },
  }), error => error === unavailable)
  assert.equal(logs.length, 5)
  assert.ok(logs.every(value => !value.includes(secret)))
  const diagnostic = JSON.parse(logs.at(-1).slice('Stapler diagnostic: '.length))
  assert.deepEqual(Object.keys(diagnostic).sort(), ['target', 'attempt', 'phase', 'exit_code', 'stdout', 'stderr'].sort())
  assert.equal(diagnostic.attempt, 5)
  assert.equal(diagnostic.phase, 'staple')
  assert.equal(diagnostic.exit_code, 'ENOENT')
  assert.equal(diagnostic.stdout, `${'s'.repeat(16_384)}\n[truncated]`)
  assert.equal(diagnostic.stderr, 'safe stapler error')
})

test('downloaded ZIP App requires its own stapled ticket while a DMG App still requires signature and Gatekeeper checks', async () => {
  for (const stapled of [true, false]) {
    const app = stapled ? '/extracted/Astral.app' : '/mounted/Astral.app'
    const calls = []
    await verifyNotarizedApp(app, 'arm64', {
      ...(stapled ? {} : { stapled: false }),
      inspect: async target => { calls.push({ program: 'verifyApp', args: [target] }) },
      run: async (program, args) => {
        calls.push({ program, args })
        return { stdout: program.endsWith('/lipo') ? 'arm64\n' : '', stderr: '' }
      },
    })
    assert.deepEqual(calls[0], { program: 'verifyApp', args: [app] })
    assert.equal(calls.some(call => call.args[0] === 'stapler' && call.args[1] === 'validate'), stapled)
    assert.deepEqual(calls.at(-1), { program: '/usr/sbin/spctl', args: ['--assess', '--type', 'execute', '--verbose=2', app] })
  }
})

test('an extracted ZIP retries ticket validation read-only before requiring Gatekeeper acceptance', async () => {
  const app = '/extracted/Astral.app'
  const calls = []
  const pauses = []
  const diagnostics = []
  let validations = 0
  await verifyNotarizedApp(app, 'x64', {
    inspect: async target => { calls.push({ program: 'verifyApp', args: [target] }) },
    run: async (program, args) => {
      calls.push({ program, args })
      if (program.endsWith('/lipo')) return { stdout: 'x86_64\n' }
      if (args[0] === 'stapler' && ++validations === 1) {
        throw Object.assign(new Error('private child error details'), {
          code: 68, stdout: 'Ticketing service temporarily unavailable', stderr: 'validation diagnostic',
          environment: { APPLE_PASSWORD: 'private credential' },
        })
      }
      return { stdout: 'The validate action worked!\n', stderr: '' }
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: value => { diagnostics.push(JSON.parse(value.slice('Stapler diagnostic: '.length))) },
  })
  assert.deepEqual(calls, [
    { program: 'verifyApp', args: [app] },
    { program: '/usr/bin/lipo', args: ['-archs', `${app}/Contents/MacOS/Astral`] },
    { program: 'xcrun', args: ['stapler', 'validate', app] },
    { program: 'xcrun', args: ['stapler', 'validate', app] },
    { program: '/usr/sbin/spctl', args: ['--assess', '--type', 'execute', '--verbose=2', app] },
  ])
  assert.deepEqual(pauses, [20_000])
  assert.deepEqual(diagnostics, [
    { target: app, attempt: 1, phase: 'validate', exit_code: 68, stdout: 'Ticketing service temporarily unavailable', stderr: 'validation diagnostic' },
    { target: app, attempt: 2, phase: 'validate', exit_code: 0, stdout: 'The validate action worked!\n', stderr: '' },
  ])
  assert.doesNotMatch(JSON.stringify(diagnostics), /private|credential|APPLE_PASSWORD/)
})

test('a missing extracted ZIP ticket fails after five read-only attempts without restapling or approving Gatekeeper', async () => {
  const app = '/extracted/Astral.app'
  const calls = []
  const pauses = []
  const diagnostics = []
  const missing = Object.assign(new Error('The stapled ticket is missing or invalid'), {
    code: 66, stdout: 'Ticket validation failed', stderr: '',
  })
  await assert.rejects(verifyNotarizedApp(app, 'arm64', {
    inspect: async () => {},
    run: async (program, args) => {
      calls.push({ program, args })
      if (program.endsWith('/lipo')) return { stdout: 'arm64\n' }
      assert.equal(program, 'xcrun')
      assert.deepEqual(args, ['stapler', 'validate', app])
      throw missing
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
    log: value => { diagnostics.push(JSON.parse(value.slice('Stapler diagnostic: '.length))) },
  }), error => error === missing)
  assert.equal(calls.length, 6)
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
  assert.equal(diagnostics.length, 5)
  assert.ok(diagnostics.every(value => value.phase === 'validate' && value.exit_code === 66))
  assert.equal(diagnostics.at(-1).attempt, 5)
})

test('extracted ZIP validation retries do not bypass signature, architecture or Gatekeeper rejection', async () => {
  const signatureFailure = new Error('Invalid App signature')
  await assert.rejects(verifyNotarizedApp('/extracted/Astral.app', 'arm64', {
    inspect: async () => { throw signatureFailure },
    run: async () => { assert.fail('signature rejection must stop before any other command') },
  }), error => error === signatureFailure)

  await assert.rejects(verifyNotarizedApp('/extracted/Astral.app', 'x64', {
    inspect: async () => {},
    run: async (program) => {
      assert.equal(program, '/usr/bin/lipo')
      return { stdout: 'arm64\n' }
    },
  }), /Unexpected packaged App architecture/)

  const gatekeeperFailure = new Error('Gatekeeper rejected the App')
  await assert.rejects(verifyNotarizedApp('/extracted/Astral.app', 'arm64', {
    inspect: async () => {},
    run: async (program) => {
      if (program.endsWith('/spctl')) throw gatekeeperFailure
      return { stdout: 'arm64\n' }
    },
    log: () => {},
  }), error => error === gatekeeperFailure)
})

test('outer DMG coverage does not exempt the enclosed App from architecture or Gatekeeper rejection', async () => {
  await assert.rejects(verifyNotarizedApp('/mounted/Astral.app', 'x64', {
    stapled: false, inspect: async () => {}, run: async () => ({ stdout: 'arm64\n' }),
  }), /Unexpected packaged App architecture/)
  await assert.rejects(verifyNotarizedApp('/mounted/Astral.app', 'arm64', {
    stapled: false,
    inspect: async () => {},
    run: async (program) => {
      if (program.endsWith('/spctl')) throw new Error('Gatekeeper rejected the App')
      return { stdout: 'arm64\n' }
    },
  }), /Gatekeeper rejected/)
})

test('pending notarization still verifies signatures and architecture without claiming Gatekeeper or stapling acceptance', async () => {
  const calls = []
  await verifyNotarizedApp('/extracted/Astral.app', 'arm64', {
    notarized: false,
    inspect: async app => { calls.push(['signature', app]) },
    run: async (program, args) => { calls.push([program, ...args]); return { stdout: 'arm64\n' } },
  })
  assert.equal(calls.length, 2)
  assert.equal(calls[0][0], 'signature')
  assert.equal(calls[1][0], '/usr/bin/lipo')
})

test('a terminal rejection returned by a successful upload is fatal before waiting', async () => {
  for (const status of ['Invalid', 'Rejected']) {
    const harness = submissionHarness([{ id: submissionID, status }])
    await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /not Accepted/)
    assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit'])
  }
})

test('pending and interrupted waits query the same Apple ID until Accepted without another upload', async () => {
  const harness = submissionHarness([
    { id: submissionID },
    { id: submissionID, status: 'In Progress' },
    { id: submissionID, status: 'In Progress' },
    new Error('temporary wait failure'),
    { id: submissionID, status: 'In Progress' },
    { id: submissionID, status: 'Accepted' },
  ])
  const pauses = []
  assert.deepEqual(await submit('/private/Astral.dmg', configuration, { ...harness.dependencies, attempts: 3, pause: async time => { pauses.push(time) } }), { id: submissionID, status: 'Accepted' })
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info', 'wait', 'info', 'wait'])
  assert.ok(harness.calls.slice(1).every(call => call.args[2] === submissionID))
  assert.deepEqual(pauses, [1_000, 2_000])
  assert.equal(harness.records.at(-1).status, 'Accepted')
})

test('pending submissions exhaust their budget without being approved or uploaded again', async () => {
  const harness = submissionHarness([
    { id: submissionID },
    { id: submissionID, status: 'In Progress' },
    { id: submissionID, status: 'In Progress' },
  ])
  await assert.rejects(submit('/private/Astral.dmg', configuration, harness.dependencies), /saved submission and input remain available for resume/)
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['submit', 'wait', 'info'])
  assert.equal(harness.records.at(-1).status, 'In Progress')
})

test('the elapsed wait deadline bounds command timeouts and prevents another query', async () => {
  let time = 0
  const calls = []
  await assert.rejects(waitForSubmission(submissionID, configuration, {
    timeout: 150_000, now: () => time, pause: async milliseconds => { time += milliseconds }, log: () => {},
    run: async (_program, args, options) => {
      calls.push({ args, timeout: options.timeout })
      time += Math.min(60_000, options.timeout)
      return { stdout: JSON.stringify({ id: submissionID, status: 'In Progress' }) }
    },
  }), /not Accepted after the bounded wait/)
  assert.deepEqual(calls.map(call => call.args[1]), ['wait', 'info', 'wait'])
  assert.equal(calls.at(-1).timeout, 29_000)
  assert.equal(time, 150_000)
})

test('resume accepts only the identical confirmed input and never issues submit', async () => {
  const receipt = { id: submissionID, name: 'Astral.dmg', ...completedUpload, status: 'In Progress', private: 'not copied' }
  const harness = submissionHarness([{ id: submissionID, status: 'Accepted', private: 'not copied' }])
  assert.deepEqual(await resumeSubmission('/private/Astral.dmg', receipt, configuration, harness.dependencies), { id: submissionID, status: 'Accepted' })
  assert.deepEqual(harness.calls.map(call => call.args[1]), ['wait'])
  assert.deepEqual(harness.records, [{ id: submissionID, name: 'Astral.dmg', ...completedUpload, status: 'Accepted' }])
  for (const change of [
    value => { value.upload_status = 'unconfirmed' },
    value => { value.name = 'another.dmg' },
    value => { value.artifact_sha256 = 'b'.repeat(64) },
    value => { value.source_sha = 'b'.repeat(40) },
  ]) {
    const changed = JSON.parse(JSON.stringify(receipt))
    change(changed)
    const rejected = submissionHarness([])
    await assert.rejects(resumeSubmission('/private/Astral.dmg', changed, configuration, rejected.dependencies), /Resume requires|input changed/)
    assert.deepEqual(rejected.calls, [])
  }
})

function notarizationReports() {
  const common = { version: '0.1.14', source_sha: 'a'.repeat(40), team_id: '6N6W6Z6739', certificate_expires_at: '2030-01-01T00:00:00.000Z' }
  const asset = name => ({ name, size: 100, sha256: 'b'.repeat(64) })
  return [
    { ...common, notarizations: [{ kind: 'binaries', id: submissionID, status: 'Accepted' }], assets: ['astral-darwin-amd64', 'astral-darwin-arm64'].map(asset) },
    ...['x64', 'arm64'].map((arch, index) => ({
      ...common, arch, native_tested: true,
      notarizations: [{ kind: 'dmg', arch, id: `${index ? 'd' : 'c'}2345678-1234-4234-8234-123456789abc`, status: 'Accepted' }],
      assets: ['zip', 'dmg'].map(extension => asset(`Astral-0.1.14-mac-${arch}.${extension}`)),
    })),
  ]
}

test('parallel native Mac evidence produces one deterministic report without signing credentials', () => {
  const reports = notarizationReports()
  const configuration = { version: '0.1.14', sourceSHA: 'a'.repeat(40), now: Date.parse('2026-11-03') }
  const combined = combineNotarizationReports(reports, configuration)
  assert.equal(combined.schema_version, 3)
  assert.deepEqual(combined.native_tested_arches, ['x64', 'arm64'])
  assert.equal(combined.notarization_status, 'accepted')
  assert.equal(combined.native_tested_arch, undefined)
  assert.equal(combined.notarizations.length, 3)
  assert.equal(combined.assets.length, 6)
  assert.deepEqual(combineNotarizationReports(reports.toReversed(), configuration), combined)
})

test('aggregation rejects partial native coverage, pending Apple status and conflicting evidence', () => {
  const configuration = { version: '0.1.14', sourceSHA: 'a'.repeat(40), now: Date.parse('2026-11-03') }
  for (const mutate of [
    values => { values[2].native_tested = false },
    values => { values[2].arch = 'x64' },
    values => { values[2].source_sha = 'b'.repeat(40) },
    values => { values[2].version = '0.1.15' },
    values => { values[2].team_id = 'OTHERTEAM1' },
    values => { values[2].certificate_expires_at = '2029-01-01T00:00:00Z' },
    values => { values.forEach(value => { value.certificate_expires_at = '2020-01-01T00:00:00Z' }) },
    values => { values[2].notarizations[0].status = 'In Progress' },
    values => { values[2].notarizations[0].status = 'Unknown' },
    values => { values[2].notarizations[0].id = submissionID },
    values => { values[2].notarizations[0].arch = 'x64' },
    values => { values[2].assets[0].name = values[1].assets[0].name },
    values => { values[2].assets.pop() },
  ]) {
    const reports = notarizationReports()
    mutate(reports)
    assert.throws(() => combineNotarizationReports(reports, configuration))
  }
})

test('final aggregation verifies the transferred bytes without Apple credentials and rejects package tampering', async t => {
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-parallel-mac-reports-'))
  t.after(() => rm(temporary, { recursive: true, force: true }))
  const directory = path.join(temporary, 'release')
  const programs = path.join(temporary, 'programs')
  await mkdir(path.join(directory, 'notarization'), { recursive: true })
  await mkdir(programs)
  const reports = notarizationReports()
  for (const [index, report] of reports.entries()) {
    for (const record of report.assets) {
      const bytes = Buffer.from(record.name)
      record.size = bytes.length
      record.sha256 = createHash('sha256').update(bytes).digest('hex')
      await writeFile(path.join(index === 0 ? programs : directory, record.name), bytes)
    }
    await writeFile(path.join(directory, 'notarization', `${['binaries', 'x64', 'arm64'][index]}.json`), JSON.stringify(report))
  }
  const options = { directory, programs, version: '0.1.14', sourceSHA: 'a'.repeat(40) }
  const combined = await finalize(options)
  assert.deepEqual(JSON.parse(await readFile(path.join(directory, 'mac-signing-report.json'), 'utf8')), combined)
  assert.deepEqual(combined.native_tested_arches, ['x64', 'arm64'])
  await writeFile(path.join(directory, 'Astral-0.1.14-mac-arm64.zip'), 'different bytes')
  await assert.rejects(finalize(options), /does not match final artifact bytes/)
})
