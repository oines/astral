import { describe, expect, it } from 'vitest'
import { createMessageTimeFormatter } from './message-time'

const format = createMessageTimeFormatter('zh-CN', 'Asia/Shanghai')

describe('assistant message timestamps in the configured timezone', () => {
  it('labels unsupported Server-zone dates explicitly in UTC and preserves original instants', () => {
    const timestamp = '2026-10-23T23:50:00Z'
    const unknown = createMessageTimeFormatter('zh-CN', 'System')(timestamp, new Date('2026-10-24T00:10:00Z'))
    expect(unknown?.label).toBe('昨天 23:50 UTC')
    expect(unknown?.full).toContain('UTC')
    expect(unknown?.dateTime).toBe('2026-10-23T23:50:00.000Z')
  })
  it('uses the configured calendar dates across midnight rather than UTC or the browser timezone', () => {
    const message = '2026-10-23T15:50:00Z'
    expect(format(message, new Date('2026-10-23T15:55:00Z'))?.label).toBe('23:50')
    expect(format(message, new Date('2026-10-23T16:10:00Z'))?.label).toBe('昨天 23:50')
    expect(format('2026-10-23T16:05:00Z', new Date('2026-10-23T16:10:00Z'))?.label).toBe('00:05')
  })

  it('keeps yesterday across a daylight-saving change even when more than 24 hours apart', () => {
    const ny = createMessageTimeFormatter('zh-CN', 'America/New_York')
    expect(ny('2026-11-01T04:30:00Z', new Date('2026-11-02T05:15:00Z'))?.label).toBe('昨天 00:30')
  })

  it('shows yesterday across New Year and includes the year only for older dates', () => {
    const now = new Date('2026-01-01T04:00:00Z')
    expect(format('2025-12-31T06:32:00Z', now)?.label).toBe('昨天 14:32')
    expect(format('2025-12-30T06:32:00Z', now)?.label).toBe('2025年12月30日 14:32')
    expect(format('2026-10-22T06:32:00Z', new Date('2026-10-24T04:00:00Z'))?.label).toBe('10月22日 14:32')
  })
  it('changes display and yesterday classification with the global timezone while preserving the stored instant', () => {
    const timestamp = '2026-10-23T23:50:00Z'
    const now = new Date('2026-10-24T00:10:00Z')
    const utc = createMessageTimeFormatter('zh-CN', 'UTC')(timestamp, now)
    const ny = createMessageTimeFormatter('zh-CN', 'America/New_York')(timestamp, now)
    expect(utc?.label).toBe('昨天 23:50')
    expect(ny?.label).toBe('19:50')
    expect(utc?.dateTime).toBe('2026-10-23T23:50:00.000Z')
    expect(ny?.dateTime).toBe(utc?.dateTime)
  })

  it('does not invent timestamps for missing, invalid or unassigned records', () => {
    const now = new Date('2026-10-24T04:00:00Z')
    for (const time of [undefined, '', 'invalid', '0001-01-01T00:00:00Z']) expect(format(time, now)).toBeUndefined()
  })
})
