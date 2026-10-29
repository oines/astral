interface CalendarParts { year: number; month: number; day: number; hour: number; minute: number; second: number }
const calendars = new Map<string, Intl.DateTimeFormat>()
const supported = new Map<string, boolean>()
export function fixedOffsetSeconds(timeZone: string) {
  const match = /^([+-])(\d{2}):(\d{2})$/.exec(timeZone)
  if (!match || Number(match[3]) > 59) return undefined
  return (match[1] === '-' ? -1 : 1) * (Number(match[2]) * 3600 + Number(match[3]) * 60)
}
export function timezoneSupported(timeZone: string) {
  if (!timeZone) return false
  if (fixedOffsetSeconds(timeZone) !== undefined) return true
  if (!supported.has(timeZone)) {
    try { new Intl.DateTimeFormat('en', { timeZone }).format(); supported.set(timeZone, !!timeZone) }
    catch { supported.set(timeZone, false) }
  }
  return supported.get(timeZone) === true
}
export function createDateTimeFormatter(locale: string, timeZone: string, options: Intl.DateTimeFormatOptions, markUnknown = true) {
  const offset = fixedOffsetSeconds(timeZone)
  const known = timezoneSupported(timeZone)
  const formatter = new Intl.DateTimeFormat(locale, { ...options, timeZone: offset !== undefined || !known ? 'UTC' : timeZone })
  return {
    format(date: Date) {
      const shifted = offset === undefined ? date : new Date(date.getTime() + offset * 1000)
      if ((offset !== undefined || !known) && options.timeZoneName) return formatter.formatToParts(shifted).map(part => part.type === 'timeZoneName' ? offset === undefined ? 'UTC' : `UTC${timeZone}` : part.value).join('')
      const value = formatter.format(shifted)
      // An opaque Server timezone can contain rules that ICU cannot reproduce.
      // Preserve the instant and label UTC explicitly; never use browser local.
      return !known && markUnknown && !options.timeZoneName ? `${value} UTC` : value
    },
  }
}
export function calendarParts(date: Date, timeZone: string): CalendarParts {
  const offset = fixedOffsetSeconds(timeZone)
  const nativeZone = offset !== undefined || !timezoneSupported(timeZone) ? 'UTC' : timeZone
  let formatter = calendars.get(nativeZone)
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-CA', { timeZone: nativeZone, calendar: 'gregory', numberingSystem: 'latn', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
    calendars.set(nativeZone, formatter)
  }
  const shifted = offset === undefined ? date : new Date(date.getTime() + offset * 1000)
  const parts = Object.fromEntries(formatter.formatToParts(shifted).map(part => [part.type, part.value]))
  return { year: Number(parts.year), month: Number(parts.month), day: Number(parts.day), hour: Number(parts.hour), minute: Number(parts.minute), second: Number(parts.second) }
}
function utc(parts: CalendarParts) {
  const date = new Date(0)
  date.setUTCFullYear(parts.year, parts.month - 1, parts.day)
  date.setUTCHours(parts.hour, parts.minute, parts.second, 0)
  return date.getTime()
}
function matches(a: CalendarParts, b: CalendarParts) { return a.year === b.year && a.month === b.month && a.day === b.day && a.hour === b.hour && a.minute === b.minute && a.second === b.second }
export function wallDateTime(iso: string, timeZone: string) {
  if (!timezoneSupported(timeZone)) return ''
  const date = new Date(iso)
  if (!Number.isFinite(date.getTime())) return ''
  const p = calendarParts(date, timeZone)
  return `${String(p.year).padStart(4, '0')}-${String(p.month).padStart(2, '0')}-${String(p.day).padStart(2, '0')}T${String(p.hour).padStart(2, '0')}:${String(p.minute).padStart(2, '0')}`
}
// A datetime-local value is a wall-clock time in Astral's configured timezone,
// never the browser's OS timezone. Reject gaps; choose the earlier occurrence
// for a repeated DST hour, matching the usual calendar-input interpretation.
export function wallTimeInstant(value: string, timeZone: string): Date | undefined {
  if (!timezoneSupported(timeZone)) return undefined
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value)
  if (!match) return undefined
  const p: CalendarParts = { year: Number(match[1]), month: Number(match[2]), day: Number(match[3]), hour: Number(match[4]), minute: Number(match[5]), second: Number(match[6] || 0) }
  const desired = utc(p)
  if (!matches(calendarParts(new Date(desired), 'UTC'), p)) return undefined
  const fixed = fixedOffsetSeconds(timeZone)
  if (fixed !== undefined) return new Date(desired - fixed * 1000)
  const offsets = new Set<number>()
  // Capture offsets on both sides of a calendar transition. The candidate is
  // accepted only after an exact wall-clock round trip, including date fields.
  for (let hours = -36; hours <= 36; hours += 6) {
    const instant = desired + hours * 3_600_000
    offsets.add(utc(calendarParts(new Date(instant), timeZone)) - instant)
  }
  const candidates = [...offsets].map(offset => desired - offset).filter(instant => matches(calendarParts(new Date(instant), timeZone), p)).sort((a, b) => a - b)
  return candidates.length ? new Date(candidates[0]!) : undefined
}
export function formatDateTime(value: string | undefined, locale: string, timeZone: string, options: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'short' }) {
  if (!value) return '—'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime()) || date.getUTCFullYear() <= 1) return '—'
  return createDateTimeFormatter(locale, timeZone, options).format(date)
}
