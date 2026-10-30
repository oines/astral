import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import { useSession } from './useSession'
import { transcriptGroups } from '../lib/session-state'
import type { Entry, Snapshot } from '../lib/types'

const mocks = vi.hoisted(() => ({ get: vi.fn(), app: undefined as unknown as { authorized: boolean; sessionViews: Record<string, unknown> } }))
vi.mock('@astral/sdk/client', () => ({ client: { get: mocks.get, setConfig: vi.fn(), interceptors: { response: { use: vi.fn() } } } }))
vi.mock('../stores/app', () => ({ useAppStore: () => mocks.app }))
vi.mock('../stores/auth', () => ({ useAuthStore: () => ({ refresh: vi.fn() }) }))
class Source {
  static all: Source[] = []
  handlers = new Map<string, (event: Event) => void>()
  onerror?: () => void
  constructor() { Source.all.push(this) }
  addEventListener(kind: string, handler: (event: Event) => void) { this.handlers.set(kind, handler) }
  close() {}
  emit(kind: string, data: unknown) { this.handlers.get(kind)?.({ data: JSON.stringify(data) } as unknown as Event) }
}
function entry(id: string, sequence: number): Entry {
  return { id, sequence, kind: 'message', session_id: 'chat', version: sequence, time: '', message: { role: 'user', content: [{ type: 'text', text: id }] } }
}
function snapshot(entries: Entry[], cursor?: string, epoch = 'server'): Snapshot {
  return { epoch, history: { entries, next_cursor: cursor, head_id: 'head' }, trace: { entries: [], head_id: 'head' }, view: { id: 'chat', run_id: 'run', event_sequence: 0 } as Snapshot['view'] }
}
let scope: EffectScope
beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('EventSource', Source)
  Source.all = []
  mocks.app = reactive({ authorized: true, sessionViews: {} })
  mocks.get.mockReset()
  scope = effectScope()
})
afterEach(() => { scope.stop(); vi.useRealTimers(); vi.unstubAllGlobals() })
async function reconnect(session: ReturnType<typeof useSession>) {
  session.reconnect()
  await vi.advanceTimersByTimeAsync(1000)
  return Source.all.at(-1)!
}

describe('session transport reconciliation', () => {
  it('keeps loaded history and live output during a connection interruption', async () => {
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([entry('old', 1)], 'older'))
    Source.all[0]!.emit('event', { epoch: 'server', core: { type: 'message_update', sequence: 1, run_id: 'run', attempt_id: 'attempt', model: { type: 'text_delta', content_index: 0, text: 'still here' } } })
    const source = await reconnect(session)
    expect(session.state.value.previews.attempt?.parts[0]?.text).toBe('still here')
    source.emit('snapshot', snapshot([entry('old', 1), entry('new', 2)], 'latest-cursor'))
    expect(session.state.value.historyCursor).toBe('older')
    expect(session.state.value.previews.attempt?.parts[0]?.text).toBe('still here')
    source.emit('snapshot', snapshot([entry('new', 2)], 'latest-cursor', 'restarted-server'))
    expect(session.state.value.previews).toEqual({})
    expect(session.state.value.entries.old).toBeDefined()
  })

  it('rejects a pagination response from before a replacement snapshot even in the same server epoch', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([entry('current', 5)], 'older'))
    const pending = session.older()
    const source = await reconnect(session)
    source.emit('snapshot', snapshot([entry('current', 5), entry('new', 6)], 'new-tail'))
    resolve({ data: { entries: [entry('stale', 1)], next_cursor: 'stale-cursor' } })
    expect(await pending).toBe(true)
    expect(session.state.value.historyCursor).toBe('older')
    expect(session.state.value.entries.stale).toBeUndefined()
    expect(session.loadingHistory.value).toBe(false)
  })

  it('fills a long outage gap without losing the old pagination boundary or a new streaming commit', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([entry('old', 2)], 'oldest'))
    const source = await reconnect(session)
    source.emit('snapshot', snapshot([entry('recent', 9)], 'gap'))
    expect(session.state.value.entries.old).toBeDefined()
    expect(mocks.get).toHaveBeenCalledWith(expect.objectContaining({ query: expect.objectContaining({ cursor: 'gap' }) }))
    source.emit('event', { epoch: 'server', core: { sequence: 1, type: 'entry_committed' }, entry: entry('live', 10) })
    resolve({ data: { entries: [entry('old', 2), entry('middle', 6)], next_cursor: 'overlap' } })
    await nextTick()
    await nextTick()
    expect(Object.keys(session.state.value.entries).sort()).toEqual(['live', 'middle', 'old', 'recent'])
    expect(session.state.value.historyCursor).toBe('oldest')
    expect(session.loadingHistory.value).toBe(false)
  })
  it('freezes A after a dropped B, ignores same-attempt C, then replaces it with committed ABC', async () => {
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([]))
    const delta = (text: string, sequence: number) => ({ epoch: 'server', core: { type: 'message_update', sequence, run_id: 'run', attempt_id: 'attempt', model: { type: 'text_delta', content_index: 0, text } } })
    Source.all[0]!.emit('event', delta('A', 1))
    const source = await reconnect(session)
    const resumed = snapshot([])
    resumed.view.event_sequence = 2 // B was emitted while disconnected.
    source.emit('snapshot', resumed)
    source.emit('event', delta('C', 3))
    expect(session.state.value.previews.attempt?.parts[0]?.text).toBe('A')
    expect(JSON.stringify(transcriptGroups(session.state.value))).not.toContain('AC')
    const accepted: Entry = { ...entry('accepted', 8), run_id: 'run', generation: { attempt_id: 'attempt', configuration_id: 'cfg', context_digest: 'digest', model: { id: 'model', context_window: 1000, max_output_tokens: 100 } }, message: { role: 'assistant', content: [{ type: 'text', text: 'ABC' }] } }
    source.emit('event', { epoch: 'server', core: { type: 'entry_committed', sequence: 4 }, entry: accepted })
    expect(session.state.value.previews.attempt).toBeUndefined()
    expect(transcriptGroups(session.state.value)[0]?.blocks).toContainEqual({ kind: 'text', key: 'attempt:0', text: 'ABC' })
  })

  it('shares reconnect gap work with scroll prefetch and permits the next older page', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(done => { resolve = done }))
      .mockResolvedValueOnce({ data: { entries: [entry('oldest', 1)] } })
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([entry('old', 2)], 'oldest-page'))
    const source = await reconnect(session)
    source.emit('snapshot', snapshot([entry('new', 9)], 'gap'))
    const scrollPrefetch = session.older()
    expect(mocks.get).toHaveBeenCalledTimes(1)
    resolve({ data: { entries: [entry('old', 2), entry('middle', 5)], next_cursor: 'overlap' } })
    expect(await scrollPrefetch).toBe(true)
    expect(session.state.value.historyCursor).toBe('oldest-page')
    expect(await session.older()).toBe(true)
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(session.state.value.entries.oldest).toBeDefined()
  })

})
