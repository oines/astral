/* eslint-disable vue/one-component-per-file -- Lightweight renderer fixtures isolate directory navigation from file loading. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, nextTick, reactive, ssrContextKey, type PropType, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import { createPinia, setActivePinia } from 'pinia'
import type { FileRef } from '../lib/types'
import type { SurfaceTab } from '../stores/surface'
import { useSurfaceStore } from '../stores/surface'
import SurfaceWorkspace from './SurfaceWorkspace.vue'

type RootSession = { id: string; agent_id: string; computer_id: string; cwd: string }
interface AppFixture {
  sessionViews: Record<string, RootSession>
  sessions: RootSession[]
  agents: Array<{ id: string; default_computer_id: string }>
  activeAgentID: string
  computers: Array<{ id: string; name: string }>
  deletedSessions: Set<string>
}
const fixture = vi.hoisted(() => ({
  app: undefined as unknown as AppFixture,
  explorers: [] as Array<{ readonly source: Pick<FileRef, 'computer_id' | 'path'>; readonly sessionID: string; open: (file: FileRef) => void }>,
  contents: [] as Array<{ id: string; active: boolean; activations: number }>,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@vueuse/core', () => ({ useResizeObserver: () => undefined }))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('@astral/ui', async () => {
  const { defineComponent, h } = await import('vue')
  return { Button: defineComponent({ props: { disabled: Boolean }, setup: (props, { attrs, slots }) => () => h('button', { ...attrs, disabled: props.disabled }, slots.default?.()) }) }
})
vi.mock('./files/FileExplorer.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({
    props: { source: { type: Object as PropType<Pick<FileRef, 'computer_id' | 'path'>>, required: true }, sessionID: { type: String, required: true } },
    emits: ['openFile'],
    setup(props, { emit }) {
      fixture.explorers.push({ get source() { return props.source }, get sessionID() { return props.sessionID }, open: file => emit('openFile', file) })
      return () => h('section', { 'data-directory-source': props.source.computer_id }, props.source.path)
    },
  }) }
})
vi.mock('./files/SurfaceFile.vue', async () => {
  const { defineComponent, h, onActivated, onDeactivated } = await import('vue')
  return { default: defineComponent({
    props: { tab: { type: Object as PropType<SurfaceTab>, required: true } },
    setup(props) {
      const content = { id: props.tab.id, active: true, activations: 0 }
      fixture.contents.push(content)
      onActivated(() => { content.active = true; content.activations++ })
      onDeactivated(() => { content.active = false })
      return () => h('section', { 'data-file-tab': props.tab.id })
    },
  }) }
})
vi.mock('./SubagentList.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ props: { rootID: { type: String, required: true } }, setup: props => () => h('section', { 'data-subagent-list': props.rootID }) }) }
})
vi.mock('./SubagentSurface.vue', async () => {
  const { defineComponent, h, onActivated, onDeactivated } = await import('vue')
  return { default: defineComponent({
    props: { sessionID: { type: String, required: true } },
    setup(props) {
      const content = { id: props.sessionID, active: true, activations: 0 }
      fixture.contents.push(content)
      onActivated(() => { content.active = true; content.activations++ })
      onDeactivated(() => { content.active = false })
      return () => h('section', { 'data-child-tab': props.sessionID })
    },
  }) }
})

interface HostNode { type: string; text: string; props: Record<string, unknown>; style: Record<string, string>; children: HostNode[]; parent: HostNode | null; inert: boolean; scrollTop: number; querySelector: () => null }
function node(type: string, text = ''): HostNode { return { type, text, props: {}, style: {}, children: [], parent: null, inert: false, scrollTop: 0, querySelector: () => null } }
function insert(child: HostNode, parent: HostNode, anchor: HostNode | null = null) {
  if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1)
  child.parent = parent
  const index = anchor ? parent.children.indexOf(anchor) : -1
  parent.children.splice(index < 0 ? parent.children.length : index, 0, child)
}
const renderer = createRenderer<HostNode, HostNode>({
  createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
  setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
  parentNode: target => target.parent, nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  insert,
  remove: target => { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null },
  insertStaticContent: (html, parent, anchor) => { const target = node('#static', html); insert(target, parent, anchor); return [target, target] },
})
function clientRender(): RenderFunction {
  const filename = 'SurfaceWorkspace.vue'
  const { descriptor } = parse(readFileSync(new URL('./SurfaceWorkspace.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const result = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (result.errors.length) throw new Error(result.errors.map(String).join('\n'))
  // The custom host exercises real Transition/KeepAlive hooks without a CSS
  // engine. Fragment groups preserve the same keyed children and nav element.
  const Transition = Vue.defineComponent({
    inheritAttrs: false,
    props: { name: String },
    setup: (_props, { attrs, slots }) => () => Vue.h(Vue.BaseTransition, attrs, slots),
  })
  const TransitionGroup = Vue.defineComponent({
    inheritAttrs: false,
    props: { name: String },
    setup: (_props, { slots }) => () => Vue.h(Vue.Fragment, null, slots.default?.()),
  })
  return new Function('Vue', result.code)({ ...Vue, Transition, TransitionGroup }) as RenderFunction
}
const all = (root: HostNode): HostNode[] => [root, ...root.children.flatMap(all)]
let unmount: (() => void) | undefined
function mount() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const surface = useSurfaceStore()
  surface.setSession('root')
  surface.reopen()
  const root = node('root')
  const app = renderer.createApp(Object.assign({}, SurfaceWorkspace, { render: clientRender() }), { rootID: 'root', activities: {} })
  app.use(pinia)
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount(root)
  unmount = () => app.unmount()
  const files = () => all(root).find(item => item.type === 'button' && String(item.props.class).includes('workspace-files-toggle'))!
  return { root, surface, files }
}
beforeEach(() => {
  fixture.app = reactive({ sessionViews: { root: { id: 'root', agent_id: 'bot', computer_id: 'office', cwd: '/work/project' } }, sessions: [], agents: [{ id: 'bot', default_computer_id: 'default' }], activeAgentID: 'bot', computers: [{ id: 'office', name: 'Office' }, { id: 'other', name: 'Other computer' }], deletedSessions: new Set<string>() })
  fixture.explorers = []
  fixture.contents = []
})
afterEach(() => { unmount?.(); unmount = undefined })

describe('directory browsing before a file tab exists', () => {
  it('offers Files on the subagent list and waits for the click before mounting its directory', async () => {
    const workspace = mount()
    await nextTick()
    expect(workspace.surface.current).toMatchObject({ kind: 'subagents', id: 'root' })
    expect(workspace.files().props.disabled).toBe(false)
    expect(fixture.explorers).toHaveLength(0)
    expect(all(workspace.root).some(item => item.props['data-subagent-list'] === 'root')).toBe(true)
    await (workspace.files().props.onClick as () => void)()
    await nextTick()
    expect(workspace.files().props['aria-expanded']).toBe(true)
    expect(fixture.explorers).toHaveLength(1)
    expect(fixture.explorers[0]?.source).toEqual({ computer_id: 'office', path: '/work/project' })
    expect(fixture.explorers[0]?.sessionID).toBe('root')
    expect(workspace.surface.tabs.map(tab => tab.kind)).toEqual(['subagents'])
    expect(workspace.surface.current?.kind).toBe('subagents')
  })

  it('follows the root computer until a file is opened, then keeps the file source across task computer changes', async () => {
    const workspace = mount()
    await (workspace.files().props.onClick as () => void)()
    await nextTick()
    fixture.app.sessionViews.root = { id: 'root', agent_id: 'bot', computer_id: 'other', cwd: '/other/project' }
    await nextTick()
    const explorer = fixture.explorers[0]!
    expect(explorer.source).toEqual({ computer_id: 'other', path: '/other/project' })
    expect(workspace.surface.current?.kind).toBe('subagents')
    const file: FileRef = { id: 'selected-file', revision: 1, created_at: '', updated_at: '', agent_id: 'bot', computer_id: 'other', path: '/other/project/report.md', display_name: 'report.md', media_type: 'text/markdown', size: 100 }
    explorer.open(file)
    await nextTick()
    expect(workspace.surface.tabs.map(tab => tab.kind)).toEqual(['subagents', 'file'])
    expect(workspace.surface.current).toMatchObject({ kind: 'file', id: file.id, sessionID: 'root', ref: file })
    expect(explorer.source).toEqual(file)
    fixture.app.sessionViews.root = { id: 'root', agent_id: 'bot', computer_id: 'office', cwd: '/new/task-directory' }
    await nextTick()
    expect(workspace.surface.current).toMatchObject({ kind: 'file', id: file.id, ref: file })
    expect(explorer.source).toEqual(file)
    expect(explorer.sessionID).toBe('root')
    expect(workspace.surface.tabs).toHaveLength(2)
  })
})

describe('cached workspace motion', () => {
  const file = (id: string): FileRef => ({ id, revision: 1, created_at: '', updated_at: '', agent_id: 'bot', computer_id: 'office', path: `/work/${id}.md`, display_name: `${id}.md`, media_type: 'text/markdown', size: 100 })

  it('keeps directory and file instances through toggles and tab changes, including their reading position', async () => {
    const workspace = mount()
    await (workspace.files().props.onClick as () => void)()
    await nextTick()
    const explorer = fixture.explorers[0]
    await (workspace.files().props.onClick as () => void)()
    await nextTick()
    expect(workspace.files().props['aria-expanded']).toBe(false)
    await (workspace.files().props.onClick as () => void)()
    await nextTick()
    expect(fixture.explorers).toEqual([explorer])

    workspace.surface.open({ kind: 'file', id: 'first', sessionID: 'root', ref: file('first') })
    await nextTick()
    const first = all(workspace.root).find(item => item.props['data-file-tab'] === 'first')!
    const firstState = fixture.contents.find(item => item.id === 'first')!
    first.scrollTop = 720
    workspace.surface.open({ kind: 'file', id: 'second', sessionID: 'root', ref: file('second') })
    await nextTick()
    expect(firstState.active).toBe(false)
    expect(first.inert).toBe(true)
    workspace.surface.select(workspace.surface.tabs.find(tab => tab.id === 'first')!.key)
    await nextTick()
    expect(all(workspace.root).find(item => item.props['data-file-tab'] === 'first')).toBe(first)
    expect(first.scrollTop).toBe(720)
    expect(first.inert).toBe(false)
    expect(firstState.active).toBe(true)
    expect(fixture.contents.map(item => item.id)).toEqual(['first', 'second'])

    const activations = firstState.activations
    workspace.surface.updateFile(workspace.surface.current!.key, { ...file('first'), display_name: 'renamed.md' })
    await nextTick()
    expect(firstState.activations).toBe(activations)
    expect(all(workspace.root).find(item => item.props['data-file-tab'] === 'first')).toBe(first)
  })

  it('deactivates the leaving child in the same update and reactivates its cached instance', async () => {
    const workspace = mount()
    workspace.surface.open({ kind: 'subagent', id: 'child', title: 'Child', path: [{ id: 'root', title: 'Parent' }, { id: 'child', title: 'Child' }] })
    await nextTick()
    const child = fixture.contents.find(item => item.id === 'child')!
    const element = all(workspace.root).find(item => item.props['data-child-tab'] === 'child')!
    expect(child.active).toBe(true)

    workspace.surface.open({ kind: 'file', id: 'first', sessionID: 'root', ref: file('first') })
    await nextTick()
    expect(child.active).toBe(false)
    expect(element.inert).toBe(true)
    workspace.surface.select(workspace.surface.tabs.find(tab => tab.id === 'child')!.key)
    await nextTick()
    expect(child.active).toBe(true)
    expect(element.inert).toBe(false)
    expect(fixture.contents.filter(item => item.id === 'child')).toHaveLength(1)
  })
})
