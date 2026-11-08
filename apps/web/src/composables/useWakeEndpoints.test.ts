import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useWakeEndpoints } from './useWakeEndpoints'
import { useWakeEndpointStore } from '../stores/wake-endpoints'
import type { WakeEndpoint } from '../lib/wake-endpoints'

const fixture = vi.hoisted(() => ({ auth: undefined as unknown as { authenticated: boolean; status: { username: string } }, mounted: [] as Array<() => void>, activated: [] as Array<() => void>, deactivated: [] as Array<() => void>, list: vi.fn(), update: vi.fn(), remove: vi.fn() }))
vi.mock('vue', async () => ({ ...await vi.importActual<typeof import('vue')>('vue'), onMounted: (callback: () => void) => fixture.mounted.push(callback), onActivated: (callback: () => void) => fixture.activated.push(callback), onDeactivated: (callback: () => void) => fixture.deactivated.push(callback) }))
vi.mock('../stores/auth', () => ({ useAuthStore: () => fixture.auth }))
vi.mock('../lib/wake-endpoints', () => ({ wakeAPI: { list: fixture.list, update: fixture.update, remove: fixture.remove } }))
class Stream {
  static instances: Stream[] = []
  closed = false
  onerror?: () => void
  listeners = new Map<string, (event: { data: string }) => void>()
  constructor(readonly url: string) { Stream.instances.push(this) }
  close() { this.closed = true }
  addEventListener(name: string, callback: (event: { data: string }) => void) { this.listeners.set(name, callback) }
  snapshot(items: WakeEndpoint[]) { this.listeners.get('wake-endpoints')?.({ data: JSON.stringify(items) }) }
}
const endpoint = (agent_id: string, title = agent_id): WakeEndpoint => ({ id: `wake-${agent_id}`, agent_id, session_id: 'root', computer_id: 'computer', source_session_id: 'root', title, enabled: true, revision: 1, created_at: '', updated_at: '' })
let scope: EffectScope
let directory: ReturnType<typeof useWakeEndpointStore>
beforeEach(() => {
  setActivePinia(createPinia())
  fixture.auth = reactive({ authenticated: true, status: { username: 'owner' } })
  fixture.mounted = []; fixture.activated = []; fixture.deactivated = []
  Stream.instances = []
  fixture.list.mockReset().mockResolvedValue([])
  fixture.update.mockReset(); fixture.remove.mockReset()
  vi.stubGlobal('EventSource', Stream)
  vi.useFakeTimers()
  scope = effectScope()
  directory = useWakeEndpointStore()
})
afterEach(() => { scope.stop(); directory.$dispose(); vi.useRealTimers(); vi.unstubAllGlobals() })
function observe() {
  const bot = ref('first')
  const feed = scope.run(() => useWakeEndpoints(bot, computed(() => fixture.auth.authenticated)))!
  return { feed, bot }
}

describe('shared Owner event endpoint observation', () => {
  it('shares one all-Bot stream across page consumers without initial GETs or reconnecting on Bot selection', async () => {
    const first = observe(), second = observe()
    expect(Stream.instances).toHaveLength(1)
    expect(Stream.instances[0]!.url).toBe('/api/v1/wake-endpoints/events?configuration_only=true')
    expect(fixture.list).not.toHaveBeenCalled()
    fixture.mounted.forEach(start => start()); fixture.activated.forEach(start => start())
    expect(Stream.instances).toHaveLength(2)
    expect(Stream.instances.filter(stream => !stream.closed)).toHaveLength(1)
    expect(Stream.instances[1]!.url).toBe('/api/v1/wake-endpoints/events')
    Stream.instances[1]!.snapshot([endpoint('first'), endpoint('second')])
    expect(first.feed.items.value.map(item => item.agent_id)).toEqual(['first'])
    second.bot.value = 'second'; await nextTick()
    expect(second.feed.items.value.map(item => item.agent_id)).toEqual(['second'])
    expect(Stream.instances).toHaveLength(2)
    fixture.deactivated.forEach(stop => stop())
    expect(Stream.instances.at(-1)!.url).toContain('configuration_only=true')
    expect(Stream.instances.filter(stream => !stream.closed)).toHaveLength(1)
    expect(fixture.list).not.toHaveBeenCalled()
    scope.stop()
    expect(Stream.instances.at(-1)!.closed).toBe(false)
  })
  it('ignores a late previous-account fetch or snapshot after logout and clears private directory state', async () => {
    const { feed } = observe()
    let finish!: (items: WakeEndpoint[]) => void
    fixture.list.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const refreshing = feed.refresh()
    const oldStream = Stream.instances[0]!
    fixture.auth.authenticated = false
    expect(oldStream.closed).toBe(true)
    oldStream.snapshot([endpoint('first')]); finish([endpoint('first')]); await refreshing
    expect(directory.items).toEqual([])
    expect(feed.ready.value).toBe(false)
    fixture.auth.authenticated = true
    expect(Stream.instances).toHaveLength(2)
    Stream.instances[1]!.snapshot([endpoint('second')])
    expect(directory.items.map(item => item.agent_id)).toEqual(['second'])
  })
  it('lets a newer streamed snapshot supersede an in-flight refresh', async () => {
    let finish!: (items: WakeEndpoint[]) => void
    fixture.list.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const refreshing = directory.refresh()
    Stream.instances[0]!.snapshot([endpoint('first', 'Fresh')])
    finish([endpoint('first', 'Stale')]); await refreshing
    expect(directory.items[0]!.title).toBe('Fresh')
  })
  it('performs one fallback refresh and cancels reconnect on disposal', async () => {
    Stream.instances[0]!.onerror!()
    expect(fixture.list).toHaveBeenCalledOnce()
    directory.$dispose()
    await vi.advanceTimersByTimeAsync(3000)
    expect(Stream.instances).toHaveLength(1)
  })
  it('keeps a newer streamed revision when an older mutation response arrives', async () => {
    let finish!: (value: WakeEndpoint) => void
    fixture.update.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const original = endpoint('first')
    const updating = directory.update(original, { title: 'Local edit' })
    Stream.instances[0]!.snapshot([{ ...original, title: 'Latest', revision: 3 }])
    finish({ ...original, title: 'Local edit', revision: 2 }); await updating
    expect(directory.items[0]).toMatchObject({ title: 'Latest', revision: 3 })
  })
  it('immediately removes a deleted Computer’s endpoints across Bots while retaining another Computer on the same target', async () => {
    const removed = endpoint('first')
    const alsoRemoved = { ...endpoint('second'), session_id: removed.session_id }
    const retained = { ...endpoint('third'), computer_id: 'other-computer', session_id: removed.session_id }
    const stale = [removed, alsoRemoved, retained]
    Stream.instances[0]!.snapshot(stale)
    let finish!: (items: WakeEndpoint[]) => void
    fixture.list.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const refreshing = directory.refresh()
    directory.forgetComputer('computer')
    expect(directory.items).toEqual([retained])
    finish(stale); await refreshing
    expect(directory.items).toEqual([retained])
    expect(Stream.instances).toHaveLength(1)
  })
  it('filters late SSE snapshots for a permanently deleted Computer until the Owner identity changes', () => {
    const removed = endpoint('first')
    const retained = { ...endpoint('second'), computer_id: 'other-computer' }
    const stream = Stream.instances[0]!
    stream.snapshot([removed, retained])
    directory.forgetComputer(removed.computer_id)
    stream.snapshot([removed, retained])
    expect(directory.items).toEqual([retained])
    expect(Stream.instances).toHaveLength(1)
    expect(fixture.list).not.toHaveBeenCalled()
    fixture.auth.status.username = 'another-owner'
    Stream.instances.at(-1)!.snapshot([removed])
    expect(directory.items).toEqual([removed])
  })
  it('does not resurrect an endpoint from a mutation response received after its Computer was permanently deleted', async () => {
    const removed = endpoint('first')
    const retained = { ...endpoint('second'), computer_id: 'other-computer' }
    Stream.instances[0]!.snapshot([removed, retained])
    let finish!: (item: WakeEndpoint) => void
    fixture.update.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const updating = directory.update(removed, { title: 'Accepted before Computer deletion' })
    directory.forgetComputer(removed.computer_id)
    finish({ ...removed, title: 'Accepted before Computer deletion', revision: 2 }); await updating
    expect(directory.items).toEqual([retained])
    expect(Stream.instances).toHaveLength(1)
    expect(fixture.list).not.toHaveBeenCalled()
  })
})
