/* eslint-disable vue/one-component-per-file -- UI primitives use isolated renderer stubs. */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, createSSRApp, defineComponent, h, nextTick, reactive, ssrContextKey, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import ToolCall from './ToolCall.vue'
import ToolResultImages from './ToolResultImages.vue'
import type { Entry, ProcessStatus } from '../lib/types'
import { useImagePreviewStore } from '../stores/image-preview'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }), createI18n: () => ({ global: { locale: { value: 'en' } } }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => ({ computers: [], sessions: [], sessionViews: {}, deletedSessions: new Set() }) }))
vi.mock('@astral/ui', () => ({
  Collapse: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  useDelayedPending: () => ({ value: false }),
}))
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
afterEach(() => { dispose?.(); setActivePinia(undefined); vi.useRealTimers() })
function mount(initial: Entry, extra: { live?: { name: string; started: boolean; ended: boolean; text: string }; process?: ProcessStatus } = {}) {
  vi.useFakeTimers()
  const pinia = createPinia()
  setActivePinia(pinia)
  const root = node('root')
  const props = reactive<{ entry: Entry; sessionID: string; live: { name: string; started: boolean; ended: boolean; text: string }; process?: ProcessStatus }>({ entry: initial, sessionID: 'session', live: { name: 'exec_command', started: true, ended: false, text: 'streaming output' }, ...extra })
  // Vitest compiles this SFC for SSR. A no-op client render lets us exercise
  // its real setup/watch lifecycle without installing a browser DOM emulator.
  const component = { ...ToolCall, render: () => null } as unknown as Component
  const app = renderer.createApp(defineComponent({ setup: () => () => h(component, props) }))
  app.use(pinia)
  app.provide(ssrContextKey, {})
  app.mount(root)
  dispose = () => app.unmount()
  const state = (app._instance!.subTree.component as unknown as { setupState: { toggle: () => void; open: boolean; output: string; active: boolean; mode: string } }).setupState
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

async function renderDetails(value: Entry, sessionID = 'session') {
  return renderToString(createSSRApp(ToolCall, { entry: value, sessionID }).use(createPinia()))
}

describe('semantic tool details in root and readonly child transcripts', () => {
  it('renders frozen read images once, using image ordinals rather than content offsets', async () => {
    const read = entry('read-result', 3)
    read.tool!.invocation.call = { id: 'read', name: 'read', arguments: { path: '/tmp/picture.png' } }
    read.tool!.result = { outcome: 'succeeded', content: [{ type: 'text', text: 'Image read' }, { type: 'image', metadata: { width: 120, height: 80 } }] }
    for (const sessionID of ['root', 'readonly-child']) {
      const html = await renderDetails(read, sessionID)
      expect(html.match(/<img /g)).toHaveLength(1)
      expect(html).toContain(`/sessions/${sessionID}/entries/read-result/images/0`)
      expect(html).toContain('width="120"')
      expect(html).not.toContain('/images/1')
    }
  })

  it('opens the existing shared image viewer and follows the latest persisted result', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const initial = entry('pending-read', 1, false)
    const props = reactive({ entry: initial, sessionID: 'readonly-child' })
    const root = node('root')
    const component = { ...ToolResultImages, render: () => null } as unknown as Component
    const app = renderer.createApp(defineComponent({ setup: () => () => h(component, props) }))
    app.use(pinia)
    app.provide(ssrContextKey, {})
    app.mount(root)
    dispose = () => app.unmount()
    const state = (app._instance!.subTree.component as unknown as { setupState: { open: (index: number) => void; images: unknown[] } }).setupState
    expect(state.images).toHaveLength(0)
    const result = entry('persisted-image', 4)
    result.tool!.result!.content = [{ type: 'text', text: 'Read image' }, { type: 'image' }, { type: 'image' }]
    props.entry = result
    await nextTick()
    expect(state.images).toHaveLength(2)
    state.open(1)
    const viewer = useImagePreviewStore(pinia)
    expect(viewer.open).toBe(true)
    expect(viewer.index).toBe(1)
    expect(viewer.items.map(item => item.src)).toEqual(['/api/v1/sessions/readonly-child/entries/persisted-image/images/0', '/api/v1/sessions/readonly-child/entries/persisted-image/images/1'])
  })

  it('shows the agent operation, task and returned stopping state without printing JSON', async () => {
    const control = entry('control-result', 3)
    control.tool!.invocation.call = { id: 'control', name: 'agent_control', arguments: { agent_id: 'child', input: 'Follow-up task\nwith details', model: 'profile', reasoning: { budget_tokens: 0 } } }
    const response = { session_id: 'child', name: 'Research', status: 'stopping', selection: { id: 'profile', reasoning: { budget_tokens: 0 } } }
    control.tool!.result = { outcome: 'succeeded', details: response, content: [{ type: 'text', text: JSON.stringify(response) }] }
    const html = await renderDetails(control)
    expect(html).toContain('toolAgentInput · toolAgentModel')
    expect(html).toContain('Follow-up task\nwith details')
    expect(html).toContain('Research')
    expect(html).toContain('stopping')
    expect(html).toContain('reasoningBudget 0')
    expect(html).not.toContain('session_id')
    expect(html).not.toContain('{&quot;')
  })

  it('shows message contents, attachment and actual unknown sending result', async () => {
    const send = entry('send-result', 3)
    send.tool!.invocation.call = { id: 'send', name: 'send_message', arguments: { text: 'Exact *literal* body', attachments: ['/tmp/report.pdf'] } }
    send.tool!.invocation.binding = { conversation_id: 'original-chat', binding_id: 'original-binding' }
    send.tool!.result = { outcome: 'failed', is_error: true, content: [{ type: 'text', text: JSON.stringify({ sent: null, error: 'Platform response lost', message_ids: ['returned-id'] }) }] }
    const html = await renderDetails(send)
    expect(html).toContain('Exact *literal* body')
    expect(html).toContain('/tmp/report.pdf')
    expect(html).toContain('original-chat')
    expect(html).toContain('toolMessageUnknown')
    expect(html).toContain('Platform response lost')
    expect(html).not.toContain('toolMessageSent')
    expect(html).not.toContain('{&quot;')
  })

  it('retains ordinary text for unknown tools', async () => {
    const custom = entry('custom-result', 3)
    custom.tool!.invocation.call.name = 'custom_tool'
    custom.tool!.result!.content = [{ type: 'text', text: 'Custom result **details**' }]
    expect(await renderDetails(custom)).toContain('Custom result **details**')
  })

  it.each(['agent_control', 'send_message'])('keeps older plain-text %s results readable', async name => {
    const old = entry('legacy-result', 3)
    old.tool!.invocation.call = { id: 'old', name, arguments: name === 'agent_control' ? { agent_id: 'exact-child-id', signal: 'delete' } : { text: 'Original message' } }
    old.tool!.invocation.binding = { conversation_id: 'exact-original-chat' }
    old.tool!.result!.content = [{ type: 'text', text: 'Older tool response: request processed.' }]
    const html = await renderDetails(old)
    expect(html).toContain('Older tool response: request processed.')
    expect(html).toContain(name === 'agent_control' ? 'exact-child-id' : 'exact-original-chat')
    expect(html).not.toContain('toolSelectedAgent')
  })

  it('keeps async exec_command spinning after tool_result until the background process exits', async () => {
    const yielded = entry('async-cmd', 3)
    yielded.tool!.result!.details = { output: 'starting server\n', session_id: 42 }
    const view = mount(yielded, {
      live: { name: 'exec_command', started: true, ended: true, text: '' },
      process: { execution_id: 'exec', running: true, has_output: true },
    })
    expect(view.state.active).toBe(true)
    expect(view.state.mode).toBe('expand')
    view.props.process = { execution_id: 'exec', running: false, exit_code: 0, has_output: true }
    await nextTick()
    expect(view.state.active).toBe(false)
  })

  it('omits disclosure affordance for static short commands and switch_computer', async () => {
    const shortCmd = entry('short-cmd', 3)
    shortCmd.tool!.invocation.call.arguments = { cmd: 'mkdir -p dist' }
    shortCmd.tool!.result!.details = { output: '', exit_code: 0 }
    const shortHtml = await renderDetails(shortCmd)
    expect(shortHtml).toContain('tool-call-static')
    expect(shortHtml).not.toContain('aria-expanded')

    const switched = entry('switch-comp', 4)
    switched.tool!.invocation.call = { id: 'sw', name: 'switch_computer', arguments: { computer_id: 'comp_1', cwd: '/work' } }
    switched.tool!.result!.content = [{ type: 'text', text: '{"computer_id":"comp_1","cwd":"/work"}' }]
    const switchHtml = await renderDetails(switched)
    expect(switchHtml).toContain('tool-call-static')
    expect(switchHtml).toContain('/work')
    expect(switchHtml).not.toContain('aria-expanded')
  })

  it('turns ordinary read_file with a bound computer into a direct workspace preview trigger', async () => {
    const read = entry('read-text', 3)
    read.tool!.invocation.call = { id: 'rd', name: 'read_file', arguments: { path: 'src/app.ts' } }
    read.tool!.invocation.binding = { computer_id: 'comp_1', cwd: '/repo' }
    read.tool!.result = { outcome: 'succeeded', content: [{ type: 'text', text: 'const x = 1' }] }
    const html = await renderDetails(read)
    expect(html).toContain('tool-call-file')
    expect(html).not.toContain('aria-expanded')
    expect(html).not.toContain('const x = 1')
  })

  it('renders web_search sources with host and summary without dumping raw JSON', async () => {
    const search = entry('search-1', 3)
    const payload = { query: 'astral', results: [{ title: 'Astral Docs', url: 'https://www.example.com/docs', summary: 'Concise summary text' }] }
    search.tool!.invocation.call = { id: 'ws', name: 'web_search', arguments: { query: 'astral' } }
    search.tool!.result = { outcome: 'succeeded', details: payload, content: [{ type: 'text', text: JSON.stringify(payload) }] }
    const html = await renderDetails(search)
    expect(html).toContain('Astral Docs')
    expect(html).toContain('example.com')
    expect(html).toContain('Concise summary text')
    expect(html).toContain('tool-source-summary')
    expect(html).not.toContain('{&quot;query&quot;')
  })

  it('renders spawn_agent semantically without dumping JSON', async () => {
    const spawn = entry('spawn-1', 3)
    const response = { session_id: 'child-1', name: 'Researcher', selection: { id: 'gpt-5' } }
    spawn.tool!.invocation.call = { id: 'sp', name: 'spawn_agent', arguments: { name: 'Researcher', task: 'Inspect logs', inherit_context: true } }
    spawn.tool!.result = { outcome: 'succeeded', details: response, content: [{ type: 'text', text: JSON.stringify(response) }] }
    const html = await renderDetails(spawn)
    expect(html).toContain('Researcher')
    expect(html).toContain('Inspect logs')
    expect(html).toContain('toolSpawnInherit')
    expect(html).not.toContain('{&quot;session_id&quot;')
  })

  it('renders inline diff for edit_file patches without dumping fallback text', async () => {
    const edit = entry('edit-1', 3)
    edit.tool!.invocation.call = { id: 'ed', name: 'edit_file', arguments: { path: 'a.ts' } }
    edit.tool!.invocation.binding = { computer_id: 'comp_1', cwd: '/repo' }
    edit.tool!.result = { outcome: 'succeeded', details: { patch: '@@ -1,2 +1,2 @@\n-old\n+new\n' }, content: [{ type: 'text', text: 'raw fallback text' }] }
    const html = await renderDetails(edit)
    expect(html).toContain('inline-diff')
    expect(html).toContain('inline-diff-add')
    expect(html).toContain('inline-diff-del')
    expect(html).not.toContain('raw fallback text')
  })
})
