/* eslint-disable vue/one-component-per-file -- Shared menu primitives are isolated SSR test stubs. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h, reactive } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import Sidebar from './Sidebar.vue'
import type { Session } from '../lib/types'

const fixture = vi.hoisted(() => ({
  app: undefined as unknown as { agents: Array<{ id: string; name: string }>; activeAgentID: string; sessions: Session[]; schedules: []; wakeEndpoints: []; statuses: Record<string, { status?: string }>; sidebar: boolean; data: { version: string } },
  choose: undefined as undefined | ((value: string) => void),
  selected: '',
  menu: undefined as undefined | { agentMenuOpen: boolean },
  manage: undefined as undefined | (() => void),
}))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('../composables/useSessionTitles', () => ({ useSessionTitles: () => ({ titles: {}, typing: {} }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/ui', () => ({
  Menu: defineComponent({ setup: (_props, { slots }) => {
    fixture.menu = (getCurrentInstance()!.parent! as unknown as { setupState: { agentMenuOpen: boolean } }).setupState
    return () => h('div', [slots.trigger?.(), slots.default?.()])
  } }),
  MenuRadioGroup: defineComponent({
    props: { modelValue: { type: String, default: '' } }, emits: ['update:modelValue'],
    setup(props, { emit, slots }) {
      fixture.choose = value => emit('update:modelValue', value)
      fixture.selected = props.modelValue
      return () => h('div', slots.default?.())
    },
  }),
  MenuRadioItem: defineComponent({ setup: (_props, { slots }) => () => h('div', slots.default?.()) }),
  MenuItem: defineComponent({ setup: (_props, { slots }) => () => {
    const children = slots.default?.()
    fixture.manage = children?.[0]?.props?.onClick
    return children
  } }),
  MenuItemIndicator: defineComponent({ setup: (_props, { slots }) => () => h('span', slots.default?.()) }),
  Modal: defineComponent({ setup: () => () => null }),
  Button: defineComponent({ setup: (_props, { slots }) => () => h('button', slots.default?.()) }),
}))

beforeEach(() => {
  fixture.app = reactive({ agents: [{ id: 'bot-a', name: 'Personal' }, { id: 'bot-b', name: 'Team' }], activeAgentID: 'bot-a', sessions: [], schedules: [], wakeEndpoints: [], statuses: {}, sidebar: true, data: { version: 'test' } })
  fixture.choose = undefined
  fixture.menu = undefined; fixture.manage = undefined
})

afterEach(() => vi.unstubAllGlobals())

describe('the sidebar Bot menu', () => {
  it('marks an idle root as active while its subagent works without changing the root’s actual state or listing children', async () => {
    const root: Session = { id: 'root', agent_id: 'bot-a', title: 'Root work', source: 'web', archived: false, pinned: false, revision: 1, created_at: '', updated_at: '' }
    fixture.app.sessions = [root, { ...root, id: 'child', title: 'Child work', subagent_parent_id: root.id }]
    fixture.app.statuses = { root: { status: 'idle' }, child: { status: 'running' } }
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
    await router.push('/sessions/root')
    const html = await renderToString(createSSRApp(Sidebar).use(router))
    expect(html).toMatch(/sidebar-section[^>]*>\s*now\s*<\/h2>/)
    expect(html).toContain('Root work')
    expect(html).toContain('running-dot')
    expect(html).toContain('stopAndArchive')
    expect(html).not.toContain('Child work')
    expect(fixture.app.statuses.root?.status).toBe('idle')
  })
  it('keeps Bot switching and opens the selected Bot settings from the bottom action', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
    await router.push('/sessions/from-another-bot')
    function render() { return renderToString(createSSRApp(Sidebar).use(router)) }
    function manageHref(html: string) {
      const link = [...html.matchAll(/<a\b[^>]*href="([^"]+)"[^>]*>(.*?)<\/a>/gs)].find(match => match[2]?.includes('manageBot'))
      expect(link).toBeDefined()
      return link![1]!
    }

    let html = await render()
    expect(fixture.selected).toBe('bot-a')
    expect(manageHref(html)).toBe('/settings/agents?agent=bot-a')
    expect(html.indexOf('sidebar-agent-separator')).toBeLessThan(html.indexOf('manageBot'))

    fixture.choose!('bot-b')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/'))
    html = await render()
    expect(fixture.selected).toBe('bot-b')
    expect(manageHref(html)).toBe('/settings/agents?agent=bot-b')
    await router.push(manageHref(html))
    expect(router.currentRoute.value.path).toBe('/settings/agents')
    expect(router.currentRoute.value.query).toEqual({ agent: 'bot-b' })
    expect(fixture.app.activeAgentID).toBe('bot-b')
  })
  it('closes the Bot menu through its actual link callback and hides the mobile sidebar', async () => {
    vi.stubGlobal('window', { innerWidth: 390 })
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
    await router.push('/')
    await renderToString(createSSRApp(Sidebar).use(router))
    fixture.menu!.agentMenuOpen = true
    expect(fixture.manage).toBeTypeOf('function')
    fixture.manage!()
    expect(fixture.menu!.agentMenuOpen).toBe(false)
    expect(fixture.app.sidebar).toBe(false)
    expect(fixture.app.activeAgentID).toBe('bot-a')
  })

})
