import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope, type Ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useAppStore } from './app'
import type { SessionView, Snapshot } from '../lib/types'
import { useSession } from '../composables/useSession'
import { sidebarGroups, visibleSidebarSessions } from '../lib/sidebar-groups'

type Query = { data: Ref<unknown>; refetch: () => Promise<void> }
const mocks = vi.hoisted(() => ({ queries: {} as Record<string, Query>, writes: vi.fn(), auth: undefined as unknown as { authenticated: boolean; status: { desktop: boolean; username: string }; refresh: () => void } }))
vi.mock('@astral/sdk/colada', () => ({
  bootstrapQuery: () => ({ name: 'bootstrap' }), bootstrapQueryKey: () => ['bootstrap'],
  listSessionsQuery: () => ({ name: 'sessions' }), listSessionsQueryKey: () => ['sessions'],
  listComputersQuery: () => ({ name: 'computers' }),
}))
vi.mock('@pinia/colada', () => ({
  useQuery: ({ name }: { name: string }) => mocks.queries[name],
  useQueryCache: () => ({
    invalidateQueries: async () => undefined,
    setQueryData: (key: string[], value: unknown) => { mocks.writes(key[0]); mocks.queries[key[0]!]!.data.value = value },
  }),
}))
vi.mock('./auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('../lib/schedules', async importOriginal => ({ ...await importOriginal<typeof import('../lib/schedules')>(), scheduleAPI: { list: async () => [] } }))
vi.mock('../i18n', async () => { const { ref } = await import('vue'); return { i18n: { global: { locale: ref('zh') } } } })
vi.mock('@vueuse/core', async () => { const { ref } = await import('vue'); return { usePreferredDark: () => ref(false), useIntervalFn: vi.fn() } })

function session(id: string, parent?: string, extra: Partial<SessionView> = {}): SessionView {
  return { id, subagent_parent_id: parent, agent_id: 'bot', revision: 1, title: id, created_at: '', updated_at: '', archived: false, pinned: false, source: 'web', status: 'idle', rollout_id: id, head_id: '', event_sequence: 0, can_continue: false, model_available: true, selection: { id: 'model' }, approvals: [], pending: [], unresolved: [], ...extra }
}
const root = session('root')
const child = session('child', 'root')
const grandchild = session('grandchild', 'child')
const fork = session('fork', undefined, { parent_session_id: 'root' })
class Stream extends EventTarget {
  static instances: Stream[] = []
  closed = false
  onerror?: () => void
  constructor(public url: string) { super(); Stream.instances.push(this) }
  close() { this.closed = true }
  send(name: string, value: unknown) { this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(value) })) }
  fail() { this.onerror?.() }
}
function snapshot(view: SessionView, epoch = 'server'): Snapshot {
  return { epoch, view, history: { entries: [], head_id: '' }, trace: { entries: [], head_id: '' } }
}
let app: ReturnType<typeof useAppStore> | undefined
let sessionScope: EffectScope | undefined
beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('window', { matchMedia: () => ({ matches: true }) })
  vi.stubGlobal('document', { documentElement: { dataset: {} }, visibilityState: 'visible', addEventListener: vi.fn(), removeEventListener: vi.fn() })
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: vi.fn() })
  Stream.instances = []
  vi.stubGlobal('EventSource', Stream)
  mocks.auth = reactive({ authenticated: true, status: { desktop: true, username: 'owner' }, refresh: vi.fn() })
  mocks.queries = {
    bootstrap: { data: ref({ agents: [], sessions: [root, child, grandchild, fork], models: [], preferences: {}, timezone: { name: 'UTC' } }), refetch: async () => undefined },
    sessions: { data: ref([root, child, grandchild, fork]), refetch: async () => undefined },
    computers: { data: ref([]), refetch: async () => undefined },
  }
  mocks.writes.mockReset()
})
afterEach(() => { sessionScope?.stop(); sessionScope = undefined; app?.$dispose(); app = undefined; vi.useRealTimers(); vi.unstubAllGlobals() })

describe('sidebar ownership across the real session stream', () => {
  function sidebarIDs(search = '', selected = 'child') {
    const visible = visibleSidebarSessions(app!.sessions, app!.schedules, row => app!.statuses[row.id]?.status === 'running', selected).filter(row => row.title.includes(search))
    return sidebarGroups(visible, app!.schedules, row => app!.statuses[row.id]?.status === 'running').flatMap(group => group.items.map(row => row.id))
  }

  it('merges a refreshed directory summary without replacing complete active execution details', async () => {
    app = useAppStore()
    const detailed = { ...root, status: 'running', run_id: 'current-run', event_sequence: 42,
      pending: [{ id: 'queued-owner', kind: 'follow_up', metadata: {} }],
      approvals: [{ ID: 'approval', Invocation: {}, Request: { prompt: 'Keep this approval' } }],
      unresolved: [{ invocation: { execution_id: 'unconfirmed-tool' } }],
      context_usage: { entry_id: 'generation', input_tokens: 1234, context_window: 128000 } }
    // Simulate the full SSE payload independently of the directory contract.
    app.sessionViews.root = detailed as SessionView
    const detailFields = new Set(['pending', 'approvals', 'unresolved', 'context_usage', 'can_continue', 'event_sequence', 'rollout_id', 'head_id'])
    const summary = Object.fromEntries(Object.entries(root).filter(([key]) => !detailFields.has(key)))
    mocks.queries.sessions!.data.value = [{ ...summary, revision: 2, title: 'Fresh directory title', status: 'idle' }, child, grandchild, fork]
    await nextTick()
    expect(app.sessions.find(row => row.id === root.id)).toMatchObject({
      title: 'Fresh directory title', status: 'running', run_id: 'current-run', event_sequence: 42,
      pending: detailed.pending, approvals: detailed.approvals, unresolved: detailed.unresolved, context_usage: detailed.context_usage,
    })
    expect(app.sessionViews.root).toEqual(detailed)
  })

  it('keeps shared and independent subagents out of bootstrap, refresh, search and archive navigation', async () => {
    const shared = { ...child, parent_session_id: 'root', archived: true, pinned: true, status: 'running' }
    mocks.queries.sessions!.data.value = undefined
    mocks.queries.bootstrap!.data.value = { agents: [], sessions: [root, shared, grandchild, fork], models: [], preferences: {}, timezone: { name: 'UTC' } }
    app = useAppStore()
    expect(sidebarIDs()).toEqual(['root', 'fork'])
    expect(sidebarIDs('child', 'grandchild')).toEqual([])
    expect(app.sessions.map(row => row.id)).toEqual(['root', 'child', 'grandchild', 'fork'])
    mocks.queries.sessions!.data.value = [root, { ...child, source: 'subagent' }, { ...grandchild, archived: true }, fork]
    await nextTick()
    expect(sidebarIDs()).toEqual(['root', 'fork'])
    expect(app.sessions.find(row => row.id === 'grandchild')?.subagent_parent_id).toBe('child')
  })

  it('does not add a newly opened child panel to navigation before or after the Owner list catches up', async () => {
    mocks.queries.sessions!.data.value = [root, fork]
    mocks.queries.bootstrap!.data.value = { agents: [], sessions: [root, fork], models: [], preferences: {}, timezone: { name: 'UTC' } }
    app = useAppStore()
    sessionScope = effectScope()
    const panel = sessionScope.run(() => useSession(ref('child')))!
    Stream.instances.find(stream => stream.url.endsWith('/child/events'))!.send('snapshot', snapshot(child))
    expect(panel.state.value.view?.subagent_parent_id).toBe('root')
    expect(app.sessionViews.child?.id).toBe('child')
    expect(app.sessions.map(row => row.id)).toEqual(['root', 'fork'])
    expect(sidebarIDs()).toEqual(['root', 'fork'])
    mocks.queries.sessions!.data.value = [root, fork, child]
    await nextTick()
    expect(app.sessions.find(row => row.id === 'child')?.id).toBe('child')
    expect(sidebarIDs()).toEqual(['root', 'fork'])
    expect(app.deletedSessions.has('child')).toBe(false)
  })

  it('retains a child panel and older descendants through SSE and reconnect without leaking them into the sidebar', async () => {
    vi.useFakeTimers()
    app = useAppStore()
    app.drafts.root = { text: 'Keep the root draft', files: [] }
    sessionScope = effectScope()
    const main = sessionScope.run(() => useSession(ref('root')))!
    const mainStream = Stream.instances.find(stream => stream.url.endsWith('/root/events'))!
    mainStream.send('snapshot', snapshot(root))
    const mainState = main.state.value
    const childID = ref('child')
    const panel = sessionScope.run(() => useSession(childID))!
    const childStream = Stream.instances.find(stream => stream.url.endsWith('/child/events'))!
    childStream.send('snapshot', snapshot(child))
    panel.state.value.scrollPosition = { following: false, top: 240 }
    childStream.send('state', { epoch: 'server', view: { ...child, revision: 2, event_sequence: 1, title: 'Updated child', status: 'running', subagent_parent_id: undefined } })
    expect(app.sessions.find(row => row.id === 'child')).toMatchObject({ title: 'Updated child', subagent_parent_id: 'root', status: 'running' })
    expect(sidebarIDs('child')).toEqual([])
    childStream.fail()
    await vi.advanceTimersByTimeAsync(1000)
    const reconnected = Stream.instances.filter(stream => stream.url.endsWith('/child/events')).at(-1)!
    reconnected.send('snapshot', snapshot({ ...child, revision: 3, subagent_parent_id: undefined }, 'restarted'))
    expect(panel.state.value.scrollPosition?.top).toBe(240)
    expect(sidebarIDs()).toEqual(['root', 'fork'])
    childID.value = 'grandchild'
    Stream.instances.find(stream => stream.url.endsWith('/grandchild/events'))!.send('snapshot', snapshot(grandchild))
    expect(panel.state.value.view?.id).toBe('grandchild')
    expect(app.sessionViews.grandchild?.subagent_parent_id).toBe('child')
    expect(sidebarIDs('grandchild', 'grandchild')).toEqual([])
    childID.value = 'child'
    expect(panel.state.value.scrollPosition?.top).toBe(240)
    expect(main.state.value).toBe(mainState)
    expect(mainStream.closed).toBe(false)
    expect(app.drafts.root?.text).toBe('Keep the root draft')
    expect(app.sessions.find(row => row.id === 'fork')?.parent_session_id).toBe('root')
  })
})

describe('deleted session reconciliation in the app store', () => {
  it('forgets an explicit root deletion with all descendants while keeping an ordinary Fork', async () => {
    app = useAppStore()
    app.sessionViews.grandchild = grandchild
    app.drafts.grandchild = { text: 'old draft', files: [] }
    app.lastChatPath = '/sessions/grandchild'
    app.forgetSession('root')
    await nextTick()
    expect([...app.deletedSessions]).toEqual(['root', 'child', 'grandchild'])
    expect(app.sessions.map(value => value.id)).toEqual(['fork'])
    expect(app.sessionViews.grandchild).toBeUndefined()
    expect(app.drafts.grandchild).toBeUndefined()
    expect(app.lastChatPath).toBe('/')
  })

  it('recognizes a tool-driven removal on the next Owner catalog without recursive query writes', async () => {
    app = useAppStore()
    app.sessionViews.grandchild = grandchild
    mocks.queries.sessions!.data.value = [root, fork]
    await nextTick()
    expect([...app.deletedSessions]).toEqual(['child', 'grandchild'])
    expect(app.sessionViews.grandchild).toBeUndefined()
    expect(app.sessions.map(value => value.id)).toEqual(['root', 'fork'])
    expect(mocks.writes.mock.calls).toEqual([['bootstrap']])
  })

  it('uses the successful bootstrap catalog until the first dedicated Owner list arrives', async () => {
    mocks.queries.sessions!.data.value = undefined
    app = useAppStore()
    mocks.queries.sessions!.data.value = [root, fork]
    await nextTick()
    expect([...app.deletedSessions]).toEqual(['child', 'grandchild'])
    expect(app.sessions.map(value => value.id)).toEqual(['root', 'fork'])
  })

  it('keeps a queued child that has not appeared in a successful catalog and remembers its relationship for root cleanup', async () => {
    mocks.queries.sessions!.data.value = [root]
    mocks.queries.bootstrap!.data.value = { agents: [], sessions: [root], models: [], preferences: {}, timezone: { name: 'UTC' } }
    app = useAppStore()
    app.sessionViews.child = child
    delete app.sessionViews.child
    app.sessionViews.grandchild = grandchild
    mocks.queries.sessions!.data.value = [root]
    await nextTick()
    expect([...app.deletedSessions]).toEqual([])
    app.forgetSession('root')
    expect([...app.deletedSessions]).toEqual(['root', 'child', 'grandchild'])
    expect(app.sessionViews.grandchild).toBeUndefined()
  })

  it('cannot resurrect a confirmed deletion from a stale in-flight catalog', async () => {
    app = useAppStore()
    app.forgetSession('child')
    await nextTick()
    mocks.writes.mockClear()
    mocks.queries.sessions!.data.value = [root, child, grandchild, fork]
    await nextTick()
    expect(app.sessions.map(value => value.id)).toEqual(['root', 'fork'])
    expect(Object.keys(app.statuses)).toEqual(['root', 'fork'])
    expect(mocks.writes).not.toHaveBeenCalled()
  })
})
