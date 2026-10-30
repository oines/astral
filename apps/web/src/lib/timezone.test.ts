import { describe, expect, it } from 'vitest'
import { createDateTimeFormatter, formatDateTime, timezoneSupported, wallDateTime, wallTimeInstant } from './timezone'

describe('global timezone wall-clock conversion', () => {
  it('handles exact fixed offsets without relying on ICU timezone-name support', () => {
    expect(timezoneSupported('+05:45')).toBe(true)
    expect(wallTimeInstant('2027-01-01T08:30', '+05:45')?.toISOString()).toBe('2027-01-01T02:45:00.000Z')
    expect(wallDateTime('2027-01-01T01:30:00Z', '-03:30')).toBe('2026-12-31T22:00')
    expect(createDateTimeFormatter('en-GB', '+05:45', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZoneName: 'short' }).format(new Date('2027-01-01T01:30:00Z'))).toBe('07:15 UTC+05:45')
  })
  it('marks unknown system-zone dates explicitly as UTC and refuses wall-clock conversion', () => {
    expect(timezoneSupported('System')).toBe(false)
    expect(wallDateTime('2027-01-01T01:30:00Z', 'System')).toBe('')
    expect(wallTimeInstant('2027-01-01T08:30', 'System')).toBeUndefined()
    expect(formatDateTime('2027-01-01T01:30:00Z', 'en-GB', 'System', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })).toBe('01:30 UTC')
  })
  it('round-trips a date and time in the configured timezone independently of the browser', () => {
    const instant = wallTimeInstant('2027-01-01T08:30', 'Asia/Shanghai')
    expect(instant?.toISOString()).toBe('2027-01-01T00:30:00.000Z')
    expect(wallDateTime(instant!.toISOString(), 'Asia/Shanghai')).toBe('2027-01-01T08:30')
    expect(wallDateTime(instant!.toISOString(), 'America/New_York')).toBe('2026-12-31T19:30')
  })
  it('rejects an invalid calendar date and a nonexistent New York DST wall time', () => {
    expect(wallTimeInstant('2027-02-30T10:00', 'UTC')).toBeUndefined()
    expect(wallTimeInstant('2027-03-14T02:30', 'America/New_York')).toBeUndefined()
    expect(wallTimeInstant('2027-03-14T03:30', 'America/New_York')?.toISOString()).toBe('2027-03-14T07:30:00.000Z')
    expect(wallTimeInstant('2027-01-01T24:00', 'UTC')).toBeUndefined()
  })
  it('uses the earlier occurrence of a repeated New York DST hour deterministically', () => {
    expect(wallTimeInstant('2027-11-07T01:30', 'America/New_York')?.toISOString()).toBe('2027-11-07T05:30:00.000Z')
    expect(wallDateTime('2027-11-07T05:30:00Z', 'America/New_York')).toBe('2027-11-07T01:30')
    expect(wallDateTime('2027-11-07T06:30:00Z', 'America/New_York')).toBe('2027-11-07T01:30')
  })
  it('formats authentication and expiry timestamps in the same configured timezone', () => {
    expect(formatDateTime('2027-01-01T01:30:00Z', 'en-GB', 'America/New_York', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })).toBe('31/12/2026, 20:30')
    for (const value of [undefined, '', 'invalid', '0001-01-01T00:00:00Z']) expect(formatDateTime(value, 'en', 'UTC')).toBe('—')
  })
})
