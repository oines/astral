/* eslint-disable vue/one-component-per-file -- Shared primitives use small isolated renderer stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import SubagentSurface from './SubagentSurface.vue'
import { useSurfaceStore } from '../stores/surface'
import { emptyState } from '../lib/session-state'
import type { SessionState } from '../lib/session-state'
import type { Ref } from 'vue'

const mocks = vi.hoisted(() => ({
  session: undefined as unknown as { state: Ref<SessionState>; error: Ref<string>; older: () => Promise<boolean>; reconnect: () => void },
  app: { sessionViews: {}, sessions: [] },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => mocks.app }))
vi.mock('../composables/useSession', () => ({ useSession: () => mocks.session }))
vi.mock('@astral/ui', async () => {
  const { computed, defineComponent, h } = await import('vue')
  return {
    useDelayedPending: (value: () => boolean) => computed(value),
    Button: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()) } }),
  }
})
vi.mock('./Transcript.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ props: { sessionID: { type: String, required: true }, state: { type: Object, required: true }, readonly: Boolean }, setup(props) { return () => h('section', { 'data-session': props.sessionID, 'data-readonly': String(props.readonly) }, 'Child transcript') } }) }
})

beforeEach(() => {
  mocks.session = { state: ref(emptyState()), error: ref(''), older: async () => false, reconnect: vi.fn() }
})
async function render() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const surface = useSurfaceStore()
  surface.openSubagent({ sessionID: 'child', parentSessionID: 'root', title: 'Backend', status: 'running' }, { id: 'root', title: 'Review' })
  return renderToString(createSSRApp(SubagentSurface, { sessionID: 'child' }).use(pinia))
}

describe('the subagent transcript surface', () => {
  it('renders the read-only transcript without a status or breadcrumb row', async () => {
    mocks.session.state.value.view = { id: 'child', title: 'Backend', status: 'running' } as SessionState['view']
    const html = await render()
    expect(html).not.toContain('<nav')
    expect(html).not.toContain('Review')
    expect(html).not.toContain('Backend')
    expect(html).toContain('data-session="child"')
    expect(html).toContain('data-readonly="true"')
    expect(html).not.toContain('subagent-surface-status')
    expect(html).not.toContain('running')
    expect(html).not.toContain('textarea')
  })

  it('keeps a loaded transcript visible alongside a reconnect error and a read-only retry', async () => {
    mocks.session.state.value.view = { id: 'child', status: 'idle' } as SessionState['view']
    mocks.session.error.value = 'The connection was interrupted.'
    const html = await render()
    expect(html).toContain('The connection was interrupted.')
    expect(html).toContain('Child transcript')
    expect(html).toContain('retry')
  })

  it('shows the model failure without offering a button that starts the child', async () => {
    mocks.session.state.value.view = { id: 'child', status: 'needs_attention', error: 'The selected model is unavailable.' } as SessionState['view']
    const html = await render()
    expect(html).toContain('The selected model is unavailable.')
    expect(html).not.toContain('subagentFailed')
    expect(html).not.toContain('retry')
  })

  it('shows the specific model unavailability reason while preserving the transcript', async () => {
    mocks.session.state.value.view = { id: 'child', status: 'needs_attention', model_available: false, model_unavailable_reason: 'model_missing' } as SessionState['view']
    const html = await render()
    expect(html).toContain('modelUnavailable_model_missing')
    expect(html).toContain('Child transcript')
    expect(html).not.toContain('retry')
  })
})
