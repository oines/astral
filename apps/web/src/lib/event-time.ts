function submillisecond(time: string): number {
  const fraction = /\.(\d+)(?:Z|[+-]\d{2}:\d{2})$/i.exec(time)?.[1] ?? ''
  return Number(fraction.slice(3, 9).padEnd(6, '0'))
}

// Date.parse normalizes UTC offsets but retains only milliseconds. Preserve
// the remaining RFC3339Nano precision when source events share that millisecond.
// Missing legacy timestamps compare equally, leaving caller tie-breaks intact.
export function compareEventTime(a: string, b: string): number {
  const milliseconds = Date.parse(a) - Date.parse(b)
  if (!Number.isFinite(milliseconds)) return 0
  return milliseconds || submillisecond(a) - submillisecond(b)
}
