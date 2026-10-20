import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import type { SessionView, Snapshot } from '../lib/types'
import { useSession } from './useSession'

const app = vi.hoisted(() => ({ sessionViews: {} as Record<string, SessionView> }))
vi.mock('../stores/app', () => ({ useAppStore: () => app }))
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
    view: { id, agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Work', archived: false, pinned: false, source: 'web', rollout_id: 'rollout', head_id: 'head', status: 'idle', options: {}, approvals: [], pending: [], unresolved: [] },
    history: { entries: [], head_id: 'head' }, trace: { entries: [], head_id: 'head' },
  }
}
let scope: EffectScope | undefined
afterEach(() => {
  scope?.stop()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Stream.instances = []
  app.sessionViews = {}
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
    expect(app.sessionViews.one).toBeUndefined()
    scope?.stop()
    expect(current.closed).toBe(true)
    expect(app.sessionViews.two).toBeUndefined()
  })
})
