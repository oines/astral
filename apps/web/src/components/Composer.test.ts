/* eslint-disable vue/one-component-per-file -- UI primitives are isolated test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setImmediate } from 'node:timers/promises'
import { createSSRApp as createVueSSRApp, defineComponent, getCurrentInstance, h, reactive, type VNode } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createPinia, setActivePinia } from 'pinia'
import type { SessionView } from '../lib/types'
import Composer from './Composer.vue'

const fixture = vi.hoisted(() => ({
  app: {
    drafts: {} as Record<string, unknown>, models: [] as { id: string; available: boolean }[],
    agents: [] as { id: string; name: string; allowed_computers: string[] }[], computers: [] as { id: string; name: string; online: boolean; revoked: boolean }[],
    data: { preferences: {} as { running_input?: 'steer' | 'follow_up' } }, preferences: vi.fn(async () => undefined),
  },
  continueSession: vi.fn(async () => ({ data: { ok: true } })),
  submitInput: vi.fn(async (options: { body: { id: string; text: string; mode: string } }) => ({ data: { ok: true }, options })),
  inputReceipt: vi.fn(async () => ({ data: undefined, response: { status: 404 } })),
  createSession: vi.fn(async () => ({ data: undefined as unknown as SessionView })),
  selectComputer: vi.fn(async () => ({ data: { ok: true } })),
  selectModel: vi.fn(async () => ({ data: { ok: true } })),
  closeComputerMenu: vi.fn(),
  computerMenu: [] as VNode[],
  send: undefined as undefined | (() => Promise<void>),
  handlers: undefined as undefined | { keyboard: (event: KeyboardEvent) => void; composerText: string; sendLabel: string; error: string; toggleQueue: () => Promise<void> },
}))
vi.mock('../stores/app', () => ({ useAppStore: () => reactive(fixture.app) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../lib/api', async importOriginal => ({
  ...await importOriginal<object>(),
  errorText: (error: unknown) => String(error), fileURL: () => '',
  request: async (promise: Promise<{ data: unknown }>) => (await promise).data,
}))
vi.mock('@astral/sdk', async (importOriginal) => ({ ...await importOriginal<object>(), continueSession: fixture.continueSession, submitInput: fixture.submitInput, inputReceipt: fixture.inputReceipt, createSession: fixture.createSession, selectComputer: fixture.selectComputer, selectModel: fixture.selectModel }))
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
  Popover: defineComponent({ props: { label: { type: String, default: '' } }, setup(props, { slots }) {
    return () => {
      const items = slots.default?.({ close: props.label === 'computer' ? fixture.closeComputerMenu : () => {} })
      if (props.label === 'computer') fixture.computerMenu = items || []
      return h('div', { 'data-popover-label': props.label }, [...slots.trigger?.() || [], ...items || []])
    }
  } }),
}))
let router: Router
function createSSRApp(...args: Parameters<typeof createVueSSRApp>) { return createVueSSRApp(...args).use(router) }
function view(canContinue: boolean): SessionView {
  return { id: 'session', agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Task', source: 'web', pinned: false, archived: false,
    selection: { id: 'deleted-model' }, profile_id: 'deleted-model', rollout_id: 'rollout', head_id: 'head', status: 'needs_attention', event_sequence: 0,
    can_continue: canContinue, model_available: false, model_unavailable_reason: 'model_missing', pending: [], approvals: [], unresolved: [] }
}
beforeEach(async () => {
  setActivePinia(createPinia())
  fixture.app.drafts = {}; fixture.app.models = []; fixture.app.agents = []; fixture.app.computers = []; fixture.app.data.preferences = {}
  fixture.computerMenu = []
  fixture.send = undefined; fixture.handlers = undefined; vi.clearAllMocks()
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  await router.push('/')
})
describe('continuing a frozen request with an unavailable live model', () => {
  it('hides execution controls for an archived session while keeping the unsent draft', async () => {
    fixture.app.drafts.session = { text: 'Keep this draft', files: [] }
    const html = await renderToString(createSSRApp(Composer, { view: { ...view(true), archived: true }, live: true }))
    expect(html).not.toContain('composer-send')
    expect(html).not.toContain('textarea')
    expect(fixture.app.drafts.session).toMatchObject({ text: 'Keep this draft' })
    expect(fixture.continueSession).not.toHaveBeenCalled()
  })
  it('rejects a stale send callback when the session becomes archived', async () => {
    const current = reactive(view(true))
    await renderToString(createSSRApp(Composer, { view: current, live: true }))
    current.archived = true
    await fixture.send!()
    expect(fixture.continueSession).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })
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
describe('Composer slow and failed submissions', () => {
  it('ignores repeated Enter while awaiting acceptance and does not label the active request as a retry', async () => {
    let release!: () => void
    const pending = new Promise<void>(resolve => { release = resolve })
    fixture.submitInput.mockImplementationOnce(async options => {
      await pending
      return { data: { ok: true }, options }
    })
    await renderInput()
    await keydown()
    await keydown()
    expect(fixture.submitInput).toHaveBeenCalledOnce()
    expect(fixture.handlers!.sendLabel).toBe('queueMessage')
    expect(fixture.app.drafts.session).toMatchObject({ text: 'hello' })

    release()
    await setImmediate()
    expect(fixture.app.drafts.session).toMatchObject({ text: '', attempt: undefined })
  })
  it('keeps a failed draft and retries it with the same input identity', async () => {
    fixture.submitInput.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    await renderInput()
    await fixture.send!()
    const original = fixture.submitInput.mock.calls[0]![0].body
    expect(fixture.inputReceipt).toHaveBeenCalledOnce()
    expect(fixture.app.drafts.session).toMatchObject({ text: 'hello', attempt: original })
    expect(fixture.handlers!.sendLabel).toBe('retry')
    expect(fixture.handlers!.error).toContain('Failed to fetch')

    await fixture.send!()
    expect(fixture.submitInput).toHaveBeenCalledTimes(2)
    expect(fixture.submitInput.mock.calls[1]![0].body.id).toBe(original.id)
    expect(fixture.app.drafts.session).toMatchObject({ text: '', attempt: undefined })
    expect(fixture.handlers!.error).toBe('')
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

function computerMenuButtons(items: VNode[] = fixture.computerMenu): VNode[] {
  return items.flatMap(item => [
    ...(item.type === 'button' && item.props?.class === 'menu-item' ? [item] : []),
    ...(Array.isArray(item.children) ? computerMenuButtons(item.children.filter((child): child is VNode => typeof child === 'object' && child !== null && 'type' in child)) : []),
  ])
}
function manageComputersLink() {
  const link = fixture.computerMenu.find(item => item.props?.to)
  expect(link).toBeDefined()
  return link!.props as { to: { path: string; query: { agent: string; tab: string } }; onClick: () => void }
}
describe('Composer Computer management entry', () => {
  function computersFor(agentID: string, allowed: string[] = ['computer-1']) {
    // Another Bot appears first in the store; navigation must use the current
    // conversation's owner rather than a global/default Bot selection.
    fixture.app.agents = [{ id: 'other-bot', name: 'Other Bot', allowed_computers: [] }, { id: agentID, name: 'Current Bot', allowed_computers: allowed }]
    fixture.app.computers = [{ id: 'computer-1', name: 'Current Computer', online: true, revoked: false }]
  }

  it.each(['running', 'stopping', 'compacting'])('keeps management reachable during %s while only Computer switching is disabled', async status => {
    computersFor('current-bot')
    const draft = { text: 'Keep my unfinished message', files: [] }
    fixture.app.drafts.session = draft
    const html = await renderToString(createSSRApp(Composer, { live: true, view: { ...view(false), agent_id: 'current-bot', computer_id: 'computer-1', status } }))
    const trigger = html.match(/<button[^>]*class="composer-picker computer-picker"[^>]*>/)?.[0]
    expect(trigger).toBeDefined()
    expect(trigger).not.toContain('disabled')
    expect(computerMenuButtons()).toHaveLength(1)
    expect(computerMenuButtons()[0]!.props?.disabled).toBe(true)
    expect(html).toContain('href="/settings/agents?agent=current-bot&amp;tab=computers"')
    expect(html).toMatch(/<hr class="separator">\s*<a[^>]*class="menu-item"/)
    const link = manageComputersLink()
    expect(link.to).toEqual({ path: '/settings/agents', query: { agent: 'current-bot', tab: 'computers' } })
    link.onClick()
    expect(fixture.closeComputerMenu).toHaveBeenCalledOnce()
    expect(fixture.app.drafts.session).toBe(draft)
    expect(fixture.app.drafts.session).toMatchObject({ text: 'Keep my unfinished message' })
    expect(fixture.createSession).not.toHaveBeenCalled()
    expect(fixture.selectComputer).not.toHaveBeenCalled()
    expect(fixture.submitInput).not.toHaveBeenCalled()
  })

  it('offers Bot Computer permissions when no Computers are currently authorized', async () => {
    computersFor('no-computers', [])
    const html = await renderToString(createSSRApp(Composer, { live: true, view: { ...view(false), agent_id: 'no-computers', status: 'running' } }))
    expect(computerMenuButtons()).toHaveLength(0)
    expect(html.match(/<button[^>]*class="composer-picker computer-picker"[^>]*>/)?.[0]).not.toContain('disabled')
    expect(html).toContain('href="/settings/agents?agent=no-computers&amp;tab=computers"')
    manageComputersLink().onClick()
    expect(fixture.closeComputerMenu).toHaveBeenCalledOnce()
    expect(fixture.createSession).not.toHaveBeenCalled()
    expect(fixture.selectComputer).not.toHaveBeenCalled()
  })

  it('uses the new-conversation Bot and preserves its draft without creating a Session', async () => {
    computersFor('new-bot', [])
    const draft = { text: 'Continue writing after permissions', files: [], selection: { profile_id: 'chosen-model' } }
    fixture.app.drafts['new:new-bot'] = draft
    const html = await renderToString(createSSRApp(Composer, { live: true, view: { ...view(false), id: '', agent_id: 'new-bot', status: 'idle' } }))
    expect(html).toContain('href="/settings/agents?agent=new-bot&amp;tab=computers"')
    const link = manageComputersLink()
    expect(link.to.query).toEqual({ agent: 'new-bot', tab: 'computers' })
    link.onClick()
    expect(fixture.closeComputerMenu).toHaveBeenCalledOnce()
    expect(fixture.app.drafts['new:new-bot']).toBe(draft)
    expect(fixture.app.drafts['new:new-bot']).toEqual({ text: 'Continue writing after permissions', files: [], selection: { profile_id: 'chosen-model' } })
    expect(fixture.createSession).not.toHaveBeenCalled()
    expect(fixture.selectComputer).not.toHaveBeenCalled()
  })

  it('retains ordinary Computer switching when the conversation is idle', async () => {
    computersFor('current-bot')
    await renderToString(createSSRApp(Composer, { live: true, view: { ...view(false), agent_id: 'current-bot', status: 'idle' } }))
    const option = computerMenuButtons()[0]!
    expect(option.props?.disabled).toBe(false)
    const click = option.props!.onClick as () => void
    click()
    await setImmediate()
    expect(fixture.selectComputer).toHaveBeenCalledExactlyOnceWith({ path: { id: 'session' }, body: { computer_id: 'computer-1' } })
    expect(fixture.closeComputerMenu).toHaveBeenCalledOnce()
    expect(fixture.createSession).not.toHaveBeenCalled()
  })
})

describe('new-conversation model intent', () => {
  async function prepareNewConversation(explicit = false) {
    fixture.app.models = ['initial-model', 'server-model', 'manual-model'].map(id => ({ id, available: true }))
    fixture.app.drafts['new:agent'] = { text: 'Complete my original task', files: [], ...(explicit ? { selection: { profile_id: 'manual-model', reasoning: { effort: 'high' } } } : {}) }
    const initial: SessionView = { ...view(false), id: '', status: 'idle', profile_id: 'initial-model', selection: { id: 'initial-model' }, model_available: true }
    // Resource availability may change between displaying the composer and
    // creating the real Session. The server owns its current fallback choice.
    fixture.createSession.mockResolvedValue({ data: { ...initial, id: 'created', profile_id: 'server-model', selection: { id: 'server-model' } } })
    await renderToString(createSSRApp(Composer, { live: true, view: initial }))
    return initial
  }

  it('does not turn an automatic initial preview into a manual override of the server model', async () => {
    await prepareNewConversation()
    await fixture.send!()
    expect(fixture.createSession).toHaveBeenCalledExactlyOnceWith({ body: { agent_id: 'agent', title: '' } })
    expect(fixture.selectModel).not.toHaveBeenCalled()
    expect(fixture.submitInput).toHaveBeenCalledExactlyOnceWith({ path: { id: 'created' }, body: expect.objectContaining({ text: 'Complete my original task' }) })
  })

  it('preserves an explicit composer model and reasoning choice over the server initial model', async () => {
    await prepareNewConversation(true)
    await fixture.send!()
    expect(fixture.selectModel).toHaveBeenCalledExactlyOnceWith({ path: { id: 'created' }, body: { profile_id: 'manual-model', reasoning: { effort: 'high' } } })
    expect(fixture.submitInput).toHaveBeenCalledOnce()
  })

  it('confirms an explicit preference even when it matches the server active fallback model', async () => {
    const initial = await prepareNewConversation(true)
    fixture.createSession.mockResolvedValueOnce({ data: { ...initial, id: 'created', profile_id: 'manual-model', reasoning: { effort: 'high' }, selection: { id: 'manual-model', reasoning: { effort: 'high' } } } })
    await fixture.send!()
    // Matching the actual model does not prove that it is the Session's
    // preferred model. An explicit user choice still needs confirmation.
    expect(fixture.selectModel).toHaveBeenCalledExactlyOnceWith({ path: { id: 'created' }, body: { profile_id: 'manual-model', reasoning: { effort: 'high' } } })
    expect(fixture.submitInput).toHaveBeenCalledOnce()
  })

  it('reuses the created Session and confirmed explicit selection when retrying the original input', async () => {
    const initial = await prepareNewConversation(true)
    fixture.submitInput.mockRejectedValueOnce(new TypeError('The input response was lost.'))
    await fixture.send!()
    const original = fixture.submitInput.mock.calls[0]![0].body
    expect(fixture.app.drafts['new:agent']).toMatchObject({ text: 'Complete my original task', session: { id: 'created', profile_id: 'manual-model' }, confirmedModelSelection: { sessionID: 'created', profile_id: 'manual-model', reasoning: { effort: 'high' } } })
    // The task may already have moved to a fallback model after the lost
    // response. Remounting or retrying must not reapply the old preference.
    const draft = fixture.app.drafts['new:agent'] as { session: SessionView }
    draft.session.profile_id = 'server-model'
    draft.session.reasoning = undefined
    await renderToString(createSSRApp(Composer, { live: true, view: initial }))
    await fixture.send!()
    expect(fixture.createSession).toHaveBeenCalledOnce()
    expect(fixture.selectModel).toHaveBeenCalledOnce()
    expect(fixture.submitInput).toHaveBeenCalledTimes(2)
    expect(fixture.submitInput.mock.calls[1]![0]).toMatchObject({ path: { id: 'created' }, body: { id: original.id, text: 'Complete my original task' } })
  })

  it('keeps the created Session while retrying a failed explicit model override', async () => {
    const initial = await prepareNewConversation(true)
    fixture.createSession.mockResolvedValueOnce({ data: { ...initial, id: 'created', profile_id: 'manual-model', reasoning: { effort: 'high' } } })
    fixture.selectModel.mockRejectedValueOnce(new Error('The model selection was rejected.'))
    await fixture.send!()
    expect(fixture.app.drafts['new:agent']).toMatchObject({ text: 'Complete my original task', session: { id: 'created', profile_id: 'manual-model' } })
    expect(fixture.app.drafts['new:agent']).not.toHaveProperty('confirmedModelSelection', expect.anything())
    expect(fixture.submitInput).not.toHaveBeenCalled()
    await fixture.send!()
    expect(fixture.createSession).toHaveBeenCalledOnce()
    expect(fixture.selectModel).toHaveBeenCalledTimes(2)
    expect(fixture.submitInput).toHaveBeenCalledOnce()
  })

  it('applies a later explicit reasoning change instead of reusing an earlier confirmation', async () => {
    const initial = await prepareNewConversation(true)
    fixture.submitInput.mockRejectedValueOnce(new TypeError('The input response was lost.'))
    await fixture.send!()
    const draft = fixture.app.drafts['new:agent'] as { selection: Partial<SessionView> }
    draft.selection.reasoning = { effort: 'low' }
    await renderToString(createSSRApp(Composer, { live: true, view: initial }))
    await fixture.send!()
    expect(fixture.createSession).toHaveBeenCalledOnce()
    expect(fixture.selectModel).toHaveBeenCalledTimes(2)
    expect(fixture.selectModel).toHaveBeenLastCalledWith({ path: { id: 'created' }, body: { profile_id: 'manual-model', reasoning: { effort: 'low' } } })
  })

  it('clears an older confirmation before creating a new Session', async () => {
    await prepareNewConversation(true)
    const draft = fixture.app.drafts['new:agent'] as { confirmedModelSelection?: { sessionID: string; profile_id: string; reasoning: { effort: string } } }
    draft.confirmedModelSelection = { sessionID: 'created', profile_id: 'manual-model', reasoning: { effort: 'high' } }
    await fixture.send!()
    expect(fixture.createSession).toHaveBeenCalledOnce()
    expect(fixture.selectModel).toHaveBeenCalledOnce()
  })
})
