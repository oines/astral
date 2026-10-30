import { sessionEntry } from '@astral/sdk'
import { request } from './api'
import type { Entry } from './types'

// Entries are immutable. Reopening a process should reuse its detail response,
// while a lifecycle update uses a different Entry ID and obtains fresh facts.
const cache = new Map<string, { promise: Promise<Entry>; bytes: number }>()
const maxBytes = 8 * 1024 * 1024
let bytes = 0
function trim() {
  while (cache.size > 40 || bytes > maxBytes) {
    const oldest = cache.keys().next().value
    if (oldest === undefined) return
    bytes -= cache.get(oldest)!.bytes
    cache.delete(oldest)
  }
}
export function toolEntry(sessionID: string, entryID: string): Promise<Entry> {
  const key = `${sessionID}:${entryID}`
  const existing = cache.get(key)
  if (existing) { cache.delete(key); cache.set(key, existing); return existing.promise }
  const item = { promise: undefined as unknown as Promise<Entry>, bytes: 0 }
  item.promise = request(sessionEntry({ path: { id: sessionID, entry: entryID } })).then(entry => {
    if (cache.get(key) === item) {
      item.bytes = JSON.stringify(entry).length * 2
      bytes += item.bytes
      trim()
    }
    return entry
  }, error => { if (cache.get(key) === item) cache.delete(key); throw error })
  cache.set(key, item)
  trim()
  return item.promise
}
