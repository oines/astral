import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, createSSRApp, reactive, ssrContextKey, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import SubagentBubble from './SubagentBubble.vue'
import { useSurfaceStore } from '../stores/surface'
import type { SubagentActivity } from '../lib/subagents'

const mocks = vi.hoisted(() => ({
  app: {
    sessionViews: {} as Record<string, { title?: string; status?: string; error?: string }>,
    statuses: {} as Record<string, { title?: string; status?: string; error?: string }>,
    sessions: [] as Array<{ id: string; title: string }>,
    deletedSessions: new Set<string>(),
  },
}))
vi.mock('../stores/app', () => ({ useAppStore: () => mocks.app }))

let i18n: typeof import('../i18n')['i18n']
beforeAll(async () => {
  vi.stubGlobal('localStorage', { getItem: () => 'zh' })
  i18n = (await import('../i18n')).i18n
})
afterAll(() => { vi.unstubAllGlobals() })
beforeEach(() => {
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  mocks.app.sessionViews = {}
  mocks.app.statuses = {}
  mocks.app.sessions = [{ id: 'parent', title: 'Review' }]
  mocks.app.deletedSessions = reactive(new Set<string>())
})
let dispose: (() => void) | undefined
afterEach(() => { dispose?.(); dispose = undefined; setActivePinia(undefined) })

function activity(status = 'started'): SubagentActivity {
  return { id: `event-${status}`, sequence: 1, session_id: 'parent', child_session_id: 'child', name: 'Runtime review', run_id: 'child-run', status, time: '2026-11-05T03:00:00Z' }
}
function render(props: { activity: SubagentActivity }) {
  return renderToString(createSSRApp(SubagentBubble, { ...props, sessionID: 'parent' }).use(i18n))
}

describe('subagent lifecycle bubbles', () => {
  it.each([
    ['started', 'Runtime review 开始工作', 'Runtime review started working'],
    ['completed', 'Runtime review 已完成', 'Runtime review completed'],
    ['failed', 'Runtime review 运行失败', 'Runtime review failed'],
    ['cancelled', 'Runtime review 已停止', 'Runtime review stopped'],
  ])('renders the recorded %s event in both languages', async (status, chinese, english) => {
    const zh = await render({ activity: activity(status) })
    expect(zh).toContain(chinese)
    expect(zh).toContain('subagent-bubble')
    expect(zh).not.toContain('subagent-bubble-copy')
    i18n.global.locale.value = 'en'
    expect(await render({ activity: activity(status) })).toContain(english)
  })

  it('keeps the recorded name and outcome when the child starts another run', async () => {
    mocks.app.sessionViews.child = { title: 'Current title', status: 'running', error: 'A later error' }
    const html = await render({ activity: activity('completed') })
    expect(html).toContain('Runtime review 已完成')
    expect(html).not.toContain('Current title')
    expect(html).not.toContain('A later error')
  })

  it('preserves a deleted child’s historical status while disabling navigation', async () => {
    mocks.app.deletedSessions.add('child')
    const html = await render({ activity: activity('completed') })
    expect(html).toContain('Runtime review 已完成')
    expect(html).toContain('disabled')
    expect(html).not.toContain('已删除')
  })

  it('opens the same contextual surface and cannot navigate after deletion', () => {
    const surface = useSurfaceStore()
    const root = {}
    const renderer = createRenderer<object, object>({
      createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
      insert() {}, remove() {}, setText() {}, setElementText() {},
      parentNode: () => null, nextSibling: () => null, patchProp() {},
    })
    // Exercise the real setup/event handler with no browser DOM dependency.
    const component = { ...SubagentBubble, render: () => null } as unknown as Component
    const opened = vi.fn()
    const app = renderer.createApp(component, { activity: activity('completed'), sessionID: 'parent', onOpen: opened }).use(i18n)
    app.provide(ssrContextKey, {})
    app.mount(root)
    dispose = () => app.unmount()
    const setup = (app._instance as unknown as { setupState: { open: () => void } }).setupState
    setup.open()
    expect(opened).toHaveBeenCalledOnce()
    expect(surface.current).toMatchObject({ kind: 'subagent', id: 'child', title: 'Runtime review', path: [{ id: 'parent', title: 'Review' }, { id: 'child', title: 'Runtime review' }] })
    surface.close()
    mocks.app.deletedSessions.add('child')
    setup.open()
    expect(surface.current).toBeUndefined()
    expect(opened).toHaveBeenCalledOnce()
  })
})
