import assert from 'node:assert/strict'
import test from 'node:test'
import { acceptedSubmission, staple, verifyNotarizedApp } from './mac-notarize.mjs'

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

test('stapling retries five times with increasing waits and validates the fetched ticket', async () => {
  const calls = []
  const pauses = []
  let attempts = 0
  await staple('/release/Astral.app', {
    run: async (program, args) => {
      calls.push({ program, args })
      if (args[1] === 'staple' && ++attempts < 5) throw new Error('CloudKit temporarily unavailable')
    },
    pause: async milliseconds => { pauses.push(milliseconds) },
  })
  assert.equal(attempts, 5)
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
  assert.deepEqual(calls.at(-1), { program: 'xcrun', args: ['stapler', 'validate', '/release/Astral.app'] })
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
  }), error => error === unavailable)
  assert.equal(attempts, 5)
  assert.deepEqual(pauses, [20_000, 40_000, 60_000, 80_000])
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
