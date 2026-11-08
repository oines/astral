import { describe, expect, it } from 'vitest'
import { compareEventTime } from './event-time'

describe('source event time comparison', () => {
  it.each([
    ['2026-11-08T01:00:00.123100Z', '2026-11-08T01:00:00.123900Z'],
    ['2026-11-08T01:00:00.123100000Z', '2026-11-08T01:00:00.123100001Z'],
    ['2026-11-08T01:00:00.123Z', '2026-11-08T01:00:00.123000001Z'],
    ['2026-11-08T01:00:00Z', '2026-11-08T01:00:00.000000001Z'],
    ['2026-11-08T01:00:00.123900Z', '2026-11-08T01:00:00.124000Z'],
    ['2026-11-08T09:00:00.123100+08:00', '2026-11-08T01:00:00.123900Z'],
    ['2026-11-07T17:00:00.123100-08:00', '2026-11-08T01:00:00.123900Z'],
    ['1969-12-31T23:59:59.999100Z', '1969-12-31T23:59:59.999900Z'],
  ])('preserves the exact order of %s and %s', (earlier, later) => {
    expect(compareEventTime(earlier, later)).toBeLessThan(0)
    expect(compareEventTime(later, earlier)).toBeGreaterThan(0)
  })

  it.each([
    ['2026-11-08T01:00:00Z', '2026-11-08T01:00:00.000000000Z'],
    ['2026-11-08T01:00:00.1231Z', '2026-11-08T01:00:00.123100000Z'],
    ['2026-11-08T01:00:00.123100Z', '2026-11-08T09:00:00.123100+08:00'],
    ['2026-11-08T01:00:00.123100Z', '2026-11-07T17:00:00.123100-08:00'],
  ])('recognizes equivalent source times %s and %s', (a, b) => {
    expect(compareEventTime(a, b)).toBe(0)
  })

  it('keeps missing and invalid legacy times available to caller tie-breaks', () => {
    expect(compareEventTime('', '2026-11-08T01:00:00Z')).toBe(0)
    expect(compareEventTime('2026-11-08T01:00:00Z', 'invalid')).toBe(0)
    expect(compareEventTime('', '')).toBe(0)
  })
})
