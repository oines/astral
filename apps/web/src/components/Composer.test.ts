/* eslint-disable vue/one-component-per-file -- UI primitives are isolated test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setImmediate } from 'node:timers/promises'
import { createSSRApp, defineComponent, getCurrentInstance, h, reactive } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { SessionView } from '../lib/types'
import Composer from './Composer.vue'

const fixture = vi.hoisted(() => ({
  app: {
    drafts: {} as Record<string, unknown>, models: [] as { id: string; available: boolean }[], agents: [], computers: [],
    data: { preferences: {} as { running_input?: 'steer' | 'follow_up' } }, preferences: vi.fn(async () => undefined),
  },
  continueSession: vi.fn(async () => ({ data: { ok: true } })),
  submitInput: vi.fn(async (options: { body: { text: string; mode: string } }) => ({ data: { ok: true }, options })),
  send: undefined as undefined | (() => Promise<void>),
  handlers: undefined as undefined | { keyboard: (event: KeyboardEvent) => void; composerText: string; toggleQueue: () => Promise<void> },
}))
vi.mock('../stores/app', () => ({ useAppStore: () => reactive(fixture.app) }))
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
      const parent = getCurrentInstance()!.parent! as unknown as { setupState: NonNullable<typeof fixture.handlers> & { send: () => Promise<void> } }
      fixture.send = parent.setupState.send
      fixture.handlers = parent.setupState
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
beforeEach(() => {
  fixture.app.drafts = {}; fixture.app.models = []; fixture.app.data.preferences = {}
  fixture.send = undefined; fixture.handlers = undefined; vi.clearAllMocks()
})
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

type InputPreference = 'steer' | 'follow_up'
async function renderInput(binding = '', preference?: InputPreference, pending: SessionView['pending'] = []) {
  fixture.app.models = [{ id: 'model', available: true }]
  fixture.app.data.preferences = preference ? { running_input: preference } : {}
  fixture.app.drafts.session = { text: 'hello', files: [] }
  return renderToString(createSSRApp(Composer, { live: true, view: {
    ...view(false), binding_id: binding, status: 'running', profile_id: 'model', selection: { id: 'model' }, model_available: true, pending,
  } }))
}
async function keydown(properties: Partial<KeyboardEvent> = {}) {
  const event = { key: 'Enter', keyCode: 13, isComposing: false, shiftKey: false, altKey: false, ctrlKey: false, metaKey: false,
    preventDefault: vi.fn(), ...properties }
  // Invoke Composer's actual Vue handler captured from its rendered child;
  // allow its mocked asynchronous submission to settle before assertions.
  fixture.handlers!.keyboard(event as unknown as KeyboardEvent)
  await setImmediate()
  return event
}
describe('Composer keyboard input', () => {
  it('keeps Safari confirmation Enter after compositionend local, then sends the next independent Enter', async () => {
    await renderInput()
    // Safari has already ended composition and committed the English text to
    // v-model before emitting Enter with isComposing=false and keyCode=229.
    fixture.handlers!.composerText = 'example'
    const confirmation = await keydown({ keyCode: 229 })
    expect(confirmation.preventDefault).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
    expect(fixture.app.drafts.session).toMatchObject({ text: 'example' })

    const send = await keydown()
    expect(send.preventDefault).toHaveBeenCalledOnce()
    expect(fixture.submitInput).toHaveBeenCalledOnce()
    expect(fixture.submitInput.mock.calls[0]![0].body).toMatchObject({ text: 'example', mode: 'follow_up' })
    expect(fixture.app.drafts.session).toMatchObject({ text: '' })
  })
  it.each([13, 229])('does not submit composing Enter with keyCode=%s', async keyCode => {
    await renderInput()
    const event = await keydown({ isComposing: true, keyCode })
    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
    expect(fixture.app.drafts.session).toMatchObject({ text: 'hello' })
  })
  it.each([
    { name: 'ordinary Enter', keys: {}, mode: 'follow_up' },
    { name: 'Shift+Enter', keys: { shiftKey: true }, mode: undefined },
    { name: 'Alt+Enter', keys: { altKey: true }, mode: undefined },
    { name: 'Ctrl+Enter', keys: { ctrlKey: true }, mode: 'steer' },
    { name: 'Meta+Enter', keys: { metaKey: true }, mode: 'steer' },
  ])('$name retains its intended behavior in a bound chat', async ({ keys, mode }) => {
    await renderInput('binding')
    const event = await keydown(keys)
    if (mode) {
      expect(event.preventDefault).toHaveBeenCalledOnce()
      expect(fixture.submitInput).toHaveBeenCalledOnce()
      expect(fixture.submitInput.mock.calls[0]![0].body).toMatchObject({ text: 'hello', mode })
    } else {
      expect(event.preventDefault).not.toHaveBeenCalled()
      expect(fixture.submitInput).not.toHaveBeenCalled()
      expect(fixture.app.drafts.session).toMatchObject({ text: 'hello' })
    }
  })
  it.each([{ ctrlKey: true }, { metaKey: true }])('does not let a modifier override Safari composition confirmation: %j', async keys => {
    await renderInput('binding')
    const event = await keydown({ ...keys, keyCode: 229 })
    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })
})
describe.each(['', 'binding'])('Composer running-input preference (binding=%s)', binding => {
  it.each([undefined, 'follow_up', 'steer'] as const)('uses preference %s for normal submission', async preference => {
    await renderInput(binding, preference)
    await fixture.send!()
    expect(fixture.submitInput).toHaveBeenCalledOnce()
    expect(fixture.submitInput.mock.calls[0]![0].body).toMatchObject({ text: 'hello', mode: preference || 'follow_up' })
  })
})
describe('queue controls in IM-bound sessions', () => {
  const pending: SessionView['pending'] = [{ id: 'queued', kind: 'follow_up', metadata: {
    source: 'web', display_text: 'queued work', delivery_id: 'queued', sender: { id: 'owner', name: 'Owner' },
  } }]
  it.each(['follow_up', 'steer'] as const)('exposes the queue option in both existing menus with preference=%s', async preference => {
    const html = await renderInput('binding', preference, pending)
    const label = preference === 'follow_up' ? 'disableQueue' : 'enableQueue'
    expect(html.match(new RegExp(label, 'g'))).toHaveLength(2)
  })
  it('persists a queue preference change and applies it to the next bound Web input', async () => {
    await renderInput('binding')
    await fixture.handlers!.toggleQueue()
    expect(fixture.app.preferences).toHaveBeenCalledWith({ running_input: 'steer' })
    await keydown()
    expect(fixture.submitInput.mock.calls[0]![0].body).toMatchObject({ text: 'hello', mode: 'steer' })
  })
})
