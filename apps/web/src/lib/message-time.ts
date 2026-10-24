function sameDay(first: Date, second: Date) {
  return first.getFullYear() === second.getFullYear() && first.getMonth() === second.getMonth() && first.getDate() === second.getDate()
}

export function createMessageTimeFormatter(locale: string) {
  const time = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  const date = new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric' })
  const datedYear = new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'short', day: 'numeric' })
  const full = new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZoneName: 'short' })
  const yesterdayLabel = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-1, 'day')
  return (value: string | undefined, now: Date) => {
    if (!value) return undefined
    const timestamp = new Date(value)
    if (!Number.isFinite(timestamp.getTime()) || timestamp.getUTCFullYear() <= 1) return undefined
    // Calendar dates, rather than elapsed hours, also work across DST changes.
    const yesterday = new Date(now)
    yesterday.setDate(yesterday.getDate() - 1)
    const prefix = sameDay(timestamp, now) ? '' : sameDay(timestamp, yesterday) ? yesterdayLabel
      : timestamp.getFullYear() === now.getFullYear() ? date.format(timestamp) : datedYear.format(timestamp)
    return { label: [prefix, time.format(timestamp)].filter(Boolean).join(' '), full: full.format(timestamp), dateTime: timestamp.toISOString() }
  }
}
