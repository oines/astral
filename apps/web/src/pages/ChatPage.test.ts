/* eslint-disable vue/one-component-per-file -- Shared controls and Composer are isolated SSR stubs. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h, reactive, ref, type PropType } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Agent, Profile, SessionView } from '../lib/types'
import ChatPage from './ChatPage.vue'

const fixture = vi.hoisted(() => ({ agents: [] as Agent[], availableModels: [] as Profile[], views: [] as SessionView[], current: undefined as SessionView | undefined, restore: undefined as undefined | (() => Promise<void>), presentation: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => reactive({ agents: fixture.agents, availableModels: fixture.availableModels, activeAgentID: 'bot', sessions: [], sidebar: false, presentation: fixture.presentation }) }))
vi.mock('../stores/surface', () => ({ useSurfaceStore: () => ({ close: vi.fn(), setSession: vi.fn() }) }))
vi.mock('../composables/useSession', () => ({ useSession: () => ({ state: ref({ view: fixture.current }), error: ref(''), connection: ref('connected'), loadingHistory: ref(false), older: vi.fn() }) }))
vi.mock('../components/Sidebar.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/Transcript.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/ChatLoading.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/ContextSurface.vue', () => ({ default: { render: () => null } }))
vi.mock('./SchedulesPage.vue', () => ({ default: { render: () => null } }))
vi.mock('@astral/ui', async importOriginal => ({ ...await importOriginal<object>(), Button: defineComponent({ setup(_props, { attrs, slots }) {
  const state = (getCurrentInstance()!.parent! as unknown as { setupState: { restore?: () => Promise<void> } }).setupState
  if (state.restore) fixture.restore = state.restore
  return () => h('button', attrs, slots.default?.())
} }) }))
vi.mock('../components/Composer.vue', () => ({ default: defineComponent({ props: { view: { type: Object as PropType<SessionView>, required: true } }, setup(props) {
  fixture.views.push(props.view as SessionView)
  return () => h('div', { 'data-composer-model': props.view.profile_id })
} }) }))

beforeEach(() => {
  fixture.agents = [{ id: 'bot', revision: 1, created_at: '', updated_at: '', name: 'Bot', default_model_id: 'disabled', default_reasoning: { effort: 'high' }, allowed_computers: [], model_auto_restore: false, model_priority: [{ id: 'disabled', reasoning: { effort: 'high' } }, { id: 'available', reasoning: { effort: 'low' } }] }]
  fixture.availableModels = [{ id: 'available', available: true }] as Profile[]
  fixture.views = []
  fixture.current = undefined
  fixture.restore = undefined
  fixture.presentation.mockReset().mockResolvedValue(undefined)
  vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() })
})
afterEach(() => vi.unstubAllGlobals())
async function render(path = '/') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }, { path: '/sessions/:id', component: { render: () => null } }] })
  await router.push(path)
  return renderToString(createSSRApp(ChatPage).use(router))
}

describe('starting conversations with an ordered model policy', () => {
  it('opens an archived URL as readable history with export and an explicit restore action', async () => {
    fixture.current = { id: 'sealed', agent_id: 'bot', title: 'Old session', archived: true } as SessionView
    const html = await render('/sessions/sealed')
    expect(html).toContain('archiveReadOnly')
    expect(html).toContain('restoreSession')
    expect(html).toContain('/api/v1/sessions/sealed/rollout')
    expect(html).not.toContain('data-composer-model')
    expect(fixture.presentation).not.toHaveBeenCalled()
    expect(fixture.restore).toBeTypeOf('function')
    await fixture.restore!()
    expect(fixture.presentation).toHaveBeenCalledWith('sealed', { archived: false })
    expect(fixture.views).toEqual([])
  })
  it('keeps Composer available when the preferred resource is disabled but an alternative is usable', async () => {
    const html = await render()
    expect(html).toContain('data-composer-model="available"')
    expect(html).not.toContain('welcome-setup')
    expect(fixture.views[0]).toMatchObject({ agent_id: 'bot', model_available: true, profile_id: 'available', reasoning: { effort: 'low' }, selection: { id: 'available', reasoning: { effort: 'low' } } })
    expect(fixture.agents[0]!.default_model_id).toBe('disabled')
  })
  it('uses the legacy default model when no ordered policy was saved', async () => {
    fixture.agents[0]!.model_priority = undefined
    fixture.agents[0]!.default_model_id = 'available'
    await render()
    expect(fixture.views[0]).toMatchObject({ profile_id: 'available', reasoning: { effort: 'high' } })
  })
  it('keeps setup available if none of this Bot’s candidates can be used', async () => {
    fixture.availableModels = [{ id: 'unrelated', available: true }] as Profile[]
    const html = await render()
    expect(html).toContain('welcome-setup')
    expect(fixture.views).toEqual([])
  })
})
