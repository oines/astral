import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { useWakeEndpoints } from './useWakeEndpoints'
import type { WakeEndpoint } from '../lib/wake-endpoints'

const fixture = vi.hoisted(() => ({ mounted: [] as Array<() => void>, activated: [] as Array<() => void>, deactivated: [] as Array<() => void>, list: vi.fn(), update: vi.fn(), remove: vi.fn() }))
vi.mock('vue', async () => {
  const actual = await vi.importActual<typeof import('vue')>('vue')
  return { ...actual, onMounted: (callback: () => void) => fixture.mounted.push(callback), onActivated: (callback: () => void) => fixture.activated.push(callback), onDeactivated: (callback: () => void) => fixture.deactivated.push(callback) }
})
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
beforeEach(() => {
  fixture.mounted = []; fixture.activated = []; fixture.deactivated = []
  Stream.instances = []
  fixture.list.mockReset().mockResolvedValue([])
  fixture.update.mockReset()
  fixture.remove.mockReset()
  vi.stubGlobal('EventSource', Stream)
  vi.useFakeTimers()
  scope = effectScope()
})
afterEach(() => { scope.stop(); vi.useRealTimers(); vi.unstubAllGlobals() })
function observe() {
  const bot = ref('first')
  const authorized = ref(true)
  const feed = scope.run(() => useWakeEndpoints(bot, authorized))!
  fixture.mounted.forEach(start => start())
  return { feed, bot, authorized }
}

describe('page-scoped event endpoint observation', () => {
  it('releases the old Bot stream and ignores its late snapshot and fetch response', async () => {
    let resolveFirst!: (items: WakeEndpoint[]) => void
    fixture.list.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
    const { feed, bot } = observe()
    const pending = feed.refresh()
    const previous = Stream.instances[0]!
    bot.value = 'second'
    await nextTick()
    const current = Stream.instances[1]!
    expect(previous.closed).toBe(true)
    expect(current.url).toContain('agent_id=second')
    current.snapshot([endpoint('second')])
    previous.snapshot([endpoint('first', 'Stale stream')])
    resolveFirst([endpoint('first', 'Stale fetch')])
    await pending
    await nextTick()
    expect(feed.items.value.map(item => item.agent_id)).toEqual(['second'])
  })

  it('keeps a live snapshot newer than a previously started fetch', async () => {
    let finish!: (items: WakeEndpoint[]) => void
    fixture.list.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const { feed } = observe()
    const pending = feed.refresh()
    Stream.instances[0]!.snapshot([endpoint('first', 'Latest change')])
    finish([endpoint('first', 'Earlier request')])
    await pending
    await nextTick()
    expect(feed.items.value[0]!.title).toBe('Latest change')
  })

  it('disconnects during deactivation and logout, and resumes with a fresh snapshot', async () => {
    const { feed, authorized } = observe()
    const first = Stream.instances[0]!
    fixture.deactivated.forEach(stop => stop())
    expect(first.closed).toBe(true)
    fixture.activated.forEach(start => start())
    const resumed = Stream.instances[1]!
    expect(resumed.closed).toBe(false)
    resumed.snapshot([endpoint('first')])
    authorized.value = false
    await nextTick()
    expect(resumed.closed).toBe(true)
    expect(feed.items.value).toEqual([])
    expect(Stream.instances).toHaveLength(2)
  })

  it('does not restart a failed connection after the page has been disposed', async () => {
    observe()
    Stream.instances[0]!.onerror?.()
    scope.stop()
    await vi.advanceTimersByTimeAsync(4000)
    expect(Stream.instances).toHaveLength(1)
  })

  it('does not overwrite a newer live edit with an older mutation response', async () => {
    let finish!: (value: WakeEndpoint) => void
    fixture.update.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const { feed } = observe()
    const current = endpoint('first')
    Stream.instances[0]!.snapshot([current])
    const pending = feed.update(current, { enabled: false })
    Stream.instances[0]!.snapshot([{ ...current, revision: 3, title: 'Newer Agent edit', enabled: false }])
    finish({ ...current, revision: 2, enabled: false })
    await pending
    expect(feed.items.value[0]).toMatchObject({ revision: 3, title: 'Newer Agent edit' })
  })
})
