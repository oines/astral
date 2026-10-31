import { describe, expect, it } from 'vitest'
import { createSessionCache, sessionBytes } from './session-cache'
import { addEntry, emptyState } from './session-state'
import type { SessionView } from './types'

function state(id: string, text = id, agent = 'bot') {
  const state = emptyState()
  state.view = { id, agent_id: agent } as SessionView
  addEntry(state, { id: 'message', sequence: 1, session_id: id, version: 1, time: '', kind: 'message', message: { role: 'user', content: [{ type: 'text', text }] } })
  return state
}
describe('inactive session cache', () => {
  it('retains state, history recovery boundary, and reading position with a session-count LRU limit', () => {
    const cache = createSessionCache(2)
    const first = state('one')
    first.scrollPosition = { following: false, top: 123 }
    const boundary = { sequence: 12, cursor: 'older' }
    cache.put('one', { state: first, historyBoundary: boundary })
    cache.put('two', { state: state('two') })
    const revisited = cache.take('one')!
    expect(revisited.state).toBe(first)
    expect(revisited.historyBoundary).toBe(boundary)
    cache.put('one', revisited)
    cache.put('three', { state: state('three') })
    expect(cache.size).toBe(2)
    expect(cache.take('two')).toBeUndefined()
    expect(cache.take('one')!.state.scrollPosition).toEqual({ following: false, top: 123 })
  })
  it('evicts by retained-byte budget and does not retain a single oversized session', () => {
    const first = state('one', 'x'.repeat(1000))
    const limit = sessionBytes(first) + 20
    const cache = createSessionCache(8, limit)
    cache.put('one', { state: first })
    cache.put('two', { state: state('two', 'x'.repeat(1000)) })
    expect(cache.take('one')).toBeUndefined()
    expect(cache.size).toBe(1)
    expect(cache.bytes).toBeLessThanOrEqual(limit)
    cache.put('large', { state: state('large', 'x'.repeat(limit)) })
    expect(cache.take('large')).toBeUndefined()
    expect(cache.size).toBe(1)
  })
  it('counts the shared entry once and removes an entire deleted Bot or account cache', () => {
    const first = state('one')
    const sharedSize = sessionBytes(first)
    first.trace.message = structuredClone(first.trace.message!)
    expect(sessionBytes(first)).toBeGreaterThan(sharedSize)
    const cache = createSessionCache()
    cache.put('one', { state: first })
    cache.put('two', { state: state('two', 'text', 'another') })
    cache.removeAgent('bot')
    expect(cache.take('one')).toBeUndefined()
    expect(cache.size).toBe(1)
    cache.clear()
    expect(cache.size).toBe(0)
    expect(cache.bytes).toBe(0)
  })
})
