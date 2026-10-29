import { calendarParts, createDateTimeFormatter } from './timezone'

export function createMessageTimeFormatter(locale: string, timeZone: string) {
  const time = createDateTimeFormatter(locale, timeZone, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  const date = createDateTimeFormatter(locale, timeZone, { month: 'short', day: 'numeric' }, false)
  const datedYear = createDateTimeFormatter(locale, timeZone, { year: 'numeric', month: 'short', day: 'numeric' }, false)
  const full = createDateTimeFormatter(locale, timeZone, { year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZoneName: 'short' })
  const yesterdayLabel = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-1, 'day')
  return (value: string | undefined, now: Date) => {
    if (!value) return undefined
    const timestamp = new Date(value)
    if (!Number.isFinite(timestamp.getTime()) || timestamp.getUTCFullYear() <= 1) return undefined
    // Calendar dates, rather than elapsed hours, also work across DST changes.
    const timestampDay = calendarParts(timestamp, timeZone)
    const currentDay = calendarParts(now, timeZone)
    const day = (parts: typeof currentDay) => Date.UTC(parts.year, parts.month - 1, parts.day) / 86_400_000
    const difference = day(currentDay) - day(timestampDay)
    const prefix = difference === 0 ? '' : difference === 1 ? yesterdayLabel
      : timestampDay.year === currentDay.year ? date.format(timestamp) : datedYear.format(timestamp)
    return { label: [prefix, time.format(timestamp)].filter(Boolean).join(' '), full: full.format(timestamp), dateTime: timestamp.toISOString() }
  }
}
