/* eslint-disable vue/one-component-per-file -- UI primitives use isolated renderer stubs. */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, nextTick, reactive, ssrContextKey, type Component } from 'vue'
import ToolCall from './ToolCall.vue'
import type { Entry } from '../lib/types'

const mocks = vi.hoisted(() => ({ load: vi.fn() }))
vi.mock('../lib/tool-entry-cache', () => ({ toolEntry: mocks.load }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/ui', () => ({ Collapse: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }) }))
vi.mock('./Markdown.vue', () => ({ default: defineComponent({ props: { content: { type: String, default: '' } }, setup(props) { return () => h('div', props.content) } }) }))

type Node = { type: string; text: string; props: Record<string, unknown>; children: Node[]; parent?: Node }
const node = (type: string, text = ''): Node => ({ type, text, props: {}, children: [] })
const renderer = createRenderer<Node, Node>({
  createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
  insert(child, parent, anchor) {
    if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1)
    child.parent = parent
    const at = anchor ? parent.children.indexOf(anchor) : -1
    if (at >= 0) parent.children.splice(at, 0, child)
    else parent.children.push(child)
  },
  remove(child) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1) },
  setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
  parentNode: target => target.parent ?? null,
  nextSibling(target) { const siblings = target.parent?.children; return siblings?.[siblings.indexOf(target) + 1] ?? null },
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
})
function entry(id: string, sequence: number, finished = true): Entry {
  return { id, sequence, version: sequence, session_id: 'session', time: '', kind: finished ? 'tool_result' : 'tool_started', tool: { invocation: { session_id: 'session', run_id: 'run', turn_id: 'turn', assistant_id: 'assistant', execution_id: 'exec', call: { id: 'call', name: 'exec_command', arguments: { cmd: 'pwd' } } }, ...(finished ? { result: { outcome: 'succeeded' } } : {}) } }
}
let dispose: (() => void) | undefined
afterEach(() => { dispose?.(); vi.useRealTimers(); mocks.load.mockReset() })
function mount(initial: Entry) {
  vi.useFakeTimers()
  const root = node('root')
  const props = reactive({ entry: initial, sessionID: 'session' })
  // Vitest compiles this SFC for SSR. A no-op client render lets us exercise
  // its real setup/watch lifecycle without installing a browser DOM emulator.
  const component = { ...ToolCall, render: () => null } as unknown as Component
  const app = renderer.createApp(defineComponent({ setup: () => () => h(component, props) }))
  app.provide(ssrContextKey, {})
  app.mount(root)
  dispose = () => app.unmount()
  const state = (app._instance!.subTree.component as unknown as { setupState: { toggle: () => void; showLoading: boolean; output: string } }).setupState
  const click = () => state.toggle()
  return { state, props, click }
}

describe('loading complete tool details', () => {
  it('shows delayed feedback even when the lightweight entry already has a result object', async () => {
    let resolve!: (entry: Entry) => void
    mocks.load.mockImplementation(() => new Promise(done => { resolve = done }))
    const view = mount(entry('result', 3))
    view.click()
    await nextTick()
    expect(view.state.showLoading).toBe(false)
    await vi.advanceTimersByTimeAsync(299)
    expect(view.state.showLoading).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(view.state.showLoading).toBe(true)
    const complete = entry('result', 3)
    complete.tool!.result!.details = { output: 'complete output' }
    resolve(complete)
    await nextTick()
    await nextTick()
    expect(view.state.showLoading).toBe(false)
    expect(view.state.output).toBe('complete output')
  })

  it('loads the new immutable result entry when an expanded started invocation completes', async () => {
    mocks.load.mockResolvedValueOnce(entry('started', 1, false)).mockResolvedValueOnce({ ...entry('finished', 5), tool: { ...entry('finished', 5).tool!, result: { outcome: 'succeeded', details: { output: 'final result' } } } })
    const view = mount(entry('started', 1, false))
    view.click()
    await nextTick()
    await nextTick()
    view.props.entry = entry('finished', 5)
    await nextTick()
    await nextTick()
    await nextTick()
    expect(mocks.load).toHaveBeenNthCalledWith(1, 'session', 'started')
    expect(mocks.load).toHaveBeenNthCalledWith(2, 'session', 'finished')
    expect(view.state.output).toBe('final result')
    expect(view.state.showLoading).toBe(false)
  })
})
