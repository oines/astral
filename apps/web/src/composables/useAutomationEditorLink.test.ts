import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import { useAutomationEditorLink } from './useAutomationEditorLink'
import type { AutomationTarget } from '../lib/automation-links'

const fixture = vi.hoisted(() => ({
  app: undefined as unknown as { activeAgentID: string; agents: Array<{ id: string }>; data: object },
  route: undefined as unknown as { path: string; query: Record<string, unknown> },
  replace: vi.fn(async (_location: { path: string; query: Record<string, unknown> }) => {}),
}))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('vue-router', () => ({ useRoute: () => fixture.route, useRouter: () => ({ replace: fixture.replace }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
let scope: EffectScope
beforeEach(() => {
  fixture.app = reactive({ activeAgentID: 'first', agents: [{ id: 'first' }, { id: 'second' }], data: {} })
  fixture.route = reactive({ path: '/schedules', query: {} })
  fixture.replace.mockImplementation(async location => { fixture.route.query = location.query })
  fixture.replace.mockClear()
  scope = effectScope()
})
afterEach(() => scope.stop())
const key = (kind: AutomationTarget['kind']) => kind === 'schedule' ? 'schedule' : 'endpoint'
describe.each(['schedule', 'event'] as const)('live %s editor navigation', kind => {
  it('waits for the initial snapshot, selects the requested Bot, and does not replay navigation on SSE refresh or close', async () => {
    const items = ref<Array<{ id: string; agent_id: string; title: string }>>([])
    const ready = ref(false)
    let draft = ''
    const open = vi.fn(item => { draft = item.title })
    fixture.route.query = { agent: 'second', [key(kind)]: 'target' }
    const linked = scope.run(() => useAutomationEditorLink(kind, { items: () => items.value, ready: () => ready.value, busy: () => false, open }))!
    expect(fixture.app.activeAgentID).toBe('second')
    expect(linked.error.value).toBe('')
    expect(open).not.toHaveBeenCalled()
    expect(draft).toBe('')
    items.value = [{ id: 'target', agent_id: 'second', title: 'Initial' }]; ready.value = true; await nextTick()
    expect(open).toHaveBeenCalledOnce()
    draft = 'Keep my draft'
    items.value = [{ id: 'target', agent_id: 'second', title: 'SSE update' }]; await nextTick()
    expect(open).toHaveBeenCalledOnce()
    expect(draft).toBe('Keep my draft')
    linked.clear('target'); await nextTick()
    items.value = [{ id: 'target', agent_id: 'second', title: 'Another update' }]; await nextTick()
    expect(open).toHaveBeenCalledOnce()
    fixture.route.query = { agent: 'second', [key(kind)]: 'target' }; await nextTick()
    expect(open).toHaveBeenCalledTimes(2)
  })
  it('does not open a substitute for a deleted ID or another Bot’s resource', async () => {
    const open = vi.fn()
    const items = ref([{ id: 'other', agent_id: 'first' }])
    fixture.route.query = { agent: 'first', [key(kind)]: 'deleted' }
    const linked = scope.run(() => useAutomationEditorLink(kind, { items: () => items.value, ready: () => true, busy: () => false, open }))!
    expect(linked.error.value).toBe('automationNotFound')
    fixture.route.query = { agent: 'second', [key(kind)]: 'other' }; await nextTick()
    expect(linked.error.value).toBe('automationNotFound')
    expect(open).not.toHaveBeenCalled()
  })
  it('does not let an older editor finishing its save clear a newer requested target', async () => {
    const busy = ref(true), open = vi.fn()
    fixture.route.query = { agent: 'first', [key(kind)]: 'new-target' }
    const linked = scope.run(() => useAutomationEditorLink(kind, { items: () => [{ id: 'new-target', agent_id: 'first' }], ready: () => true, busy: () => busy.value, open }))!
    linked.clear('old-target')
    expect(fixture.replace).not.toHaveBeenCalled()
    busy.value = false; await nextTick()
    expect(open).toHaveBeenCalledWith({ id: 'new-target', agent_id: 'first' })
  })
})
