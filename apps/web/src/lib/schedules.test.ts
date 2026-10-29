import { describe, expect, it } from 'vitest'
import { repeatCron, repeatFields } from './schedules'

describe('schedule editor repeat rules', () => {
  it('round-trips daily and weekly times without changing their cron meaning', () => {
    const daily = repeatFields({ kind: 'cron', cron: '5 9 * * *' })
    expect(daily).toMatchObject({ kind: 'daily', time: '09:05' })
    expect(repeatCron(daily.kind, daily.time, daily.weekday, daily.cron)).toBe('5 9 * * *')
    const weekly = repeatFields({ kind: 'cron', cron: '30 18 * * 0' })
    expect(weekly).toMatchObject({ kind: 'weekly', time: '18:30', weekday: '0' })
    expect(repeatCron(weekly.kind, weekly.time, weekly.weekday, weekly.cron)).toBe('30 18 * * 0')
  })
  it('keeps advanced API-created cron expressions intact', () => {
    const cron = '*/15 8-18 * * MON-FRI'
    const fields = repeatFields({ kind: 'cron', cron })
    expect(fields.kind).toBe('cron')
    expect(repeatCron(fields.kind, fields.time, fields.weekday, fields.cron)).toBe(cron)
  })
  it('rejects invalid time inputs before sending them to the Server', () => {
    expect(() => repeatCron('daily', '24:00', '1', '')).toThrow()
    expect(() => repeatCron('weekly', '12:99', '1', '')).toThrow()
    expect(() => repeatCron('daily', '', '1', '')).toThrow()
  })
})
