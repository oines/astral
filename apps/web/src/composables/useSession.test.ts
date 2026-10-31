import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import type { SessionView, Snapshot } from '../lib/types'
import { useSession } from './useSession'

type Action = { name: string; args: unknown[]; after: (callback: () => void) => void }
const stores = vi.hoisted(() => ({
  app: undefined as unknown as { authorized: boolean; sessionViews: Record<string, SessionView>; $onAction: (callback: (action: Action) => void) => void },
  auth: undefined as unknown as { refresh: () => void; status: { username: string; desktop: boolean } },
}))
vi.mock('../stores/app', () => ({ useAppStore: () => stores.app }))
vi.mock('../stores/auth', () => ({ useAuthStore: () => stores.auth }))
vi.mock('../lib/api', () => ({
  errorText: (error: unknown) => String(error),
  object: (value: unknown) => value && typeof value === 'object' ? value : {},
  request: async (promise: Promise<{ data: unknown }>) => (await promise).data,
}))
class Stream extends EventTarget {
  static instances: Stream[] = []
  closed = false
  onerror?: () => void
  constructor(public url: string) { super(); Stream.instances.push(this) }
  close() { this.closed = true }
  send(name: string, value: unknown) { this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(value) })) }
  fail() { this.onerror?.() }
}
function snapshot(id: string, epoch: string): Snapshot {
  return {
    epoch,
    view: { id, agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Work', archived: false, pinned: false, source: 'web', rollout_id: 'rollout', head_id: 'head', event_sequence: 0, status: 'idle', can_continue: false, model_available: true, selection: { id: 'model' }, approvals: [], pending: [], unresolved: [] },
    history: { entries: [], head_id: 'head' }, trace: { entries: [], head_id: 'head' },
  }
}
let scope: EffectScope | undefined
let actionHandlers: Array<(action: Action) => void>
beforeEach(() => {
  actionHandlers = []
  stores.app = reactive({ authorized: true, sessionViews: {}, $onAction: (callback: (action: Action) => void) => { actionHandlers.push(callback) } })
  stores.auth = reactive({ refresh: vi.fn(), status: { username: 'owner', desktop: false } })
})
afterEach(() => {
  scope?.stop()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Stream.instances = []
})
function connect(id = 'one') {
  vi.useFakeTimers()
  vi.stubGlobal('EventSource', Stream)
  scope = effectScope()
  const sessionID = ref(id)
  const connection = scope.run(() => useSession(sessionID))!
  return { sessionID, ...connection }
}
describe('recovering a session connection', () => {
  it('keeps the last Core usage while waiting and restores it from a reconnect snapshot without polling', async () => {
    const fetch = vi.fn(() => { throw new Error('usage must come from the Core subscription') })
    vi.stubGlobal('fetch', fetch)
    const live = connect()
    const first = snapshot('one', 'server')
    first.view.context_usage = { entry_id: 'reported', model_ref: 'model', input_tokens: 27707, context_window: 1000000 }
    const answer = { id: 'answer', session_id: 'one', kind: 'message', version: 7, sequence: 1, time: '2026-10-24T00:00:00Z', message: { role: 'assistant', content: [{ type: 'text', text: 'Keep this response visible while reconnecting.' }] } }
    first.history.entries = [answer]
    const stream = Stream.instances[0]!
    stream.send('snapshot', first)
    stream.send('event', { epoch: 'server', core: { sequence: 1, session_id: 'one', type: 'attempt_start' } })
    await vi.advanceTimersByTimeAsync(10000)
    expect(live.state.value.view?.context_usage).toEqual(first.view.context_usage)
    expect(fetch).not.toHaveBeenCalled()
    const newer = { ...first.view, event_sequence: 2, context_usage: { ...first.view.context_usage, entry_id: 'newer', input_tokens: 29000 } }
    stream.send('event', { epoch: 'server', core: { sequence: 2, session_id: 'one', type: 'entry_committed' }, view: newer })
    expect(live.state.value.view?.context_usage?.input_tokens).toBe(29000)
    stream.fail()
    expect(live.state.value.view).toBeDefined()
    expect(live.state.value.entries.answer).toEqual(answer)
    await vi.advanceTimersByTimeAsync(1000)
    const restored = snapshot('one', 'restarted')
    restored.view = newer
    restored.history = first.history
    Stream.instances[1]!.send('snapshot', restored)
    expect(live.state.value.view?.context_usage).toEqual(newer.context_usage)
    expect(live.state.value.entries.answer).toEqual(answer)
    expect(fetch).not.toHaveBeenCalled()
  })
  it('restores retry state from a new snapshot without submitting or continuing a model request', async () => {
    const fetch = vi.fn(() => { throw new Error('reconnection must only open the event stream') })
    vi.stubGlobal('fetch', fetch)
    const live = connect()
    const original = Stream.instances[0]!
    original.send('snapshot', snapshot('one', 'server'))
    original.fail()
    await vi.advanceTimersByTimeAsync(1000)
    const restored = snapshot('one', 'server')
    restored.view = { ...restored.view, event_sequence: 20, status: 'running', can_continue: false, run_id: 'run', attempt: { attempt_id: 'a', run_id: 'run', turn_id: 'turn', purpose: 'generation', attempt: 1, max_attempts: 3, state: 'retrying', next_retry_at: '2026-10-22T00:00:10Z' } }
    Stream.instances[1]!.send('snapshot', restored)
    expect(live.state.value.view?.attempt).toEqual(restored.view.attempt)
    expect(live.state.value.sequence).toBe(20)
    expect(fetch).not.toHaveBeenCalled()
  })
  it('reopens after a fatal proxy error instead of depending on native automatic retries', async () => {
    const live = connect()
    const original = Stream.instances[0]!
    original.send('snapshot', snapshot('one', 'before-restart'))
    expect(live.connection.value).toBe('connected')
    original.fail()
    expect(original.closed).toBe(true)
    expect(live.connection.value).toBe('reconnecting')
    await vi.advanceTimersByTimeAsync(1000)
    const restored = Stream.instances[1]!
    restored.send('snapshot', snapshot('one', 'after-restart'))
    expect(live.connection.value).toBe('connected')
    expect(live.state.value.epoch).toBe('after-restart')
    // A callback queued by a closing connection cannot undo the recovery.
    original.fail()
    original.send('snapshot', snapshot('one', 'stale'))
    expect(live.connection.value).toBe('connected')
    expect(live.state.value.epoch).toBe('after-restart')
  })
  it('ignores the old session after navigation and removes its live overlay', async () => {
    const live = connect()
    const original = Stream.instances[0]!
    original.send('snapshot', snapshot('one', 'server'))
    live.sessionID.value = 'two'
    await nextTick()
    const current = Stream.instances[1]!
    current.send('snapshot', snapshot('two', 'server'))
    original.send('snapshot', snapshot('one', 'old'))
    original.fail()
    expect(live.state.value.view?.id).toBe('two')
    expect(live.connection.value).toBe('connected')
    expect(stores.app.sessionViews.one).toBeUndefined()
    scope?.stop()
    expect(current.closed).toBe(true)
    expect(stores.app.sessionViews.two).toBeUndefined()
  })

  it('restores a cached session before its replacement stream has returned a snapshot', async () => {
    const live = connect()
    const initial = snapshot('one', 'server')
    initial.history.entries = [{ id: 'question', session_id: 'one', sequence: 1, version: 1, time: '', kind: 'message', message: { role: 'user', content: [{ type: 'text', text: 'Keep my place' }] } }]
    initial.history.next_cursor = 'older-page'
    Stream.instances[0]!.send('snapshot', initial)
    live.state.value.scrollPosition = { following: false, top: 310, anchor: { key: 'question', offset: -20 } }
    const original = live.state.value
    live.sessionID.value = 'two'
    Stream.instances[1]!.send('snapshot', snapshot('two', 'server'))
    live.sessionID.value = 'one'
    expect(live.state.value).toBe(original)
    expect(live.state.value.historyCursor).toBe('older-page')
    expect(live.state.value.scrollPosition?.top).toBe(310)
    expect(live.connection.value).toBe('connecting')
    expect(Stream.instances[2]!.url).toContain('/one/events')
    Stream.instances[2]!.send('snapshot', initial)
    expect(live.connection.value).toBe('connected')
    expect(live.state.value.entries.question).toBe(original.entries.question)
    expect(live.state.value.scrollPosition).toEqual(original.scrollPosition)
    await nextTick()
  })

  it('flushes queued text into the old session before switching and ignores late transport callbacks', async () => {
    const live = connect()
    const first = snapshot('one', 'server')
    first.view = { ...first.view, run_id: 'run', status: 'running' }
    const original = Stream.instances[0]!
    original.send('snapshot', first)
    const delta = (text: string, sequence: number) => ({ epoch: 'server', core: { sequence, session_id: 'one', type: 'message_update', run_id: 'run', attempt_id: 'attempt', model: { type: 'text_delta', content_index: 0, text } } })
    original.send('event', delta('A', 1))
    expect(live.state.value.previews.attempt).toBeUndefined()
    live.sessionID.value = 'two'
    Stream.instances[1]!.send('snapshot', snapshot('two', 'server'))
    original.send('event', delta('late', 2))
    await vi.advanceTimersByTimeAsync(100)
    expect(live.state.value.previews).toEqual({})
    live.sessionID.value = 'one'
    expect(live.state.value.previews.attempt).toMatchObject({ interrupted: true, parts: { 0: { text: 'A' } } })
  })

  it('clears cached sessions on logout even after the chat scope is disposed', () => {
    const live = connect()
    Stream.instances[0]!.send('snapshot', snapshot('one', 'server'))
    live.state.value.scrollPosition = { following: false, top: 100 }
    scope!.stop()
    stores.app.authorized = false
    stores.app.authorized = true
    const reopened = connect()
    expect(reopened.state.value.view).toBeUndefined()
    expect(reopened.state.value.scrollPosition).toBeUndefined()
  })

  it('clears active and cached history immediately when the account identity changes', () => {
    const live = connect()
    Stream.instances[0]!.send('snapshot', snapshot('one', 'server'))
    live.sessionID.value = 'two'
    Stream.instances[1]!.send('snapshot', snapshot('two', 'server'))
    stores.auth.status.username = 'different-owner'
    expect(Stream.instances[1]!.closed).toBe(true)
    expect(live.state.value.view).toBeUndefined()
    live.sessionID.value = 'one'
    expect(live.state.value.view).toBeUndefined()
  })

  it.each(['forgetSession', 'forgetAgent'])('clears a still-mounted active session and stops its transport after %s', async action => {
    const live = connect()
    const original = Stream.instances[0]!
    original.send('snapshot', snapshot('one', 'server'))
    live.state.value.scrollPosition = { following: false, top: 120 }
    for (const handler of actionHandlers) handler({ name: action, args: [action === 'forgetSession' ? 'one' : 'agent'], after: callback => callback() })
    expect(original.closed).toBe(true)
    expect(live.state.value.view).toBeUndefined()
    expect(live.state.value.scrollPosition).toBeUndefined()
    expect(live.connection.value).toBe('disconnected')
    original.send('snapshot', snapshot('one', 'stale'))
    original.fail()
    live.reconnect()
    await vi.advanceTimersByTimeAsync(2000)
    expect(Stream.instances).toHaveLength(1)
    expect(live.state.value.view).toBeUndefined()
    live.sessionID.value = 'two'
    live.sessionID.value = 'one'
    expect(live.state.value.view).toBeUndefined()
  })
})
