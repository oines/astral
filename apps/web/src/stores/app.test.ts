import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref, type Ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useAppStore } from './app'
import type { SessionView } from '../lib/types'

type Query = { data: Ref<unknown>; refetch: () => Promise<void> }
const mocks = vi.hoisted(() => ({ queries: {} as Record<string, Query>, writes: vi.fn(), auth: undefined as unknown as { authenticated: boolean; status: { desktop: boolean; username: string } } }))
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
vi.mock('../lib/schedules', () => ({ scheduleAPI: { list: async () => [] } }))
vi.mock('../i18n', async () => { const { ref } = await import('vue'); return { i18n: { global: { locale: ref('zh') } } } })
vi.mock('@vueuse/core', async () => { const { ref } = await import('vue'); return { usePreferredDark: () => ref(false), useIntervalFn: vi.fn() } })

function session(id: string, parent?: string, extra: Partial<SessionView> = {}): SessionView {
  return { id, subagent_parent_id: parent, agent_id: 'bot', revision: 1, title: id, created_at: '', updated_at: '', archived: false, pinned: false, source: 'web', status: 'idle', rollout_id: id, head_id: '', event_sequence: 0, can_continue: false, model_available: true, selection: { id: 'model' }, approvals: [], pending: [], unresolved: [], ...extra }
}
const root = session('root')
const child = session('child', 'root')
const grandchild = session('grandchild', 'child')
const fork = session('fork', undefined, { parent_session_id: 'root' })
let app: ReturnType<typeof useAppStore> | undefined
beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('window', { matchMedia: () => ({ matches: true }) })
  vi.stubGlobal('document', { documentElement: { dataset: {} } })
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: vi.fn() })
  vi.stubGlobal('EventSource', class { addEventListener() {} close() {} })
  mocks.auth = reactive({ authenticated: true, status: { desktop: true, username: 'owner' } })
  mocks.queries = {
    bootstrap: { data: ref({ agents: [], sessions: [root, child, grandchild, fork], models: [], preferences: {}, timezone: { name: 'UTC' } }), refetch: async () => undefined },
    sessions: { data: ref([root, child, grandchild, fork]), refetch: async () => undefined },
    computers: { data: ref([]), refetch: async () => undefined },
  }
  mocks.writes.mockReset()
})
afterEach(() => { app?.$dispose(); app = undefined; vi.unstubAllGlobals() })

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
