/* eslint-disable vue/one-component-per-file -- UI primitives use isolated renderer stubs. */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h, nextTick, reactive, ssrContextKey, type Component } from 'vue'
import ToolCall from './ToolCall.vue'
import type { Entry } from '../lib/types'

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
afterEach(() => { dispose?.(); vi.useRealTimers() })
function mount(initial: Entry) {
  vi.useFakeTimers()
  const root = node('root')
  const props = reactive({ entry: initial, sessionID: 'session', live: { name: 'exec_command', started: true, ended: false, text: 'streaming output' } })
  // Vitest compiles this SFC for SSR. A no-op client render lets us exercise
  // its real setup/watch lifecycle without installing a browser DOM emulator.
  const component = { ...ToolCall, render: () => null } as unknown as Component
  const app = renderer.createApp(defineComponent({ setup: () => () => h(component, props) }))
  app.provide(ssrContextKey, {})
  app.mount(root)
  dispose = () => app.unmount()
  const state = (app._instance!.subTree.component as unknown as { setupState: { toggle: () => void; open: boolean; output: string } }).setupState
  const click = () => state.toggle()
  return { state, props, click }
}

describe('displaying tool details from the loaded history page', () => {
  it('exposes the complete output immediately when expanded', () => {
    const complete = entry('result', 3)
    complete.tool!.result!.details = { output: 'complete output' }
    const view = mount(complete)
    view.click()
    expect(view.state.open).toBe(true)
    expect(view.state.output).toBe('complete output')
    view.click()
    view.click()
    expect(view.state.output).toBe('complete output')
  })

  it('keeps an expanded invocation open when its final result replaces live output', async () => {
    const view = mount(entry('started', 1, false))
    view.click()
    expect(view.state.output).toBe('streaming output')
    const complete = entry('finished', 5)
    complete.tool!.result!.details = { output: 'final result' }
    view.props.entry = complete
    view.props.live.ended = true
    await nextTick()
    expect(view.state.output).toBe('final result')
    expect(view.state.open).toBe(true)
  })
})
