import { toRaw } from 'vue'
import type { SessionState } from './session-state'

export interface HistoryBoundary { sequence: number; cursor?: string }
export interface CachedSession { state: SessionState; historyBoundary?: HistoryBoundary }

// A conservative retained-size estimate, counting shared Entry objects once.
// Stop as soon as a state cannot fit; large chats need no serialization copy.
export function sessionBytes(state: SessionState, limit = Infinity) {
  const stack: unknown[] = [toRaw(state)]
  const seen = new Set<object>()
  let bytes = 0
  while (stack.length && bytes <= limit) {
    const value = stack.pop()
    if (typeof value === 'string') { bytes += value.length * 2; continue }
    if (typeof value === 'number' || typeof value === 'boolean') { bytes += 8; continue }
    if (!value || typeof value !== 'object') continue
    const raw = toRaw(value)
    if (seen.has(raw)) continue
    seen.add(raw)
    bytes += 64
    if (raw instanceof Set || Array.isArray(raw)) for (const item of raw) stack.push(item)
    else if (raw instanceof Map) for (const [key, item] of raw) stack.push(key, item)
    else for (const [key, item] of Object.entries(raw)) { bytes += key.length * 2 + 16; stack.push(item) }
  }
  return bytes
}

export function createSessionCache(maxSessions = 8, maxBytes = 32 * 1024 * 1024) {
  const sessions = new Map<string, { value: CachedSession; bytes: number }>()
  let bytes = 0
  function remove(id: string) {
    const cached = sessions.get(id)
    if (cached) { sessions.delete(id); bytes -= cached.bytes }
    return cached?.value
  }
  function put(id: string, value: CachedSession) {
    remove(id)
    const size = sessionBytes(value.state, maxBytes)
    if (!value.state.view || size > maxBytes || maxSessions < 1) return
    sessions.set(id, { value, bytes: size })
    bytes += size
    while (sessions.size > maxSessions || bytes > maxBytes) remove(sessions.keys().next().value!)
  }
  return {
    put,
    // Active state leaves the LRU. Its growth is measured when it leaves the
    // page again, so cached byte totals never rely on a changing live object.
    take: remove,
    remove,
    removeAgent(id: string) { for (const [sessionID, cached] of sessions) if (cached.value.state.view?.agent_id === id) remove(sessionID) },
    clear() { sessions.clear(); bytes = 0 },
    get size() { return sessions.size },
    get bytes() { return bytes },
  }
}
