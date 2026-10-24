import assert from 'node:assert/strict'
import test from 'node:test'
import { acceptedSubmission } from './mac-notarize.mjs'

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
