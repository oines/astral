/* eslint-disable vue/one-component-per-file -- UI primitives are isolated test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { SessionView } from '../lib/types'
import Composer from './Composer.vue'

const fixture = vi.hoisted(() => ({
  app: { drafts: {} as Record<string, unknown>, models: [], agents: [], computers: [], data: { preferences: {} } },
  continueSession: vi.fn(async () => ({ data: { ok: true } })),
  submitInput: vi.fn(async () => ({ data: { ok: true } })),
  send: undefined as undefined | (() => Promise<void>),
}))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../lib/api', () => ({
  errorText: (error: unknown) => String(error), fileURL: () => '',
  request: async (promise: Promise<{ data: unknown }>) => (await promise).data,
}))
vi.mock('@astral/sdk', async (importOriginal) => ({ ...await importOriginal<object>(), continueSession: fixture.continueSession, submitInput: fixture.submitInput }))
vi.mock('./ModelSelection.vue', () => ({ default: { render: () => null } }))
vi.mock('@astral/ui', async () => ({
  ...await import('../../../../packages/ui/src/useDelayedPending'),
  Button: defineComponent({ props: { disabled: Boolean }, setup(props, { attrs, slots }) {
    if (attrs.class === 'composer-send') {
      const parent = getCurrentInstance()!.parent! as unknown as { setupState: { send: () => Promise<void> } }
      fixture.send = parent.setupState.send
    }
    return () => h('button', { ...attrs, disabled: props.disabled }, slots.default?.())
  } }),
  Popover: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.({ close: () => {} })) } }),
}))
function view(canContinue: boolean): SessionView {
  return { id: 'session', agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Task', source: 'web', pinned: false, archived: false,
    selection: { id: 'deleted-model' }, profile_id: 'deleted-model', rollout_id: 'rollout', head_id: 'head', status: 'needs_attention', event_sequence: 0,
    can_continue: canContinue, model_available: false, model_unavailable_reason: 'model_missing', pending: [], approvals: [], unresolved: [] }
}
beforeEach(() => { fixture.app.drafts = {}; fixture.send = undefined; vi.clearAllMocks() })
describe('continuing a frozen request with an unavailable live model', () => {
  it('keeps Continue enabled and submits it without starting a new input', async () => {
    const html = await renderToString(createSSRApp(Composer, { view: view(true), live: true }))
    const button = html.match(/<button[^>]*class="composer-send"[^>]*>/)?.[0]
    expect(button).toBeDefined()
    expect(button).not.toContain('disabled')
    expect(fixture.send).toBeTypeOf('function')
    await fixture.send!()
    expect(fixture.continueSession).toHaveBeenCalledWith({ path: { id: 'session' } })
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })
  it('keeps a terminal run blocked when there is no frozen continuation', async () => {
    const html = await renderToString(createSSRApp(Composer, { view: view(false), live: true }))
    expect(html.match(/<button[^>]*class="composer-send"[^>]*>/)?.[0]).toContain('disabled')
    await fixture.send!()
    expect(fixture.continueSession).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })
  it('does not use frozen continuation permission to send new text', async () => {
    fixture.app.drafts.session = { text: 'new work', files: [] }
    const html = await renderToString(createSSRApp(Composer, { view: view(true), live: true }))
    expect(html.match(/<button[^>]*class="composer-send"[^>]*>/)?.[0]).toContain('disabled')
    await fixture.send!()
    expect(fixture.continueSession).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })
})
