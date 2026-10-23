import { describe, expect, it } from 'vitest'
import { createMessageTimeFormatter } from './message-time'

const format = createMessageTimeFormatter('zh-CN')
const local = (year: number, month: number, day: number, hour: number, minute: number) => new Date(year, month - 1, day, hour, minute)

describe('assistant message timestamps', () => {
  it('uses local calendar dates across midnight, not the UTC date in the record', () => {
    const message = local(2026, 10, 23, 23, 50).toISOString()
    expect(format(message, local(2026, 10, 23, 23, 55))?.label).toBe('23:50')
    expect(format(message, local(2026, 10, 24, 0, 10))?.label).toBe('昨天 23:50')
    expect(format(local(2026, 10, 24, 0, 5).toISOString(), local(2026, 10, 24, 0, 10))?.label).toBe('00:05')
  })

  it('keeps yesterday across a daylight-saving change even when more than 24 hours apart', () => {
    expect(format(local(2026, 11, 1, 0, 30).toISOString(), local(2026, 11, 2, 0, 15))?.label).toBe('昨天 00:30')
  })

  it('shows yesterday across New Year and includes the year only for older dates', () => {
    const now = local(2026, 1, 1, 12, 0)
    expect(format(local(2025, 12, 31, 14, 32).toISOString(), now)?.label).toBe('昨天 14:32')
    expect(format(local(2025, 12, 30, 14, 32).toISOString(), now)?.label).toBe('2025年12月30日 14:32')
    expect(format(local(2026, 10, 22, 14, 32).toISOString(), local(2026, 10, 24, 12, 0))?.label).toBe('10月22日 14:32')
  })

  it('does not invent timestamps for missing, invalid or unassigned records', () => {
    const now = local(2026, 10, 24, 12, 0)
    for (const time of [undefined, '', 'invalid', '0001-01-01T00:00:00Z']) expect(format(time, now)).toBeUndefined()
  })
})
