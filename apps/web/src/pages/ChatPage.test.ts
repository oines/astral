 
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h, reactive, ref, type PropType } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Agent, Profile, SessionView } from '../lib/types'
import ChatPage from './ChatPage.vue'

const fixture = vi.hoisted(() => ({ agents: [] as Agent[], availableModels: [] as Profile[], views: [] as SessionView[] }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => reactive({ agents: fixture.agents, availableModels: fixture.availableModels, activeAgentID: 'bot', sessions: [], sidebar: false }) }))
vi.mock('../stores/surface', () => ({ useSurfaceStore: () => ({ close: vi.fn(), setSession: vi.fn() }) }))
vi.mock('../composables/useSession', () => ({ useSession: () => ({ state: ref({ view: undefined }), error: ref(''), connection: ref('connected') }) }))
vi.mock('../components/Sidebar.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/Transcript.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/ChatLoading.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/ContextSurface.vue', () => ({ default: { render: () => null } }))
vi.mock('./SchedulesPage.vue', () => ({ default: { render: () => null } }))
vi.mock('../components/Composer.vue', () => ({ default: defineComponent({ props: { view: { type: Object as PropType<SessionView>, required: true } }, setup(props) {
  fixture.views.push(props.view as SessionView)
  return () => h('div', { 'data-composer-model': props.view.profile_id })
} }) }))

beforeEach(() => {
  fixture.agents = [{ id: 'bot', revision: 1, created_at: '', updated_at: '', name: 'Bot', default_model_id: 'disabled', default_reasoning: { effort: 'high' }, allowed_computers: [], model_auto_restore: false, model_priority: [{ id: 'disabled', reasoning: { effort: 'high' } }, { id: 'available', reasoning: { effort: 'low' } }] }]
  fixture.availableModels = [{ id: 'available', available: true }] as Profile[]
  fixture.views = []
  vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() })
})
afterEach(() => vi.unstubAllGlobals())
async function render() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push('/')
  return renderToString(createSSRApp(ChatPage).use(router))
}

describe('starting conversations with an ordered model policy', () => {
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
