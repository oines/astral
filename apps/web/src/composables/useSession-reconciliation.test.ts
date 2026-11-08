import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import { useSession } from './useSession'
import { transcriptGroups } from '../lib/session-state'
import type { Entry, Snapshot } from '../lib/types'
import type { SubagentActivity } from '../lib/subagents'

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
  return { epoch, history: { entries, next_cursor: cursor, head_id: 'head' }, trace: { entries: [], head_id: 'head' }, view: { id: 'chat', run_id: 'run', event_sequence: 0 } as Snapshot['view'], subagent_activity: [], subagent_activity_cursor: '0' }
}
function activity(sequence: number): SubagentActivity {
  return { id: `child-event-${sequence}`, sequence, session_id: 'chat', child_session_id: 'child', name: 'Review', run_id: `run-${sequence}`, status: 'started', time: new Date(Date.UTC(2026, 10, 5, 0, 0, sequence)).toISOString() }
}
function timedEntry(id: string, sequence: number): Entry {
  return { ...entry(id, sequence), time: activity(sequence).time }
}
function shownActivities(session: ReturnType<typeof useSession>) {
  return transcriptGroups(session.state.value).flatMap(group => group.blocks)
    .filter(block => block.kind === 'subagent-status').map(block => block.activity.sequence)
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
  it('reveals retained status rows only as delayed main-history pages reach their time range', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', { ...snapshot([timedEntry('recent', 10)], 'older'), subagent_activity: [activity(2), activity(5), activity(12)], subagent_activity_cursor: '12' })
    expect(shownActivities(session)).toEqual([12])
    const pending = session.older()
    expect(shownActivities(session)).toEqual([12])
    expect(Object.keys(session.state.value.subagentActivity ?? {})).toHaveLength(3)
    resolve({ data: { entries: [timedEntry('middle', 4)], next_cursor: 'oldest' } })
    await pending
    expect(shownActivities(session)).toEqual([5, 12])
    const last = session.older()
    resolve({ data: { entries: [timedEntry('first', 1)] } })
    await last
    expect(shownActivities(session)).toEqual([2, 5, 12])
  })

  it('does not render outage status walls while delayed pages are still filling a reconnect gap', async () => {
    const pages: Array<(value: unknown) => void> = []
    mocks.get.mockImplementation(() => new Promise(done => { pages.push(done) }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', { ...snapshot([timedEntry('old', 2)], 'oldest'), subagent_activity: [activity(2)], subagent_activity_cursor: '2' })
    const source = await reconnect(session)
    source.emit('snapshot', { ...snapshot([timedEntry('recent', 9)], 'gap'), subagent_activity: [activity(2), activity(3), activity(5), activity(8), activity(10)], subagent_activity_cursor: '10' })
    expect(shownActivities(session)).toEqual([2, 10])
    expect(mocks.get).toHaveBeenCalledTimes(1)
    pages[0]!({ data: { entries: [timedEntry('middle', 6)], next_cursor: 'gap-2' } })
    await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(2))
    expect(shownActivities(session)).toEqual([2, 8, 10])
    pages[1]!({ data: { entries: [timedEntry('old', 2), timedEntry('earlier', 3)], next_cursor: 'overlap' } })
    await nextTick(); await nextTick()
    expect(shownActivities(session)).toEqual([2, 3, 5, 8, 10])
    expect(session.state.value.historyGaps).toBeUndefined()
    expect(session.state.value.historyCursor).toBe('oldest')
  })

  it('accepts a complete replacement snapshot without retaining a stale gap cursor', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([timedEntry('old', 2)], 'oldest'))
    const source = await reconnect(session)
    source.emit('snapshot', snapshot([timedEntry('recent', 9)], 'gap'))
    expect(session.state.value.historyGaps).toHaveLength(1)
    source.emit('snapshot', snapshot([timedEntry('old', 2), timedEntry('middle', 5), timedEntry('recent', 9)]))
    expect(session.state.value.historyCursor).toBeUndefined()
    expect(session.state.value.historyGaps).toBeUndefined()
    resolve({ data: { entries: [timedEntry('stale', 3)], next_cursor: 'stale' } })
    await nextTick(); await nextTick()
    expect(session.state.value.entries.stale).toBeUndefined()
    expect(mocks.get).toHaveBeenCalledTimes(1)
  })

  it('does not mistake trace overlap for a continuous main-history window', async () => {
    mocks.get.mockImplementation(() => new Promise(() => {}))
    const session = scope.run(() => useSession(ref('chat')))!
    const old = timedEntry('old', 2)
    Source.all[0]!.emit('snapshot', snapshot([old], 'oldest'))
    const source = await reconnect(session)
    const recent = snapshot([timedEntry('recent', 9)], 'gap')
    recent.trace.entries = [old]
    source.emit('snapshot', recent)
    expect(session.state.value.historyCursor).toBe('gap')
    expect(mocks.get).toHaveBeenCalledWith(expect.objectContaining({ query: expect.objectContaining({ cursor: 'gap' }) }))
  })

  it('keeps previously displayed child activity while deferring new outage activity', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', { ...snapshot([timedEntry('old', 2)], 'oldest'), subagent_activity: [activity(3), activity(4)], subagent_activity_cursor: '4' })
    expect(shownActivities(session)).toEqual([3, 4])
    const source = await reconnect(session)
    source.emit('snapshot', { ...snapshot([timedEntry('recent', 9)], 'gap'), subagent_activity: [activity(3), activity(4), activity(6), activity(10)], subagent_activity_cursor: '10' })
    expect(shownActivities(session)).toEqual([3, 4, 10])
    expect(session.state.value.historyGaps?.[0]?.activityAfter).toBe(4)
    resolve({ data: { entries: [timedEntry('middle', 5)], next_cursor: 'gap-2' } })
    await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(2))
    expect(shownActivities(session)).toEqual([3, 4, 6, 10])
    expect(session.state.value.historyGaps?.[0]?.activityAfter).toBe(4)
  })

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

  it('resumes a cached long-outage gap after navigation and rejects the abandoned page', async () => {
    const responses: Array<(value: unknown) => void> = []
    mocks.get.mockImplementation(() => new Promise(done => { responses.push(done) }))
    const id = ref('chat')
    const session = scope.run(() => useSession(id))!
    Source.all[0]!.emit('snapshot', snapshot([entry('old', 2)], 'oldest-page'))
    const source = await reconnect(session)
    source.emit('snapshot', snapshot([entry('recent', 9)], 'gap'))
    expect(mocks.get).toHaveBeenCalledTimes(1)
    id.value = 'another'
    id.value = 'chat'
    expect(session.state.value.entries.old).toBeDefined()
    expect(session.state.value.entries.recent).toBeDefined()
    Source.all.at(-1)!.emit('snapshot', snapshot([entry('recent', 9)], 'latest-tail'))
    expect(mocks.get).toHaveBeenCalledTimes(2)
    responses[0]!({ data: { entries: [entry('stale', 1)], next_cursor: 'wrong' } })
    responses[1]!({ data: { entries: [entry('old', 2), entry('middle', 6)], next_cursor: 'overlap' } })
    await nextTick(); await nextTick()
    expect(session.state.value.entries.stale).toBeUndefined()
    expect(session.state.value.entries.middle).toBeDefined()
    expect(session.state.value.historyCursor).toBe('oldest-page')
  })

  it('loads older child activity after normal message history has reached its end', async () => {
    mocks.get.mockResolvedValue({ data: { entries: [], head_id: '', subagent_activity: [activity(1)], subagent_activity_cursor: '3' } })
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', { ...snapshot([]), subagent_activity: [activity(3)], subagent_activity_cursor: '3', subagent_activity_next_cursor: '3' })
    expect(await session.older()).toBe(true)
    expect(mocks.get).toHaveBeenCalledWith(expect.objectContaining({ query: expect.objectContaining({ cursor: undefined, activity_cursor: '3' }) }))
    expect(Object.keys(session.state.value.subagentActivity ?? {}).sort()).toEqual(['child-event-1', 'child-event-3'])
    expect(session.state.value.historyCursor).toBeUndefined()
    expect(await session.older()).toBe(false)
    expect(mocks.get).toHaveBeenCalledTimes(1)
  })

  it('fills a child-activity outage gap without reading unrelated older messages or losing live states', async () => {
    let resolve!: (value: unknown) => void
    mocks.get.mockImplementation(() => new Promise(done => { resolve = done }))
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', { ...snapshot([entry('old', 2)], 'older-messages'), subagent_activity: [activity(2)], subagent_activity_cursor: '2', subagent_activity_next_cursor: '2' })
    const source = await reconnect(session)
    source.emit('snapshot', { ...snapshot([entry('old', 2)], 'recent-messages'), subagent_activity: [activity(8), activity(9)], subagent_activity_cursor: '9', subagent_activity_next_cursor: '8' })
    expect(mocks.get).toHaveBeenCalledWith(expect.objectContaining({ query: expect.objectContaining({ cursor: undefined, activity_cursor: '8' }) }))
    source.emit('event', { epoch: 'server', subagent_activity: [activity(10)], subagent_activity_cursor: '10' })
    resolve({ data: { entries: [], head_id: '', subagent_activity: [activity(2), activity(5)], subagent_activity_cursor: '9', subagent_activity_next_cursor: '2' } })
    await nextTick(); await nextTick(); await nextTick()
    expect(Object.keys(session.state.value.subagentActivity ?? {}).sort()).toEqual(['child-event-10', 'child-event-2', 'child-event-5', 'child-event-8', 'child-event-9'])
    expect(session.state.value.subagentActivityCursor).toBe('10')
    expect(session.state.value.subagentActivityNextCursor).toBe('2')
    expect(session.state.value.subagentActivityBoundary).toBeUndefined()
    expect(session.state.value.historyCursor).toBe('older-messages')
    expect(mocks.get).toHaveBeenCalledTimes(1)
  })

  it('does not re-fetch recent child states when only earlier normal messages remain', async () => {
    mocks.get.mockResolvedValue({ data: { entries: [entry('older', 1)], head_id: '' } })
    const session = scope.run(() => useSession(ref('chat')))!
    Source.all[0]!.emit('snapshot', snapshot([entry('newer', 2)], 'older-message-page'))
    await session.older()
    expect(mocks.get).toHaveBeenCalledWith(expect.objectContaining({ query: expect.objectContaining({ cursor: 'older-message-page', activity_cursor: '0' }) }))
  })

})
