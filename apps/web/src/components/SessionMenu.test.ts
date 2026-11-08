/* eslint-disable vue/one-component-per-file -- Shared menu primitives are isolated test stubs. */
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Session } from '../lib/types'
import type { SessionAutomation } from '../lib/sidebar-groups'
import SessionMenu from './SessionMenu.vue'

const fixture = vi.hoisted(() => ({ actions: undefined as undefined | { navigateResource: (event: MouseEvent) => void; closeAutoFocus: (event: Event) => void } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: { count: number }) => values ? `${key} ${values.count}` : key }) }))
vi.mock('@astral/ui', () => ({
  Menu: defineComponent({ setup(_props, { slots }) { fixture.actions = (getCurrentInstance()!.parent! as unknown as { setupState: NonNullable<typeof fixture.actions> }).setupState; return () => h('div', slots.default?.()) } }),
  MenuItem: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  MenuSeparator: defineComponent({ setup(_props, { attrs }) { return () => h('hr', attrs) } }),
  Button: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()) } }),
}))
const session: Session = { id: 'session', agent_id: 'bot', title: 'Work', archived: false, pinned: false, source: 'web', revision: 1, created_at: '', updated_at: '' }
describe('bound IM conversation actions', () => {
  it('keeps focus in the destination editor after resource navigation and leaves ordinary menu restoration unchanged', async () => {
    await renderToString(createSSRApp(SessionMenu, { session, busy: false }))
    const close = { preventDefault: vi.fn() } as unknown as Event
    fixture.actions!.closeAutoFocus(close)
    expect(close.preventDefault).not.toHaveBeenCalled()
    fixture.actions!.navigateResource({ metaKey: false, ctrlKey: false, shiftKey: false, altKey: false } as MouseEvent)
    fixture.actions!.closeAutoFocus(close)
    expect(close.preventDefault).toHaveBeenCalledOnce()
    fixture.actions!.closeAutoFocus(close)
    expect(close.preventDefault).toHaveBeenCalledOnce()
  })
  it('places timed-task and endpoint links above normal actions and links remaining items to this Bot’s task list', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
    await router.push('/sessions/session')
    const item = (kind: SessionAutomation['kind'], id: string) => ({ kind, id, title: `${kind} ${id}` })
    const html = await renderToString(createSSRApp(SessionMenu, { session, busy: false, automations: { timed: Array.from({ length: 7 }, (_, index) => item('schedule', `daily-${index}`)), events: [item('event', 'build')] } }).use(router))
    expect(html).toContain('href="/schedules?agent=bot&amp;schedule=daily-0"')
    expect(html).toContain('href="/schedules?agent=bot&amp;endpoint=build"')
    expect(html).toContain('sessionAutomationsMore 2')
    expect(html).toContain('href="/schedules?agent=bot"')
    expect(html.indexOf('schedule daily-0')).toBeLessThan(html.indexOf('session-automation-separator'))
    expect(html.indexOf('session-automation-separator')).toBeLessThan(html.indexOf('rename'))
    expect(html).not.toContain('schedule daily-5')
  })
  it('hides rename and archive while preserving ordinary conversation actions', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, binding_id: 'binding' }, busy: false }))
    expect(html).not.toContain('rename')
    expect(html).not.toContain('archive')
    expect(html).toContain('pin')
    expect(html).toContain('export')
    expect(html).toContain('makeSessionPrivate')
  })
  it('offers rename and archive after an IM conversation is unbound', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, source: 'im' }, busy: false }))
    expect(html).toContain('rename')
    expect(html).toContain('archive')
  })
  it('allows the owner to turn private mode off on an IM-bound session', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, private: true, binding_id: 'binding' }, busy: false }))
    expect(html).toContain('makeSessionNonPrivate')
    expect(html).not.toContain('makeSessionPrivate')
    expect(html).not.toContain('archive')
  })
})
