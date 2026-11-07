/* eslint-disable vue/one-component-per-file -- UI primitives are isolated renderer stubs. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, nextTick, reactive, ssrContextKey, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import type { Agent, Profile } from '../../lib/types'
import type { ModelSelection } from '../../lib/model-reasoning'
import type { useSettingsSave } from '../../composables/useSettingsSave'
import { createSettingsMutations, type SettingsSaveState } from '../../lib/settings-mutations'
import BotModelPriority from './BotModelPriority.vue'

const fixture = vi.hoisted(() => ({
  agent: undefined as Agent | undefined,
  saves: undefined as Pick<ReturnType<typeof useSettingsSave>, 'run' | 'value' | 'state' | 'confirmAgent'> | undefined,
  models: [] as Profile[],
  refresh: vi.fn(async () => undefined),
  save: vi.fn(async ({ body }: { body: Agent }) => ({ data: body })),
  apply: vi.fn(async () => ({ data: { updated: 3 } })),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/sdk', async importOriginal => ({ ...await importOriginal<object>(),
  listAgents: async () => ({ data: [fixture.agent] }), saveAgent: fixture.save, applyAgentModelPriority: fixture.apply,
}))
vi.mock('../../stores/app', () => ({ useAppStore: () => ({ models: fixture.models, availableModels: fixture.models.filter(model => model.available), refresh: fixture.refresh }) }))
vi.mock('../../composables/useSettingsSave', () => ({ useSettingsSave: () => fixture.saves }))
vi.mock('@astral/ui', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    Button: defineComponent({
      inheritAttrs: false, props: { disabled: Boolean, busy: Boolean },
      setup: (props, { attrs, slots }) => () => h('button', { ...attrs, type: 'button', disabled: props.disabled || props.busy }, slots.default?.()),
    }),
    ConfirmPopover: defineComponent({ setup: (_props, { slots }) => () => h('div', slots.trigger?.()) }),
    SettingsRow: defineComponent({ setup: (_props, { slots }) => () => h('div', slots.default?.()) }),
    Switch: defineComponent({ setup: () => () => h('button', { type: 'button' }) }),
  }
})
vi.mock('../ModelSelection.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({
    inheritAttrs: false, props: { modelValue: { type: String, default: '' }, busy: Boolean, disabled: Boolean },
    setup: (props, { slots }) => () => h('div', { class: 'model-selection' }, slots.trigger?.() || h('button', { class: 'picker-trigger', type: 'button', disabled: props.disabled }, props.modelValue)),
  }) }
})
vi.mock('./SettingsFeedback.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ setup: () => () => h('div') }) }
})

type Policy = { models: NonNullable<Agent['model_priority']>; autoRestore: boolean }
type Setup = {
  policy: Policy; ready: boolean; move: (index: number, delta: number) => void; select: (index: number, value: ModelSelection) => void; remove: (index: number) => void; save: (policy: Policy) => Promise<boolean>; apply: () => Promise<void>; applying: boolean; applyError: string; confirm: boolean
  listElement?: HTMLOListElement; dragging: string; pointerActive: boolean; dropBefore: string | null; editingDisabled: boolean; announcement: string
  startDrag: (event: PointerEvent, id: string) => void; dragMove: (event: PointerEvent) => void; endDrag: (event: PointerEvent) => void; cancelPointer: (event: PointerEvent) => void; cancelDrag: () => void; sortKey: (event: KeyboardEvent, id: string) => void
}
class TestNode {
  children: TestNode[] = []
  parent?: TestNode
  props: Record<string, unknown> = {}
  captures = new Set<number>()
  setPointerCapture = vi.fn((id: number) => { this.captures.add(id) })
  hasPointerCapture = vi.fn((id: number) => this.captures.has(id))
  releasePointerCapture = vi.fn((id: number) => { this.captures.delete(id) })
  focus = vi.fn()
  rect = { left: 0, right: 400, top: 0, bottom: 50, width: 400, height: 50 }
  constructor(readonly type: string, public text = '') {}
  get dataset() { return { modelId: this.props['data-model-id'] as string | undefined } }
  getBoundingClientRect() { return this.rect }
  querySelectorAll(selector: string): TestNode[] {
    return this.children.flatMap(child => [...(selector === '[data-model-id]' && child.dataset.modelId || selector === '[data-reorder-handle]' && 'data-reorder-handle' in child.props ? [child] : []), ...child.querySelectorAll(selector)])
  }
  querySelector(selector: string) { return this.querySelectorAll(selector)[0] || null }
}
const renderer = createRenderer<TestNode, TestNode>({
  createElement: type => new TestNode(type), createText: text => new TestNode('#text', text), createComment: text => new TestNode('#comment', text),
  setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text; node.children = [] },
  patchProp: (node, key, _previous, value) => { node.props[key] = value }, parentNode: node => node.parent ?? null,
  nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  insert: (node, parent, anchor) => {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const at = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(at < 0 ? parent.children.length : at, 0, node)
  },
  remove: node => { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1) },
})
let unmount: (() => void) | undefined
let root: TestNode
beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  vi.stubGlobal('window', new EventTarget())
  fixture.agent = reactive({ id: 'bot', revision: 1, created_at: '', updated_at: '', name: 'Bot', allowed_computers: [], default_model_id: 'preferred', default_reasoning: { effort: 'high' }, model_priority: [{ id: 'preferred', reasoning: { effort: 'high' } }, { id: 'disabled', reasoning: {} }], model_auto_restore: false })
  fixture.models = [{ id: 'preferred', name: 'Preferred', available: true }, { id: 'disabled', name: 'Paused', available: false }, { id: 'alternate', name: 'Alternate', available: true }] as Profile[]
  fixture.save.mockImplementation(async ({ body }) => ({ data: { ...body, revision: body.revision + 1 } }))
  fixture.apply.mockResolvedValue({ data: { updated: 3 } })
  const states = reactive<Record<string, SettingsSaveState>>({})
  const intents: Record<string, unknown> = {}
  const mutations = createSettingsMutations((key, state) => { states[key] = state })
  fixture.saves = {
    run: (resource, field, operation, intent) => { intents[`${resource}/${field}`] = intent; return mutations.run(resource, field, operation) },
    value: <T>(key: string, confirmed: T): T => states[key]?.status === 'saving' && intents[key] !== undefined ? intents[key] as T : confirmed,
    state: key => states[key] || { status: 'idle' },
    confirmAgent: value => { Object.assign(fixture.agent!, value) },
  }
})
afterEach(() => { unmount?.(); unmount = undefined; vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals() })
function clientRender(): RenderFunction {
  const filename = 'BotModelPriority.vue'
  const { descriptor } = parse(readFileSync(new URL('./BotModelPriority.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const result = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (result.errors.length) throw new Error(result.errors.map(String).join('\n'))
  return new Function('Vue', result.code)(Vue) as RenderFunction
}
function mount(template = false) {
  // Exercise the actual component's mutation callbacks and reactive rollback
  // without replacing the shared picker in the production template.
  const app = renderer.createApp({ ...BotModelPriority, render: template ? clientRender() : () => null }, { agent: fixture.agent })
  app.provide(ssrContextKey, { modules: new Set() })
  root = new TestNode('root')
  app.mount(root)
  unmount = () => app.unmount()
  return (app._instance! as unknown as { setupState: Setup }).setupState
}
async function settled() { await vi.advanceTimersByTimeAsync(0); await nextTick() }
function geometry(view: Setup) {
  const list = new TestNode('ol')
  list.rect = { ...list.rect, bottom: view.policy.models.length * 50, height: view.policy.models.length * 50 }
  for (const [index, model] of view.policy.models.entries()) {
    const row = new TestNode('li')
    row.props['data-model-id'] = model.id
    row.rect = { ...row.rect, top: index * 50, bottom: (index + 1) * 50 }
    const handle = new TestNode('button')
    handle.props['data-reorder-handle'] = ''
    row.children.push(handle)
    list.children.push(row)
  }
  view.listElement = list as unknown as HTMLOListElement
  return { list, handle: (id: string) => list.children.find(row => row.dataset.modelId === id)!.children[0]! }
}
function pointer(handle: TestNode, fields: Partial<PointerEvent> = {}) {
  return { pointerId: 7, isPrimary: true, button: 0, clientX: 100, clientY: 25, currentTarget: handle, preventDefault: vi.fn(), ...fields } as unknown as PointerEvent
}
function key(value: string) { return { key: value, preventDefault: vi.fn() } as unknown as KeyboardEvent }
function addThirdModel() { fixture.agent!.model_priority!.push({ id: 'alternate', reasoning: { effort: 'low' }, options: { temperature: 0.15 } }) }
function find(node: TestNode, predicate: (node: TestNode) => boolean): TestNode | undefined {
  if (predicate(node)) return node
  for (const child of node.children) { const result = find(child, predicate); if (result) return result }
}
async function fire(node: TestNode, type: string, fields: Record<string, unknown> = {}) {
  const event = Object.assign(new Event(type, { cancelable: true, bubbles: true }), fields)
  Object.defineProperty(event, 'target', { value: node })
  for (let target: TestNode | undefined = node; target; target = target.parent) {
    Object.defineProperty(event, 'currentTarget', { configurable: true, value: target })
    const handler = target.props[`on${type[0]!.toUpperCase()}${type.slice(1)}`]
    if (typeof handler === 'function') handler(event)
    if (event.cancelBubble) break
  }
  await nextTick()
  return event
}

describe('Bot model priority settings', () => {
  it('reorders the complete list while keeping unavailable entries and per-model reasoning', async () => {
    const view = mount()
    view.move(1, -1)
    expect(view.policy.models.map(model => model.id)).toEqual(['disabled', 'preferred'])
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
    expect(fixture.save.mock.calls[0]![0].body).toMatchObject({ model_priority: [{ id: 'disabled', reasoning: {} }, { id: 'preferred', reasoning: { effort: 'high' } }], default_model_id: 'disabled', default_reasoning: {}, model_auto_restore: false })
  })
  it('refuses duplicates and restores the confirmed list after a rejected complete save', async () => {
    const view = mount()
    view.select(2, { profile_id: 'preferred' })
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
    fixture.save.mockRejectedValueOnce({ code: 'invalid', message: 'The list changed.' })
    view.select(0, { profile_id: 'alternate', reasoning: { effort: 'low' } })
    await settled()
    expect(view.policy.models[0]).toEqual({ id: 'preferred', reasoning: { effort: 'high' } })
    expect(fixture.saves!.state('agent:bot/model-priority')).toMatchObject({ status: 'error', error: 'The list changed.' })
    expect(view.ready).toBe(false)
  })
  it('keeps generation overrides when changing only reasoning for the same model', async () => {
    fixture.agent!.model_priority![0]!.options = { temperature: 0.15 }
    const view = mount()
    view.select(0, { profile_id: 'preferred', reasoning: { effort: 'low' } })
    await settled()
    expect(fixture.save.mock.calls[0]![0].body.model_priority![0]).toMatchObject({ id: 'preferred', reasoning: { effort: 'low' }, options: { temperature: 0.15 } })
  })
  it('confirms a committed save with server-normalized empty options after losing the response', async () => {
    const view = mount()
    fixture.save.mockImplementationOnce(async ({ body }) => {
      Object.assign(fixture.agent!, { ...body, revision: body.revision + 1, model_priority: body.model_priority?.map(model => ({ ...model, options: model.options || {} })) })
      throw new TypeError('The save response was lost.')
    })
    view.select(2, { profile_id: 'alternate', reasoning: {} })
    await settled()
    expect(fixture.saves!.state('agent:bot/model-priority').status).toBe('saved')
    expect(view.policy.models.at(-1)).toMatchObject({ id: 'alternate', reasoning: {}, options: {} })
    expect(view.ready).toBe(true)
  })
  it('applies the saved strategy only after saving and keeps failures available for retry', async () => {
    const view = mount()
    view.select(2, { profile_id: 'alternate', reasoning: {} })
    await view.apply()
    expect(fixture.apply).not.toHaveBeenCalled()
    await settled()
    fixture.apply.mockRejectedValueOnce(new Error('Connection interrupted.'))
    view.confirm = true
    await view.apply()
    expect(view.applyError).toBe('Connection interrupted.')
    expect(view.confirm).toBe(true)
    expect(fixture.refresh).toHaveBeenCalled()
    await view.apply()
    expect(fixture.apply).toHaveBeenCalledTimes(2)
    expect(view.confirm).toBe(false)
    expect(view.applyError).toBe('')
  })
})

describe('Bot model priority pointer sorting', () => {
  it.each(['mouse', 'touch'])('previews %s movement and saves one complete order only on drop', async pointerType => {
    addThirdModel()
    const view = mount()
    const { handle } = geometry(view)
    const source = handle('preferred')
    view.startDrag(pointer(source, { pointerType }), 'preferred')
    expect(source.setPointerCapture).toHaveBeenCalledExactlyOnceWith(7)
    expect(view.editingDisabled).toBe(true)
    view.dragMove(pointer(source, { pointerType, clientY: 95 }))
    expect(view.dropBefore).toBe('alternate')
    view.dragMove(pointer(source, { pointerType, clientY: 135 }))
    expect(view.dropBefore).toBe('')
    expect(view.policy.models.map(model => model.id)).toEqual(['preferred', 'disabled', 'alternate'])
    view.select(0, { profile_id: 'alternate' })
    view.remove(1)
    expect(fixture.save).not.toHaveBeenCalled()
    view.endDrag(pointer(source, { pointerType, clientY: 135 }))
    expect(view.pointerActive).toBe(false)
    expect(view.policy.models.map(model => model.id)).toEqual(['disabled', 'alternate', 'preferred'])
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
    expect(fixture.save.mock.calls[0]![0].body.model_priority).toEqual([
      { id: 'disabled', reasoning: {} }, { id: 'alternate', reasoning: { effort: 'low' }, options: { temperature: 0.15 } }, { id: 'preferred', reasoning: { effort: 'high' } },
    ])
    expect(source.releasePointerCapture).toHaveBeenCalledExactlyOnceWith(7)
    expect(source.focus).toHaveBeenLastCalledWith()
  })

  it('moves the last row before the first without an insertion-index offset', async () => {
    addThirdModel()
    const view = mount()
    const source = geometry(view).handle('alternate')
    view.startDrag(pointer(source, { clientY: 125 }), 'alternate')
    view.dragMove(pointer(source, { clientY: 5 }))
    expect(view.dropBefore).toBe('preferred')
    view.endDrag(pointer(source, { clientY: 5 }))
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
    expect(view.policy.models.map(model => model.id)).toEqual(['alternate', 'preferred', 'disabled'])
  })

  it('does not save a short press, movement below threshold, or a return to the original position', async () => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 30 }))
    expect(view.dragging).toBe('')
    view.endDrag(pointer(source, { clientY: 30 }))
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 95 }))
    view.dragMove(pointer(source, { clientY: 25 }))
    expect(view.dragging).toBe('preferred')
    expect(view.dropBefore).toBe('disabled')
    view.endDrag(pointer(source))
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
    expect(view.policy.models.map(model => model.id)).toEqual(['preferred', 'disabled'])
  })

  it.each(['Escape', 'pointercancel', 'lostpointercapture', 'blur', 'global Escape', 'unmount'])('cancels %s without a later pointerup saving the preview', async cancellation => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 95 }))
    expect(view.dropBefore).toBe('')
    if (cancellation === 'Escape') view.sortKey(key('Escape'), 'preferred')
    else if (cancellation === 'pointercancel' || cancellation === 'lostpointercapture') view.cancelPointer(pointer(source, { type: cancellation }))
    else if (cancellation === 'blur') window.dispatchEvent(new Event('blur'))
    else if (cancellation === 'global Escape') window.dispatchEvent(Object.assign(new Event('keydown'), { key: 'Escape' }))
    else { unmount!(); unmount = undefined }
    expect(view.pointerActive).toBe(false)
    expect(view.dragging).toBe('')
    expect(view.dropBefore).toBeNull()
    expect(source.releasePointerCapture).toHaveBeenCalledOnce()
    view.endDrag(pointer(source, { clientY: 95 }))
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
  })

  it.each(['revision', 'models', 'agent', 'autoRestore'])('cancels when external %s changes invalidate the captured policy', async change => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 95 }))
    if (change === 'revision') fixture.agent!.revision++
    else if (change === 'models') fixture.agent!.model_priority!.reverse()
    else if (change === 'agent') fixture.agent!.id = 'another-bot'
    else fixture.agent!.model_auto_restore = true
    expect(view.pointerActive).toBe(false)
    view.endDrag(pointer(source, { clientY: 95 }))
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
  })

  it('ignores a second pointer and refuses non-primary or secondary-button starts', async () => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    view.startDrag(pointer(source, { button: 2 }), 'preferred')
    view.startDrag(pointer(source, { isPrimary: false }), 'preferred')
    expect(source.setPointerCapture).not.toHaveBeenCalled()
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { pointerId: 8, clientY: 95 }))
    view.cancelPointer(pointer(source, { pointerId: 8 }))
    view.endDrag(pointer(source, { pointerId: 8, clientY: 95 }))
    expect(view.pointerActive).toBe(true)
    expect(view.dragging).toBe('')
    view.dragMove(pointer(source, { clientY: 95 }))
    view.endDrag(pointer(source, { clientY: 95 }))
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
  })

  it('does not save a drop outside the list or allow sorting while saving or applying', async () => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 95 }))
    view.dragMove(pointer(source, { clientX: 450, clientY: 95 }))
    expect(view.dropBefore).toBeNull()
    view.endDrag(pointer(source, { clientX: 450, clientY: 95 }))
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
    source.setPointerCapture.mockClear()
    view.applying = true
    view.startDrag(pointer(source), 'preferred')
    view.sortKey(key('End'), 'preferred')
    expect(source.setPointerCapture).not.toHaveBeenCalled()
    expect(fixture.save).not.toHaveBeenCalled()
    view.applying = false
    let finish!: () => void
    fixture.save.mockImplementationOnce(({ body }) => new Promise(resolve => { finish = () => resolve({ data: { ...body, revision: body.revision + 1 } }) }))
    view.move(0, 1)
    await settled()
    expect(view.editingDisabled).toBe(true)
    view.startDrag(pointer(source), 'preferred')
    view.sortKey(key('Home'), 'preferred')
    expect(source.setPointerCapture).not.toHaveBeenCalled()
    expect(fixture.save).toHaveBeenCalledOnce()
    finish()
    await settled()
    expect(view.editingDisabled).toBe(false)
  })

  it('restores the confirmed order and moved handle focus after a failed drop save', async () => {
    const view = mount()
    const source = geometry(view).handle('preferred')
    fixture.save.mockRejectedValueOnce({ code: 'conflict', message: 'Another editor changed this list.' })
    view.startDrag(pointer(source), 'preferred')
    view.dragMove(pointer(source, { clientY: 95 }))
    view.endDrag(pointer(source, { clientY: 95 }))
    expect(view.policy.models.map(model => model.id)).toEqual(['disabled', 'preferred'])
    await settled()
    expect(view.policy.models.map(model => model.id)).toEqual(['preferred', 'disabled'])
    expect(source.focus).toHaveBeenLastCalledWith()
    expect(fixture.saves!.state('agent:bot/model-priority')).toMatchObject({ status: 'error', error: 'Another editor changed this list.' })
    expect(view.announcement).toBe('')
  })

  it('binds pointer and keyboard sorting to the rendered handle while keeping picker interaction separate', async () => {
    const view = mount(true)
    const picker = find(root, node => node.props.class === 'picker-trigger')!
    const handle = find(root, node => 'data-reorder-handle' in node.props)!
    expect(picker).toBeDefined()
    expect(handle).toBeDefined()
    const fields = { pointerId: 7, pointerType: 'mouse', isPrimary: true, button: 0, clientX: 100, clientY: 25 }
    await fire(picker, 'pointerdown', fields)
    expect(view.pointerActive).toBe(false)
    expect(handle.setPointerCapture).not.toHaveBeenCalled()
    await fire(handle, 'pointerdown', fields)
    expect(view.pointerActive).toBe(true)
    expect(handle.setPointerCapture).toHaveBeenCalledExactlyOnceWith(7)
    expect(picker.props.disabled).toBe(true)
    expect(handle.props.disabled).toBe(false)
    await fire(handle, 'pointercancel', fields)
    expect(view.pointerActive).toBe(false)
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
    await fire(handle, 'keydown', { key: 'End' })
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
    expect(view.policy.models.map(model => model.id)).toEqual(['disabled', 'preferred'])
    expect(handle.focus).toHaveBeenLastCalledWith()
  })
})

describe('Bot model priority keyboard sorting', () => {
  it.each([
    { key: 'ArrowUp', id: 'disabled', order: ['disabled', 'preferred', 'alternate'] },
    { key: 'ArrowDown', id: 'preferred', order: ['disabled', 'preferred', 'alternate'] },
    { key: 'Home', id: 'alternate', order: ['alternate', 'preferred', 'disabled'] },
    { key: 'End', id: 'preferred', order: ['disabled', 'alternate', 'preferred'] },
  ])('supports $key, saves once and restores focus to the moved model', async scenario => {
    addThirdModel()
    const view = mount()
    const source = geometry(view).handle(scenario.id)
    const event = key(scenario.key)
    view.sortKey(event, scenario.id)
    expect(event.preventDefault).toHaveBeenCalledOnce()
    await settled()
    expect(fixture.save).toHaveBeenCalledOnce()
    expect(view.policy.models.map(model => model.id)).toEqual(scenario.order)
    expect(source.focus).toHaveBeenCalledExactlyOnceWith()
    expect(view.announcement).toBe('botModelMoved')
  })

  it('does not save at list boundaries or intercept unrelated navigation keys', async () => {
    const view = mount()
    geometry(view)
    for (const [value, id] of [['ArrowUp', 'preferred'], ['Home', 'preferred'], ['ArrowDown', 'disabled'], ['End', 'disabled']]) view.sortKey(key(value!), id!)
    for (const value of ['Tab', 'Enter', 'ArrowLeft']) {
      const event = key(value)
      view.sortKey(event, 'preferred')
      expect(event.preventDefault).not.toHaveBeenCalled()
    }
    await settled()
    expect(fixture.save).not.toHaveBeenCalled()
  })
})
